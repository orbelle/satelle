//go:build !linux && !darwin && !windows

package procgroup

// Ancestors is nil where the platform has no reader for a process's parent
// here: sessions are then never proven alive or gone.
func Ancestors(int) []Proc { return nil }
