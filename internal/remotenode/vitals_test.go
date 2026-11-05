package remotenode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vitalsFixture writes a proc root with meminfo and, unless withoutPressure,
// pressure/memory, and points procRoot at it for the test.
func vitalsFixture(t *testing.T, withoutPressure bool) string {
	t.Helper()
	root := t.TempDir()
	meminfo := "MemTotal:        8048640 kB\nMemFree:          100000 kB\nMemAvailable:    2097152 kB\nSwapCached:            0 kB\nSwapTotal:       8388608 kB\nSwapFree:        7340032 kB\n"
	if err := os.WriteFile(filepath.Join(root, "meminfo"), []byte(meminfo), 0o644); err != nil {
		t.Fatal(err)
	}
	if !withoutPressure {
		if err := os.MkdirAll(filepath.Join(root, "pressure"), 0o755); err != nil {
			t.Fatal(err)
		}
		psi := "some avg10=12.50 avg60=3.00 avg300=1.00 total=123\nfull avg10=4.25 avg60=1.00 avg300=0.10 total=45\n"
		if err := os.WriteFile(filepath.Join(root, "pressure", "memory"), []byte(psi), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = prev })
	return root
}

// AC3: the host's memory, swap and memory-pressure figures are read from
// /proc/meminfo and /proc/pressure/memory.
func TestReadVitalsParsesMeminfoAndPressure(t *testing.T) {
	v := ReadVitals(vitalsFixture(t, false))
	want := Vitals{MemTotal: 8048640, MemAvailable: 2097152, SwapTotal: 8388608, SwapFree: 7340032, PSISome10: 12.5, PSIFull10: 4.25}
	if v != want {
		t.Errorf("vitals = %+v, want %+v", v, want)
	}
	if got, line := v.String(), "mem avail=2048MiB/7860MiB swap used=1024MiB/8192MiB psi some=12.50 full=4.25"; got != line {
		t.Errorf("String = %q, want %q", got, line)
	}
}

// A host with no pressure file (an older kernel) still reports memory and swap,
// and names what it could not read.
func TestReadVitalsMissingPressure(t *testing.T) {
	v := ReadVitals(vitalsFixture(t, true))
	if v.MemTotal != 8048640 || v.PSISome10 != 0 || v.PSIFull10 != 0 || !strings.Contains(v.Err, "pressure") {
		t.Errorf("vitals = %+v, want memory read, PSI zero and the pressure file named in Err", v)
	}
	if empty := ReadVitals(filepath.Join(t.TempDir(), "none")); !strings.Contains(empty.Err, "meminfo") || empty.MemTotal != 0 {
		t.Errorf("vitals of a missing root = %+v, want zero figures and meminfo named", empty)
	}
}
