//go:build integration && darwin

package tests

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const launchdFake = `#!/bin/sh
st=$LAUNCHD_FAKE
echo "$*" >> "$st/log"
sub=$1
label=${2##*/}
start() {
	plist=$1
	name=$2
	grep -q '<key>KeepAlive</key>' "$plist" || return 0
	set --
	i=0
	while a=$(/usr/bin/plutil -extract ProgramArguments.$i raw -o - "$plist" 2>/dev/null); do
		set -- "$@" "$a"
		i=$((i+1))
	done
	nohup "$@" >"$st/$name.out" 2>&1 &
	echo $! > "$st/$name.pid"
}
stop() {
	if [ -f "$st/$1.pid" ]; then kill "$(cat "$st/$1.pid")" 2>/dev/null; rm -f "$st/$1.pid"; fi
}
case "$sub" in
bootstrap)
	label=$(basename "$3" .plist)
	: > "$st/$label.loaded"
	cp "$3" "$st/$label.plist"
	start "$3" "$label";;
bootout)
	stop "$label"
	rm -f "$st/$label.loaded";;
kickstart)
	label=${3##*/}
	stop "$label"
	start "$st/$label.plist" "$label";;
print)
	[ -f "$st/$label.loaded" ] || exit 113
	echo "state = running";;
esac
exit 0
`

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func copyExec(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

func answers(port int, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func fakePid(t *testing.T, state string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(state, "dev.satelle.satelled.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func TestServiceOnLaunchd(t *testing.T) {
	s := newSandbox(t)
	state := t.TempDir()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "launchctl"), []byte(launchdFake), 0o755); err != nil {
		t.Fatal(err)
	}
	s.path = append(s.path, bin)
	s.env["LAUNCHD_FAKE"] = state
	t.Cleanup(func() {
		pids, _ := filepath.Glob(filepath.Join(state, "*.pid"))
		for _, p := range pids {
			if b, err := os.ReadFile(p); err == nil {
				if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
			}
		}
	})
	inst := t.TempDir()
	cli, daemon := filepath.Join(inst, "satelle"), filepath.Join(inst, "satelled")
	copyExec(t, satelleBin, cli)
	copyExec(t, satelledBin, daemon)
	port := freeTCPPort(t)
	agents := filepath.Join(s.home, "Library", "LaunchAgents")
	dp, sp := filepath.Join(agents, "dev.satelle.satelled.plist"), filepath.Join(agents, "dev.satelle.sync.plist")
	must := func(t *testing.T, args ...string) string {
		t.Helper()
		r := s.exec(cli, "", args...)
		if r.code != 0 {
			t.Fatalf("%v: exit %d\n%s%s", args, r.code, r.out, r.err)
		}
		return r.out
	}

	t.Run("install", func(t *testing.T) {
		must(t, "service", "install", "--port", strconv.Itoa(port))
		for _, p := range []string{dp, sp} {
			if out, err := exec.Command("/usr/bin/plutil", "-lint", p).CombinedOutput(); err != nil {
				t.Errorf("plutil -lint %s: %v\n%s", p, err, out)
			}
		}
		if !answers(port, 10*time.Second) {
			t.Fatalf("satelled started from %s does not answer /healthz on %d", dp, port)
		}
	})

	t.Run("refresh", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "satelled")
		copyExec(t, satelledBin, other)
		b, err := os.ReadFile(dp)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dp, []byte(strings.Replace(string(b), "<string>"+daemon+"</string>", "<string>"+other+"</string>", 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		before := fakePid(t, state)
		out := must(t, "service", "refresh")
		if !strings.Contains(out, "wrote "+dp+" (was "+other+")") {
			t.Errorf("refresh output:\n%s", out)
		}
		if after := fakePid(t, state); after == 0 || after == before {
			t.Errorf("satelled pid %d after refresh, %d before", after, before)
		}
		if !answers(port, 10*time.Second) {
			t.Fatal("satelled does not answer after refresh")
		}
	})

	t.Run("doctor", func(t *testing.T) {
		must(t, "service", "uninstall")
		var rep struct {
			Issues []struct{ Check, Problem, Fix string } `json:"issues"`
		}
		r := s.exec(cli, "", "doctor", "--json")
		if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
			t.Fatalf("doctor --json: %v\n%s", err, r.out)
		}
		found := false
		for _, is := range rep.Issues {
			if is.Check == "versions" && strings.Contains(is.Problem, "launch agent") && strings.Contains(is.Problem, "dev.satelle.satelled") && is.Fix == "satelle service install" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no missing launch agent issue:\n%s", r.out)
		}
		s.exec(cli, "", "doctor", "--resolve", "--json")
		if !answers(port, 10*time.Second) {
			t.Fatal("satelled does not answer after doctor --resolve")
		}
		rep.Issues = nil
		r = s.exec(cli, "", "doctor", "--json")
		if err := json.Unmarshal([]byte(r.out), &rep); err != nil {
			t.Fatalf("doctor --json: %v\n%s", err, r.out)
		}
		for _, is := range rep.Issues {
			if is.Check == "versions" && strings.Contains(is.Problem, "launch agent") {
				t.Errorf("issue left after --resolve: %+v", is)
			}
		}
	})
}
