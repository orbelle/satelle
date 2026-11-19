package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type launchctlFake struct {
	FailOn string
	Stuck  bool
}

const launchctlScript = `#!/bin/sh
echo "$*" >> %[1]q
state=%[2]q
failon=%[3]q
stuck=%[4]q
sub=$1
label=${2##*/}
case "$sub" in
bootstrap)
	if [ "$failon" = bootstrap ]; then echo "Bootstrap failed: 5: Input/output error"; exit 5; fi
	label=$(basename "$3" .plist)
	: > "$state/$label.loaded"
	exit 0;;
bootout)
	[ -n "$stuck" ] || rm -f "$state/$label.loaded"
	if [ "$failon" = bootout ]; then echo "Boot-out failed: 5: Input/output error"; exit 5; fi
	exit 0;;
print)
	if [ -f "$state/fail-print" ]; then echo "Bad request."; exit 5; fi
	if [ -f "$state/$label.loaded" ]; then printf '%%s = {\n\tstate = running\n}\n' "$2"; exit 0; fi
	echo "Could not find service \"$label\" in domain"
	exit 113;;
*)
	if [ "$failon" = "$sub" ]; then echo "$sub failed"; exit 1; fi
	exit 0;;
esac
`

func fakeLaunchctl(t *testing.T, opt launchctlFake) (log string) {
	t.Helper()
	bin := t.TempDir()
	dir := t.TempDir()
	log = filepath.Join(dir, "launchctl.log")
	state := launchctlState(log)
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	stuck := ""
	if opt.Stuck {
		stuck = "1"
	}
	writeExec(t, filepath.Join(bin, "launchctl"), fmt.Sprintf(launchctlScript, log, state, opt.FailOn, stuck))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func launchctlState(log string) string { return filepath.Join(filepath.Dir(log), "state") }
