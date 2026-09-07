package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Wired by the CLI's cloud composition root; a project key cannot act as an
// account credential against the control plane.
var CloudRuntimePreparer func(context.Context, string, string) error

func prepareStackRuntime(ctx context.Context, dir string, target Target, cred Credentials, approve bool, out io.Writer) error {
	if target.OnThisMachine() || !isCloudProjectAddress(target.URL) {
		return nil
	}
	return prepareCloudRuntime(ctx, dir, target, cred, approve, out)
}

func prepareCloudRuntime(ctx context.Context, dir string, target Target, cred Credentials, approve bool, out io.Writer) error {
	want := installedBackendVersion(dir)
	if want == "" {
		return errors.New("cannot determine the SDK required by this checkout")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	running, err := projectSDKVersion(probeCtx, target, cred)
	cancel()
	if err != nil || running == "" {
		return errors.New("could not verify the running SDK; no image or code was changed")
	}
	if running == want {
		return nil
	}
	// A schema refusal must not replace a healthy image as a side effect.
	if err := checkRuntimeSchemaPlan(ctx, dir, target, cred, approve, out); err != nil {
		return err
	}
	if CloudRuntimePreparer == nil {
		return errors.New("this CLI cannot prepare cloud runtimes; update the CLI before pushing a different SDK")
	}
	fmt.Fprintf(out, "runtime: preparing @palbase/backend %s (currently %s)\n", want, running)
	swapCtx, cancelSwap := context.WithTimeout(ctx, 5*time.Minute)
	err = CloudRuntimePreparer(swapCtx, target.URL, want)
	cancelSwap()
	if err != nil {
		return fmt.Errorf("runtime preparation failed before code upload: %w", err)
	}
	probeCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	running, err = projectSDKVersion(probeCtx, target, cred)
	cancel()
	if err != nil || running != want {
		return fmt.Errorf("runtime preparation was not verified: required %s, observed %q; no code was uploaded", want, running)
	}
	fmt.Fprintf(out, "runtime: verified @palbase/backend %s before code upload\n", running)
	return nil
}

func checkRuntimeSchemaPlan(ctx context.Context, dir string, target Target, cred Credentials, approve bool, out io.Writer) error {
	sources, err := ReadSchemaSources(dir)
	if errors.Is(err, ErrNoSchema) {
		return nil
	}
	if err != nil {
		return err
	}
	payload, err := SchemaSourcesBody(sources)
	if err != nil {
		return err
	}
	status, body, err := managementCall(ctx, target, cred, http.MethodPost, "/v1/management/schema/plan", payload, "application/json")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("schema preflight returned HTTP %d; no image or code was changed: %s", status, trimBody(body))
	}
	var plan schemaPlanWire
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields["in_sync"] == nil {
		return errors.New("schema preflight did not return a schema plan; no image or code was changed")
	}
	if err := json.Unmarshal(body, &plan); err != nil {
		return fmt.Errorf("invalid schema preflight; no image or code was changed: %w", err)
	}
	blocked := len(plan.Incompatible) > 0 || len(plan.Unsupported) > 0
	if !approve {
		for _, drop := range plan.Destructive {
			if drop.Rows > 0 && (drop.Column == "" || drop.NonNull == nil || *drop.NonNull > 0) {
				blocked = true
			}
		}
	}
	if blocked {
		renderSchemaPlan(out, body)
		return errors.New("schema preflight refused the push; no image or code was changed")
	}
	return nil
}
