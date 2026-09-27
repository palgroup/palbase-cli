package backend

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControllerStagingStaysOutsideTheProject(t *testing.T) {
	requiresRealToolchain(t)
	useTestParserCache(t)
	dir := t.TempDir()
	mustWrite(t, dir, "modules/health.controller.ts", `
import { Controller, Get, z } from "@palbase/backend";
export const Health = z.object({ status: z.string() });
export type Health = z.infer<typeof Health>;
@Controller("/health")
export class HealthController {
  @Get("/")
  async check(): Promise<Health> { return { status: "ok" }; }
}
`)
	mustWrite(t, dir, "modules/value.ts", `export const value = "relative";`)
	mustWrite(t, dir, "probe.ts", `
import { dependency } from "fixture-dependency";
import { value } from "./modules/value";
console.log(dependency + ":" + value);
`)
	mustWrite(t, dir, "node_modules/fixture-dependency/package.json",
		`{"name":"fixture-dependency","main":"index.js"}`)
	mustWrite(t, dir, "node_modules/fixture-dependency/index.js",
		`exports.dependency = "installed";`)
	before, err := os.ReadDir(dir)
	require.NoError(t, err)
	sourcePath := filepath.Join(dir, "modules", "health.controller.ts")
	source, err := os.ReadFile(sourcePath)
	require.NoError(t, err)

	// Keep both builds alive: neither may replace the other's staged sources.
	first, err := stageControllers(context.Background(), dir, io.Discard)
	require.NoError(t, err)
	t.Cleanup(func() { removeTemp(first) })
	second, err := stageControllers(context.Background(), dir, io.Discard)
	require.NoError(t, err)
	t.Cleanup(func() { removeTemp(second) })
	require.NotEqual(t, first, second)
	for _, staged := range []string{first, second} {
		rel, err := filepath.Rel(dir, staged)
		require.NoError(t, err)
		require.False(t, filepath.IsLocal(rel), "staging must stay outside the checkout even while building")
		injected, err := os.ReadFile(filepath.Join(staged, "modules", "health.controller.ts"))
		require.NoError(t, err)
		require.Contains(t, string(injected), "palbase.backend.returnBuffer")
		bundle := filepath.Join(t.TempDir(), "probe.js")
		require.NoError(t, run(context.Background(), dir, "bun", "build",
			filepath.Join(staged, "probe.ts"), "--target=bun", "--outfile="+bundle))
		got, err := output(context.Background(), dir, "bun", bundle)
		require.NoError(t, err)
		require.Equal(t, "installed:relative\n", got)
	}
	after, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Equal(t, before, after, "no generated directory may appear while staging exists")
	unchanged, err := os.ReadFile(sourcePath)
	require.NoError(t, err)
	require.Equal(t, source, unchanged)
}

func TestControllerStagingCleansUpAfterARefusal(t *testing.T) {
	requiresRealToolchain(t)
	useTestParserCache(t)
	dir := t.TempDir()
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	mustWrite(t, dir, "broken.controller.ts", `
import { Controller, Get } from "@palbase/backend";
@Controller("/broken")
export class BrokenController {
  @Get("/")
  async check() { return { status: "missing return type" }; }
}
`)
	staged, err := stageControllers(context.Background(), dir, io.Discard)
	require.Error(t, err)
	require.Empty(t, staged)
	left, err := os.ReadDir(scratch)
	require.NoError(t, err)
	require.Empty(t, left, "a refused stage must remove its temporary sources and tools")
	require.NoDirExists(t, filepath.Join(dir, deployStagingDir))
}

// recordedThrows reads the throw bindings a stage appended to one controller:
// "<Class>.<method>" → the codes it records, sorted.
func recordedThrows(t *testing.T, staged, rel string) map[string][]string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(staged, filepath.FromSlash(rel)))
	require.NoError(t, err)
	call := regexp.MustCompile(`recordThrows\((\w+)\.prototype, "(\w+)", \[(.*?)\]\);`)
	code := regexp.MustCompile(`code:"([^"]+)"`)
	got := map[string][]string{}
	for _, m := range call.FindAllStringSubmatch(string(body), -1) {
		codes := []string{}
		for _, c := range code.FindAllStringSubmatch(m[3], -1) {
			codes = append(codes, c[1])
		}
		sort.Strings(codes)
		got[m[1]+"."+m[2]] = codes
	}
	return got
}

// WHAT A PUSH STAGES CARRIES EVERY ERROR THE ROUTE CAN THROW
// (palgroup/palbase#15, palgroup/palbase#16).
//
// The push stager is the one `x-palbase-errors` is computed from for the cloud,
// and until v0.74.0 it handed the analysis no module graph: every error behind
// an abstract port fell out of the contract (#15, Penny: 167 pairs). The same
// analysis missed three shapes the SDK itself uses (#16): an error handed to a
// transaction guard, a concrete class the container substitutes (with `super`
// and inherited calls), and a call on what a factory method returns.
//
// Each case is the issue's own reproduction, staged through the real
// stageControllers → stageForPushScript, and each route's recorded set is
// asserted EXACTLY — a missing code is the defect, an extra one is a different
// defect.
func TestPushStagingRecordsEveryErrorARouteCanThrow(t *testing.T) {
	requiresRealToolchain(t)
	useTestParserCache(t)
	dir := t.TempDir()
	done := `export const Done = z.object({ ok: z.boolean() });
export type Done = z.infer<typeof Done>;
`

	// #15 — the SDK's own NoteRepo → DbNoteRepo port (errors.md).
	mustWrite(t, dir, "modules/notes/notes.errors.ts", `import { defineError } from "@palbase/backend";
export const NoteMissing = defineError("note_missing", 404);
`)
	mustWrite(t, dir, "modules/notes/notes.repo.ts", `import { Injectable } from "@palbase/backend";
import { NoteMissing } from "./notes.errors";
export abstract class NoteRepo { abstract find(id: string): Promise<string> }
@Injectable() export class DbNoteRepo extends NoteRepo {
  async find(id: string) { if (!id) throw new NoteMissing(); return id; }
}
`)
	mustWrite(t, dir, "modules/notes/notes.controller.ts", `import { Controller, Get, z } from "@palbase/backend";
import { NoteRepo } from "./notes.repo";
`+done+`@Controller("/notes") export class NotesController {
  constructor(private readonly notes: NoteRepo) {}
  @Get("/one") async get(): Promise<Done> { await this.notes.find(""); return { ok: true }; }
}
`)
	mustWrite(t, dir, "modules/notes/notes.module.ts", `import { Module } from "@palbase/backend";
import { NotesController } from "./notes.controller";
import { DbNoteRepo } from "./notes.repo";
@Module({ controllers: [NotesController], providers: [DbNoteRepo] })
export class NotesModule {}
`)

	// #16.1 — errors handed to transaction guards.
	mustWrite(t, dir, "modules/items/items.errors.ts", `import { defineError } from "@palbase/backend";
export const ItemLocked = defineError("item_locked", 409);
export const ItemGone = defineError("item_gone", 404);
export const ItemTaken = defineError("item_taken", 409);
export const ItemsFull = defineError("items_full", 409);
`)
	mustWrite(t, dir, "modules/items/items.controller.ts", `import { Controller, Post, Database, z } from "@palbase/backend";
import { ItemLocked, ItemGone, ItemTaken, ItemsFull } from "./items.errors";
`+done+`@Controller("/items") export class ItemsController {
  @Post("/archive") async archive(): Promise<Done> {
    const id = "locked";
    if (id === "locked") throw new ItemLocked();
    await Database.$transaction((tx: any) => {
      tx.public.items.updateWhere({ id }, { archived: true }).expectOne(new ItemGone());
      return null;
    });
    return { ok: true };
  }
  @Post("/claim") async claim(): Promise<Done> {
    await Database.$transaction((tx: any) => {
      tx.public.items.select({ owner: "me" }).expectNone(new ItemTaken());
      return null;
    });
    return { ok: true };
  }
  @Post("/reserve") async reserve(): Promise<Done> {
    await Database.$transaction((tx: any) => {
      tx.public.items.select({ reserved: true }).expectAtMost(3, new ItemsFull());
      return null;
    });
    return { ok: true };
  }
}
`)
	mustWrite(t, dir, "modules/items/items.module.ts", `import { Module } from "@palbase/backend";
import { ItemsController } from "./items.controller";
@Module({ controllers: [ItemsController] })
export class ItemsModule {}
`)

	// #16.2 — a CONCRETE class the container substitutes, with super and an
	// inherited this-call.
	mustWrite(t, dir, "modules/accounts/accounts.errors.ts", `import { defineError } from "@palbase/backend";
export const AccountMissing = defineError("account_missing", 404);
export const AccountBlocked = defineError("account_blocked", 403);
`)
	mustWrite(t, dir, "modules/accounts/account.service.ts", `import { Injectable } from "@palbase/backend";
import { AccountMissing, AccountBlocked } from "./accounts.errors";
export class AccountService {
  async resolve(id: string) { return this.load(id); }
  async load(id: string) { if (!id) throw new AccountMissing(); return id; }
}
@Injectable() export class CheckedAccountService extends AccountService {
  override async resolve(id: string) {
    const account = await super.resolve(id);
    if (account === "blocked") throw new AccountBlocked();
    return account;
  }
}
`)
	mustWrite(t, dir, "modules/accounts/accounts.controller.ts", `import { Controller, Get, z } from "@palbase/backend";
import { AccountService } from "./account.service";
`+done+`@Controller("/accounts") export class AccountsController {
  constructor(private readonly accounts: AccountService) {}
  @Get("/one") async get(): Promise<Done> { await this.accounts.resolve(""); return { ok: true }; }
}
`)
	mustWrite(t, dir, "modules/accounts/accounts.module.ts", `import { Module } from "@palbase/backend";
import { AccountsController } from "./accounts.controller";
import { CheckedAccountService } from "./account.service";
@Module({ controllers: [AccountsController], providers: [CheckedAccountService] })
export class AccountsModule {}
`)

	// #16.3 — a call on the value a factory method returns: chained, and
	// through a local (the issue's own spelling).
	mustWrite(t, dir, "modules/checks/checks.errors.ts", `import { defineError } from "@palbase/backend";
export const CheckFailed = defineError("check_failed", 422);
`)
	mustWrite(t, dir, "modules/checks/checker.ts", `import { Injectable } from "@palbase/backend";
import { CheckFailed } from "./checks.errors";
export interface Checker { check(v: string): Promise<void> }
export class RealChecker implements Checker { async check(v: string) { if (!v) throw new CheckFailed(); } }
export abstract class CheckerFactory { abstract create(): Checker }
@Injectable() export class RealCheckerFactory extends CheckerFactory { create(): Checker { return new RealChecker(); } }
@Injectable() export class CheckService {
  constructor(private readonly checkers: CheckerFactory) {}
  async chained(v: string) { await this.checkers.create().check(v); }
  async viaLocal(value: string) { const checker = this.checkers.create(); await checker.check(value); }
}
`)
	mustWrite(t, dir, "modules/checks/checks.controller.ts", `import { Controller, Post, z } from "@palbase/backend";
import { CheckService } from "./checker";
`+done+`@Controller("/checks") export class ChecksController {
  constructor(private readonly checks: CheckService) {}
  @Post("/chained") async chained(): Promise<Done> { await this.checks.chained(""); return { ok: true }; }
  @Post("/local") async local(): Promise<Done> { await this.checks.viaLocal(""); return { ok: true }; }
}
`)
	mustWrite(t, dir, "modules/checks/checks.module.ts", `import { Module } from "@palbase/backend";
import { ChecksController } from "./checks.controller";
import { CheckService, RealCheckerFactory } from "./checker";
@Module({ controllers: [ChecksController], providers: [CheckService, RealCheckerFactory] })
export class ChecksModule {}
`)

	staged, err := stageControllers(context.Background(), dir, io.Discard)
	require.NoError(t, err)
	t.Cleanup(func() { removeTemp(staged) })

	for rel, want := range map[string]map[string][]string{
		"modules/notes/notes.controller.ts": {"NotesController.get": {"note_missing"}},
		"modules/items/items.controller.ts": {
			"ItemsController.archive": {"item_gone", "item_locked"},
			"ItemsController.claim":   {"item_taken"},
			"ItemsController.reserve": {"items_full"},
		},
		"modules/accounts/accounts.controller.ts": {"AccountsController.get": {"account_blocked", "account_missing"}},
		"modules/checks/checks.controller.ts": {
			"ChecksController.chained": {"check_failed"},
			"ChecksController.local":   {"check_failed"},
		},
	} {
		assert.Equal(t, want, recordedThrows(t, staged, rel), "%s: the staged throw bindings", rel)
	}
}

func TestModuleDiscoveryContinuesPastLinkedDependencies(t *testing.T) {
	dir := t.TempDir()
	dependencies := t.TempDir()
	mustWrite(t, dependencies, "vendor.module.ts", "export class VendorModule {}")
	mustWrite(t, dir, "z.module.ts", "export class ProjectModule {}")
	require.NoError(t, os.Symlink(dependencies, filepath.Join(dir, "node_modules")))
	found, err := moduleSources(dir)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(dir, "z.module.ts")}, found)
}

func TestStackBundleDoesNotDependOnTemporaryDirectory(t *testing.T) {
	requiresRealToolchain(t)
	dir := t.TempDir()
	buildableBackend(t, dir)
	var previous []byte
	for i := 0; i < 2; i++ {
		// HER TURDA FARKLI BİR BUNDLE KÖKÜ — testin adı bunu istiyor zaten.
		// Ürünler artık checkout'a değil geçici bir köke yazıldığı için, o kökün
		// adı da artefakta sızabilecek bir girdi hâline geldi; iki farklı kökle
		// koşup baytları karşılaştırmak o sızıntıyı ölçer.
		bundleRoot := t.TempDir()
		_, _, err := buildStackArtifact(context.Background(), dir, bundleRoot, io.Discard)
		require.NoError(t, err)
		bundle, err := os.ReadFile(filepath.Join(bundleRoot, ".palbase", "esm", "controllers", "controllers.js"))
		require.NoError(t, err)
		if i > 0 {
			require.True(t, string(previous) == string(bundle), "temporary directory names must not change the deployment artifact")
		}
		previous = bundle
	}
}
