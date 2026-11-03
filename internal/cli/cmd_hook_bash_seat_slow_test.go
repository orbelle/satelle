//go:build clislow

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestSlowBashGateForeignSeat (sty_bc78617c AC2/AC3): with a live seat held by a
// different session, a read-only shell command and writes confined to temp
// paths pass both PreToolUse handlers, while real tree mutations are still
// refused as not attributed to the seat holder.
func TestSlowBashGateForeignSeat(t *testing.T) {
	seatHeldBySessionA(t)
	scratch := filepath.Join(t.TempDir(), "scratchpad")
	t.Setenv("SCRATCH", scratch)
	t.Setenv("TMPDIR", t.TempDir())

	allowed := []string{
		"sed -n 332,463p internal/foo.go",
		"mkdir -p " + scratch + "/octop; curl -sfL https://x/y -o " + scratch + "/octop/f",
		"mkdir -p /tmp/sty_bc78617c_octop; curl -sfL https://x/y -o /tmp/sty_bc78617c_octop/f",
		"mkdir -p $SCRATCH/octop",
		"mkdir -p ${TMPDIR}/octop && touch $TMPDIR/octop/f",
		"echo x > $SCRATCH/out",
	}
	for _, c := range allowed {
		for _, sub := range []string{"gate", "commitgate"} {
			if out, err := runRootIn(t, sessionBBashEvent(c), "hook", sub); err != nil {
				t.Errorf("hook %s denied %q under a foreign seat: %v\n%s", sub, c, err, out)
			}
		}
	}

	denied := []string{
		"sed -i s/a/b/ internal/foo.go",
		"sed -i 's/a/b/' internal/foo.go",
		`sed -i "s/a/b/" internal/foo.go`,
		"sed -i -e 's/a/b/' internal/foo.go",
		"rm internal/x.go",
		"echo x > internal/x.go",
		"mkdir -p internal/newdir",
		"mkdir -p $NO_SUCH_VAR_FOR_TEST/octop",
	}
	for _, c := range denied {
		for _, sub := range []string{"gate", "commitgate"} {
			out, err := runRootIn(t, sessionBBashEvent(c), "hook", sub)
			if err == nil {
				t.Errorf("hook %s allowed %q under a foreign seat:\n%s", sub, c, out)
				continue
			}
			if !strings.Contains(out, "holds the live engagement seat") {
				t.Errorf("hook %s deny of %q is not the seat-holder reason:\n%s", sub, c, out)
			}
		}
	}
}

// TestSlowBashScratchRedirectAnySeatState (sty_e642df17): a shell command whose only
// writes are redirects into an exempt location passes the PreToolUse hook with no
// story engaged by this session, whatever the seat state — quoted or not, with a
// descriptor duplication after it — while a redirect into the repo tree is still
// refused with that state's reason.
func TestSlowBashScratchRedirectAnySeatState(t *testing.T) {
	setups := bashSeatStateSetups()
	allowed := []string{
		`make test > "/tmp/sty_e642df17/maketest.log" 2>&1`,
		`cmd > "/tmp/sty_e642df17/x.log"`,
		`cmd > '/tmp/sty_e642df17/x.log'`,
		`cmd >"/tmp/sty_e642df17/x.log" 2>>"/tmp/sty_e642df17/err.log"`,
		`cmd &> /tmp/sty_e642df17/x.log`,
		`cmd > /tmp/sty_e642df17/x.log 2>&1`,
		"cat >> '/tmp/sty_e642df17/out.md' <<'EOF'\nbody\nEOF",
	}
	denied := []string{
		`echo x > internal/foo.go`,
		`echo x > "README.md" 2>&1`,
		`echo x > './README.md'`,
		`echo x > "$UNSET_VAR_FOR_TEST/x.log"`,
		`echo x > "$(pwd)/README.md"`,
		// A variable assigned earlier in the same command is not resolved: gated.
		"F=/tmp/sty_e642df17/out.md; cat >> $F <<'EOF'\nbody\nEOF",
	}
	checkBashWritesAnySeatState(t, setups, allowed, denied)
}

// TestSlowBashWrappedQuotedTeeAnySeatState (sty_e79be13a): a quoted tee destination
// gets the verdict of the bare one when tee runs behind a wrapper command — refused
// into the repo tree with that seat state's reason, passed into an exempt location
// whatever the seat state.
func TestSlowBashWrappedQuotedTeeAnySeatState(t *testing.T) {
	denied := []string{
		`true | env tee README.md`, // unquoted control
		`true | env tee "README.md"`,
		`sudo tee 'internal/x.go'`,
		`nohup tee -a "README.md"`,
		`xargs tee "README.md"`,
		`stdbuf -o0 tee "README.md"`,
		`echo x | tee "README.md"`,
	}
	allowed := []string{
		`true | env tee "/tmp/sty_e79be13a/x.log"`,
		`sudo tee '/tmp/sty_e79be13a/x.log'`,
		`nohup tee -a "/tmp/sty_e79be13a/x.log"`,
		`xargs tee "/tmp/sty_e79be13a/x.log"`,
		`stdbuf -o0 tee "/tmp/sty_e79be13a/x.log"`,
		`echo x | tee "/tmp/sty_e79be13a/x.log" > /dev/null`,
	}
	checkBashWritesAnySeatState(t, bashSeatStateSetups(), allowed, denied)
}
