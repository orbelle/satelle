//go:build !linux && !darwin && !windows

package procgroup

func Ancestors(int) []Proc { return nil }
