package remotenode

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot is where host vitals are read from; tests point it at a fixture.
var procRoot = "/proc"

// Vitals are the host's memory, swap and memory-pressure figures at one moment,
// logged with each heartbeat so a node that stalls (a paused sprite, a host out
// of memory) leaves evidence of the state it was in. Memory and swap are in kB,
// as /proc/meminfo gives them; PSI is the 10s average of /proc/pressure/memory.
type Vitals struct {
	MemTotal, MemAvailable uint64
	SwapTotal, SwapFree    uint64
	PSISome10, PSIFull10   float64
	Err                    string // what could not be read, empty when all was
}

// ReadVitals reads <root>/meminfo and <root>/pressure/memory. A file that cannot
// be read leaves its fields zero and is noted in Err; it never fails.
func ReadVitals(root string) Vitals {
	var v Vitals
	var errs []string
	if err := v.readMeminfo(filepath.Join(root, "meminfo")); err != nil {
		errs = append(errs, err.Error())
	}
	if err := v.readPressure(filepath.Join(root, "pressure", "memory")); err != nil {
		errs = append(errs, err.Error())
	}
	v.Err = strings.Join(errs, "; ")
	return v
}

func (v *Vitals) readMeminfo(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("meminfo: %w", err)
	}
	defer f.Close()
	fields := map[string]*uint64{"MemTotal": &v.MemTotal, "MemAvailable": &v.MemAvailable, "SwapTotal": &v.SwapTotal, "SwapFree": &v.SwapFree}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		dst, want := fields[key]
		if !ok || !want {
			continue
		}
		if kb := strings.Fields(rest); len(kb) > 0 {
			*dst, _ = strconv.ParseUint(kb[0], 10, 64)
		}
	}
	return sc.Err()
}

func (v *Vitals) readPressure(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("pressure: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		var dst *float64
		switch f[0] {
		case "some":
			dst = &v.PSISome10
		case "full":
			dst = &v.PSIFull10
		default:
			continue
		}
		for _, kv := range f[1:] {
			if val, ok := strings.CutPrefix(kv, "avg10="); ok {
				*dst, _ = strconv.ParseFloat(val, 64)
			}
		}
	}
	return nil
}

// String is the heartbeat's vitals line: available and total memory, used and
// total swap (MiB), and the 10s memory-pressure averages.
func (v Vitals) String() string {
	const mib = 1024
	var swapUsed uint64
	if v.SwapTotal > v.SwapFree {
		swapUsed = v.SwapTotal - v.SwapFree
	}
	s := fmt.Sprintf("mem avail=%dMiB/%dMiB swap used=%dMiB/%dMiB psi some=%.2f full=%.2f",
		v.MemAvailable/mib, v.MemTotal/mib, swapUsed/mib, v.SwapTotal/mib, v.PSISome10, v.PSIFull10)
	if v.Err != "" {
		s += " err=" + v.Err
	}
	return s
}
