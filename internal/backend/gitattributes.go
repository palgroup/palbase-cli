package backend

// gitattributes.go — RETIRED (FR-011).
//
// `palbase link` wrote `palbase/.gitattributes` on every run, marking every
// generated client, contract and barrel `linguist-generated=true -diff` so that
// a spec fetch collapsed to one line in review. The marking was right about
// review noise and wrong about ownership: it was a SECOND file this tool put in
// somebody's repository — hidden, rewritten on every link, and never asked for.
//
// The rule now is one generated path per checkout (D-6, FR-001). A person who
// wants the markers writes them in their own `.gitattributes`, where they can
// see them and keep them; and the file this CLI already wrote is swept as a
// retired artifact (`retiredProjectPaths`), because fixing the producer does not
// remove what it produced.
//
// This file stays as the note rather than disappearing, so the next person
// looking for where the attributes went finds the reason instead of a gap.
