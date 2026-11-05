//go:build unix && !linux && !darwin

package procgroup

// osProbe is the signal-0 existence check only: this platform has no reader for
// a process's creation identity here, so a live process is reported with none.
// A run's handle then carries IdentityUnavailable, and a session waiting on it
// is told once, plainly, that it cannot rely on the wait.
func osProbe(pid int) (Liveness, string) { return signalLiveness(pid), "" }
