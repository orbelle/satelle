//go:build linux

package capacity

import (
	"errors"
	"os"
)

func readMemory() (total, avail uint64, err error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	total, avail, ok := parseMeminfo(string(b))
	if !ok {
		return 0, 0, errors.New("/proc/meminfo has no MemTotal and MemAvailable")
	}
	return total, avail, nil
}

func readLoad() ([3]float64, error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return [3]float64{}, err
	}
	l, ok := parseLoadavg(string(b))
	if !ok {
		return l, errors.New("/proc/loadavg is not three load averages")
	}
	return l, nil
}
