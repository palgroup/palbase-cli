package backend

// Reading a bounded body without lying about how much of it there was.
//
// `io.ReadAll(io.LimitReader(body, N))` reads like a safe read and is not one.
// The limit reader stops at N and reports a CLEAN EOF; io.ReadAll sees a normal
// end of stream and returns a nil error; the caller gets N bytes and no
// indication that there were more. A body cut in half is handed back as a
// complete one — silence, reported as success.
//
// What that costs is measured, not theoretical: `palbase pull` unpacked the
// first megabyte of a 3.4 MB bundle and died at the far end with "read tar
// entry: unexpected EOF". The truncation was silent and only the symptom was
// loud, and the symptom pointed at the archive rather than at the door.
//
// So there is ONE reader for capped bodies and it reads one byte PAST the cap.
// If that byte exists the answer was longer than this call can hold, and the
// only honest thing to do with it is refuse — a caller that receives a
// truncated body cannot tell it from a complete one, which is precisely why
// each site cannot be trusted to remember the +1 for itself.

import (
	"fmt"
	"io"
)

// readCapped reads at most `limit` bytes from r, and refuses a body longer than
// that instead of returning a truncated one.
//
// `what` names the source in the refusal — a URL or a path. "More than 1048576
// bytes" with nothing to attach it to sends the reader looking through every
// request the command made.
//
// It does NOT choose the limit. Each caller keeps the cap it already had:
// retuning those numbers is a separate judgement about what each door is for,
// and folding it into a change about honesty would hide it.
func readCapped(r io.Reader, limit int64, what string) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		// Nothing is returned beside the error. Handing back the partial bytes
		// would recreate the defect one level up, where somebody would use them
		// "just for the error message" and then for everything else.
		return nil, fmt.Errorf(
			"%s answered with more than %d bytes and the answer was cut there; "+
				"a truncated body cannot be told from a complete one, so it is refused rather than parsed",
			what, limit)
	}
	return raw, nil
}
