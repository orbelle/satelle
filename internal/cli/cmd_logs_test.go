package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/logfile"
	"github.com/bobmcallan/satelle/internal/mirror"
)

func TestLogsDoctor(t *testing.T) {
	isolate(t)
	path := mirror.DoctorLogPath(config.GlobalDir())
	if out := must(t, "logs", "--doctor"); strings.TrimSpace(out) != "no doctor actions logged at "+path {
		t.Errorf("--doctor with no log: %q, want it to name %s", out, path)
	}
	if out, err := run(t, "", "logs", "--doctor", "--server"); err == nil {
		t.Errorf("--doctor --server exited 0:\n%s", out)
	}
	if out := must(t, "logs", "--doctor", "--path"); strings.TrimSpace(out) != path {
		t.Errorf("--doctor --path: %q, want %s", out, path)
	}
	now := time.Now()
	for _, line := range []string{"first line", "last line"} {
		if err := logfile.Append(now, path, logfile.DefaultConfig, line); err != nil {
			t.Fatal(err)
		}
	}
	if out, want := must(t, "logs", "--doctor", "--tail", "1"), "# "+path+"\nlast line\n"; out != want {
		t.Errorf("--doctor --tail 1: %q, want %q", out, want)
	}
}
