//go:build unix && !linux && !darwin

package procgroup

func osProbe(pid int) (Liveness, string) { return signalLiveness(pid), "" }
