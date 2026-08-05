// Package admin wires `palbase admin ...` platform-administration
// subcommands over the Management API REST transport. These are
// fleet-wide operations, not per-project — the server gates them behind a
// platform-admin allowlist (PALBASE_PLATFORM_ADMIN_USER_IDS); a
// non-admin credential gets a 403.
//
// migrate-all-tenants re-applies one module's migrations to EVERY active
// tenant DB, so old tenants don't drift when a module ships a new
// migration. It POSTs /api/v1/admin/migrate-tenants, which starts the
// orchestrator's ReconcileTenantMigrationsWorkflow, and prints the
// returned workflow id. The operation is idempotent (goose /
// golang-migrate skip already-applied versions) and fail-isolated
// server-side (one tenant's failure doesn't abort the batch).
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/palgroup/palbase-cli/internal/transport"
	"github.com/spf13/cobra"
)

// validModules are the modules the platform actively reconciles — the same set
// the orchestrator schedules a 6-hourly reconcile for (schedules.reconcileModules).
// The parent repo's "module reconcile surface parity" check fails when this, the
// mgmt API's `migrateTenantsInput` enum, and that list disagree.
//
// It stopped at palauth while the orchestrator had gained palsvc, so there was
// NO supported way to reconcile the schemas palsvc owns — including messaging's,
// after the absorb moved them there. Discovering the fleet was two migrations
// behind did not help: the command to repair it refused the module.
//
// palmessaging is deliberately NOT here even though the orchestrator can map it:
// its activity runs the standalone messaging-server image, which the absorb
// stopped building. palsvc reconciles that schema now.
var validModules = []string{"palauth", "palnotify", "paldocs", "palflags", "palsvc"}

// imageModules are the modules whose container image can be pinned on the
// module-image channel. A superset of validModules: channel-only modules
// (backend-runtime, schema-diff-proxy) have no reconcile of their own but DO
// have a pinnable image. The parity check keeps this and the mgmt API's
// `moduleImageName` enum identical.
//
// palmessaging is NOT pinnable: the absorb deleted the messaging-server build
// workflow, so no new immutable `sha-<40hex>@sha256:<64hex>` is ever published
// for it — the only ref the channel accepts. Offering the module would let an
// operator try to pin an image that cannot exist.
var imageModules = []string{"palnotify", "paldocs", "palflags", "palauth", "palsvc", "backend-runtime", "schema-diff-proxy"}

// REST is the subset of the Management-API transport the admin commands
// use. transport.Client satisfies it; tests substitute a stub.
type REST interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Resolvers lets the cobra wiring read the lazily-built REST client from
// main.go's PersistentPreRunE — mirrors branch.Resolvers' pattern.
type Resolvers struct {
	REST func() REST
}

// NewCommand returns the `palbase admin` parent command. Hidden: it is
// platform-admin only (the server 403s everyone else) — no reason to
// advertise it in every customer's --help.
func NewCommand(r Resolvers) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "admin",
		Short:  "Platform administration commands (platform-admin only)",
		Hidden: true,
	}
	cmd.AddCommand(migrateAllTenantsCmd(r.REST))
	cmd.AddCommand(setModuleImageCmd(r.REST))
	cmd.AddCommand(rotateKeyCmd(r.REST))
	return cmd
}

// migrateAllTenantsCmd wires `palbase admin migrate-all-tenants --module=X`.
// It validates the module client-side (fast feedback), then POSTs to the
// admin endpoint and surfaces the orchestrator workflow id.
func migrateAllTenantsCmd(rest func() REST) *cobra.Command {
	var (
		module  string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "migrate-all-tenants",
		Short: "Re-apply a module's migrations to every existing tenant",
		Long: "Forward-only, idempotent fleet migration reconcile.\n\n" +
			"Re-applies the chosen module's latest migrations across every active\n" +
			"tenant DB so old tenants don't drift behind a newly shipped migration.\n" +
			"Tenants already at HEAD are a no-op. The work runs server-side as an\n" +
			"orchestrator workflow; this command starts it and prints its id.\n\n" +
			"Platform-admin only (server-side allowlist).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isValidModule(module, validModules) {
				return fmt.Errorf("invalid --module %q (one of: %s)", module, moduleList(validModules))
			}
			out := cmd.OutOrStdout()

			var handle struct {
				WorkflowID string `json:"workflowId"`
			}
			body := map[string]string{"module": module}
			if err := rest().Do(cmd.Context(), http.MethodPost, "/api/v1/admin/migrate-tenants", body, &handle); err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(handle)
			}
			if _, err := fmt.Fprintf(out, "✓ fleet migration reconcile started for %q\n", module); err != nil {
				return err
			}
			_, err := fmt.Fprintf(out, "  workflow: %s\n", handle.WorkflowID)
			return err
		},
	}
	cmd.Flags().StringVar(&module, "module", "", "Module to migrate ("+moduleList(validModules)+")")
	_ = cmd.MarkFlagRequired("module")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

// setModuleImageRequest is the typed POST body for set-module-image:
// which module to pin and the container image reference to pin it to.
type setModuleImageRequest struct {
	Module string `json:"module"`
	Image  string `json:"image"`
}

// setModuleImageCmd wires `palbase admin set-module-image --module=X --image=Y`.
// It validates the module client-side (fast feedback), then POSTs to the
// admin endpoint and surfaces the orchestrator workflow id.
func setModuleImageCmd(rest func() REST) *cobra.Command {
	var (
		module  string
		image   string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "set-module-image",
		Short: "Pin a module's container image across the fleet",
		Long: "Pin a module's container image fleet-wide.\n\n" +
			"Sets the module's per-tenant migrate-Job image and its system image\n" +
			"(the control-pg module-image channel) to the given reference, then\n" +
			"triggers a canary→fleet migration reconcile so every active tenant\n" +
			"converges onto the pinned image. The work runs server-side as an\n" +
			"orchestrator workflow; this command starts it and prints its id.\n\n" +
			"Platform-admin only (server-side allowlist).",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isValidModule(module, imageModules) {
				return fmt.Errorf("invalid --module %q (one of: %s)", module, moduleList(imageModules))
			}
			if image == "" {
				return fmt.Errorf("image is required")
			}
			out := cmd.OutOrStdout()

			var handle struct {
				WorkflowID string `json:"workflowId"`
			}
			body := setModuleImageRequest{Module: module, Image: image}
			if err := rest().Do(cmd.Context(), http.MethodPost, "/api/v1/admin/set-module-image", body, &handle); err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(handle)
			}
			if _, err := fmt.Fprintf(out, "✓ module image pinned for %q → %s\n", module, image); err != nil {
				return err
			}
			_, err := fmt.Fprintf(out, "  workflow: %s\n", handle.WorkflowID)
			return err
		},
	}
	cmd.Flags().StringVar(&module, "module", "", "Module to pin ("+moduleList(imageModules)+")")
	_ = cmd.MarkFlagRequired("module")
	cmd.Flags().StringVar(&image, "image", "", "Container image reference to pin")
	_ = cmd.MarkFlagRequired("image")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}

func isValidModule(m string, list []string) bool {
	return slices.Contains(list, m)
}

// moduleList renders a module set as a pipe-separated list for help text and
// error messages (e.g. "palnotify|paldocs|palflags|palauth").
func moduleList(list []string) string {
	return strings.Join(list, "|")
}

// operationIDPattern is the server's own shape for a durable operation id: it
// validates the field as a UUID, so a malformed --operation-id is caught here
// rather than as a 400 after the round trip.
var operationIDPattern = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// rotateKeyRequest is the typed POST body for rotate-environment-key. The
// operationId is a UUID because the server derives the durable mutation id from
// it: a retry carrying the same value joins the SAME rotation rather than
// minting a second key.
type rotateKeyRequest struct {
	Ref         string `json:"ref"`
	ID          string `json:"id"`
	OperationID string `json:"operationId"`
}

// rotateKeyCmd wires `palbase admin rotate-key --ref=X --id=Y`.
//
// This is the incident path for a key the CUSTOMER surface cannot touch. The
// customer rotate filters on scope='c' AND visibility='customer', so a leaked
// service_role key — the one that bypasses a tenant's RLS — had no remedy at
// all: not in the CLI, not in the customer API, and deleting the tenant burns
// its ref permanently. The capability landed in Studio's tRPC first, meaning a
// browser; this is the same capability where an operator actually is.
//
// Platform-admin only. The server 403s everyone else — the allowlist is the
// gate, not this command's obscurity.
func rotateKeyCmd(rest func() REST) *cobra.Command {
	var (
		ref         string
		keyID       string
		operationID string
		jsonOut     bool
	)
	cmd := &cobra.Command{
		Use:   "rotate-key",
		Short: "Rotate ANY of an environment's keys, including the internal service_role one",
		Long: "Rotate one of an environment's API keys as the platform.\n\n" +
			"Unlike `palbase apikey rotate`, this reaches keys the customer surface\n" +
			"cannot: the internal service_role key that bypasses the tenant's RLS.\n" +
			"Use it when such a key leaks — the replacement is minted and the old one\n" +
			"revoked in a single durable operation, and the well-known KV slot every\n" +
			"platform consumer reads is republished before the old one is retired.\n\n" +
			"The new secret is printed ONCE. Platform-admin only (server-side allowlist).",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			var rotated struct {
				ID             string `json:"id"`
				EnvironmentRef string `json:"environment_ref"`
				Name           string `json:"name"`
				Scope          string `json:"scope"`
				IsDefault      bool   `json:"is_default"`
				Plaintext      string `json:"plaintext"`
				LookupPrefix   string `json:"lookup_prefix"`
			}
			// A fresh id per invocation is right for a NEW rotation, and wrong for
			// a retry: the server keys the durable mutation off this value, so a
			// second run with a new id mints a SECOND key. That matters because a
			// rotation can commit and still answer with an error — the reply is
			// only the delivery. --operation-id is how the operator resumes the
			// one that already happened.
			op := operationID
			if op == "" {
				op = transport.NewOperationID()
			} else if !operationIDPattern.MatchString(op) {
				return fmt.Errorf("--operation-id must be a UUID, got %q", op)
			}
			body := rotateKeyRequest{Ref: ref, ID: keyID, OperationID: op}
			if err := rest().Do(cmd.Context(), http.MethodPost,
				"/api/v1/admin/rotate-environment-key", body, &rotated); err != nil {
				// Carry the id out with the failure. Without it the operator has
				// nothing to resume with and the only available move is a fresh
				// rotation — which is exactly the wrong one if this call already
				// committed.
				return fmt.Errorf("%w\n  retry with --operation-id %s to resume THIS rotation; "+
					"a new id would rotate the key again", err, op)
			}
			if rotated.EnvironmentRef != ref {
				return fmt.Errorf(
					"rotate-key response environment_ref %q does not match the requested environment %q",
					rotated.EnvironmentRef, ref)
			}
			if jsonOut {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(rotated)
			}
			if _, err := fmt.Fprintf(out, "✓ rotated %s → %s (%s)\n", keyID, rotated.ID, ref); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out, "  %s\n", rotated.Plaintext); err != nil {
				return err
			}
			_, err := fmt.Fprintln(out, "  (shown once — the previous secret is revoked)")
			return err
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Environment ref (e.g. todoappm8p6zm)")
	_ = cmd.MarkFlagRequired("ref")
	cmd.Flags().StringVar(&keyID, "id", "", "Id of the key to rotate")
	_ = cmd.MarkFlagRequired("id")
	cmd.Flags().StringVar(&operationID, "operation-id", "",
		"Resume a rotation that already started (UUID). Omit to begin a new one")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit raw JSON")
	return cmd
}
