package backend

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// waitlistController is palgroup/palbase#30's route: open to anonymous callers,
// and still asking for a `User` it is not guaranteed.
func waitlistController(userDecorator, userType string) string {
	return `
import { Controller, Post, Body, ` + userDecorator + `, z } from "@palbase/backend";

export const JoinWaitlistBody = z.object({ email: z.string() });
export type JoinWaitlistBody = z.infer<typeof JoinWaitlistBody>;
export const WaitlistResult = z.object({ joined: z.boolean() });
export type WaitlistResult = z.infer<typeof WaitlistResult>;

@Controller("/areas")
export class AreasController {
  @Post("/waitlist", { auth: false })
  joinWaitlist(
    @Body(JoinWaitlistBody) body: JoinWaitlistBody,
    @` + userDecorator + `() user: ` + userType + `,
  ): WaitlistResult {
    return { joined: body.email.length > 0 && user !== undefined };
  }
}
`
}

// `@User()` ON A ROUTE ANONYMOUS CALLERS REACH IS REFUSED BY THE BUILD.
//
// Measured on a cloud tenant (palgroup/palbase#30, 2026-10-04): `palbase build`
// and `palbase push` accepted it, an anonymous call injected null, and the
// handler's `user.id` answered 500 — found only by the deploy's own e2e check,
// four pushes in a row. The SDK refuses it in `createApp`; the build asks the
// same function, so the two answers are one answer.
func TestAUserOnAnOpenRouteIsRefusedByTheBuild(t *testing.T) {
	requiresRealToolchain(t)
	dir := t.TempDir()
	ctxPack, cancelPack := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelPack()
	sdk := packLocalSDK(t, ctxPack)
	if !npmInstallProject(t, dir, sdk, "typescript@^5", "zod-to-json-schema") {
		t.Skip("node/npm unavailable or the install failed")
	}
	useTestParserCache(t)

	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	write("modules/areas/areas.module.ts", `
import { Module, type Token } from "@palbase/backend";
import { AreasController } from "./areas.controller.ts";

@Module({ controllers: [AreasController as Token] })
export class AreasModule {}
`)
	write("modules/areas/areas.controller.ts", waitlistController("User", "{ id: string }"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var out bytes.Buffer
	err := runBuild(ctx, dir, &out)
	require.Error(t, err, "@User() on an open route was accepted:\n%s", out.String())
	require.Contains(t, out.String(), "AreasController.joinWaitlist (POST /areas/waitlist)",
		"the refusal must name the class, the method and the route")
	require.Contains(t, out.String(), "@OptionalUser()", "the refusal must name the cure")

	// NEGATIVE CONTROL: the same route with @OptionalUser() and `| null` builds.
	write("modules/areas/areas.controller.ts", waitlistController("OptionalUser", "{ id: string } | null"))
	out.Reset()
	require.NoError(t, runBuild(ctx, dir, &out), "@OptionalUser() on an open route was refused:\n%s", out.String())
	require.Contains(t, out.String(), "build OK")
}
