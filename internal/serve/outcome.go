package serve

// Sync outcomes the CLI reports to satelled (sty_0367d20c).
//
// A push or snapshot child can exit 0 without converging: a push that only held
// in-flight stories, or a snapshot that stopped on a story in conflict. The exit
// code cannot say so, so satelled passes --outcome-json and the verb ends its
// stdout with one JSON line counting what it left behind. Drifted and refused
// pushes are not counted here: they exit non-zero, and that stays their signal.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// outcomeFlag asks a push or snapshot verb for its closing outcome line.
const outcomeFlag = "--outcome-json"

// syncOutcome is the closing stdout line of a verb run with outcomeFlag.
type syncOutcome struct {
	Held     int `json:"held"`
	Conflict int `json:"conflict"`
}

// parseOutcome reads the last non-empty stdout line as a syncOutcome. Anything
// else — prose from an older binary, or malformed JSON — reads as clean, so a
// binary that does not report never makes satelled back off; ok says whether a
// line was found.
func parseOutcome(out []byte) (o syncOutcome, ok bool) {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	last := bytes.TrimSpace(lines[len(lines)-1])
	if len(last) == 0 || last[0] != '{' {
		return syncOutcome{}, false
	}
	if err := json.Unmarshal(last, &o); err != nil {
		return syncOutcome{}, false
	}
	return o, true
}

// errHeldOnly is a push that exited 0 having held in-flight stories. Held is in
// flight, not failure (sty_c203fd62): the Pusher records it as a success and
// schedules no re-run; the push after the story leaves flight publishes it.
type errHeldOnly struct{ n int }

func (e errHeldOnly) Error() string { return fmt.Sprintf("held %d", e.n) }

// errUnconverged is a snapshot that exited 0 stopped on n stories in conflict.
// The Reconciler backs that repo off until a local change touches it.
type errUnconverged struct {
	n    int
	what string
}

func (e errUnconverged) Error() string { return fmt.Sprintf("%s %d", e.what, e.n) }

// heldCount reports how many stories a push held, or 0 when err is not a held
// outcome.
func heldCount(err error) int {
	var h errHeldOnly
	if errors.As(err, &h) {
		return h.n
	}
	return 0
}

// unconverged reports whether err is a snapshot's conflict stop.
func unconverged(err error) (errUnconverged, bool) {
	var u errUnconverged
	ok := errors.As(err, &u)
	return u, ok
}
