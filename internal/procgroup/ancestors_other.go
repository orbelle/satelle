//go:build !linux && !darwin

package procgroup

func Ancestors(int) []Proc { return nil }
