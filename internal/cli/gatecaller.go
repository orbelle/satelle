package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/bobmcallan/satelle/internal/config"
)

// Gates run synchronously (REBUILD Q-gate-async): a reviewer-running verb holds
// its caller until the verdict is in. What remains here is where the gate's
// one-line progress goes and how a quiet failure exits.

// stderrIsTerminal is a var so tests can stand in for a person at a terminal.
var stderrIsTerminal = func() bool {
	st, err := os.Stderr.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// gateProgressSink is where the reviewer's one-line progress goes: stderr for a
// person at a terminal; dropped for an agent caller, whose progress lines are
// tokens it pays to read and cannot act on.
func gateProgressSink(cfg config.Config, repoRoot, runtimeDir string) func(string) {
	if config.IsAgentCaller() || !stderrIsTerminal() {
		return func(string) {}
	}
	return func(msg string) { fmt.Fprintln(os.Stderr, msg) }
}

// finishExecute maps Execute's error onto the exit contract and reports whether
// err should still be printed.
func finishExecute(err error) (out error, print bool) {
	var ee *ExitError
	switch {
	case err == nil:
		return nil, false
	case errors.As(err, &ee) && ee.Quiet:
		return err, false
	}
	return err, true
}
