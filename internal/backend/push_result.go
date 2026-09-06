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
