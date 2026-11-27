//go:build !linux

package capacity

import (
	"fmt"
	"runtime"
)

func readMemory() (total, avail uint64, err error) {
	return 0, 0, fmt.Errorf("unavailable on %s", runtime.GOOS)
}

func readLoad() ([3]float64, error) {
	return [3]float64{}, fmt.Errorf("unavailable on %s", runtime.GOOS)
}
