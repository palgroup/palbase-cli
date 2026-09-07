package backend

import (
	"context"
	"fmt"
	"io"
)

func finishStackPush(ctx context.Context, w io.Writer, out pushResult, refresh func(context.Context, io.Writer) error) error {
	if out.Schema.Changed {
		fmt.Fprintln(w, "schema:")
		for _, line := range out.Schema.Summary {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if out.Unchanged && !out.Schema.Changed {
		fmt.Fprintf(w, "already live: %s — no changes to push\n", short(out.Digest))
		return nil
	}
	fmt.Fprintf(w, "live: %d endpoint(s), %s\n", out.EndpointCount, short(out.Digest))
	if err := refresh(ctx, w); err != nil {
		return fmt.Errorf("the push landed, but the client could not be regenerated: %w", err)
	}
	return nil
}

func writePushRuntime(w io.Writer, builtWith, running string, readErr error) {
	if builtWith == "" {
		return
	}
	if readErr != nil || running == "" {
		fmt.Fprintln(w, "runtime: could not verify the running SDK after the code upload; image migration is unconfirmed")
		return
	}
	if running != builtWith {
		fmt.Fprintf(w, "runtime: still @palbase/backend %s; code was built with %s — image migration is NOT complete\n", running, builtWith)
		return
	}
	fmt.Fprintf(w, "runtime: verified @palbase/backend %s\n", running)
}
