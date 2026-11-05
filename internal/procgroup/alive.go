package procgroup

// Liveness is what a platform could say about a process.
type Liveness int

const (
	// Gone: no such process (a zombie counts).
	Gone Liveness = iota
	// Alive: the process exists.
	Alive
	// Unknown: the platform could not tell (access denied).
	Unknown
)

// ProbeIdentity reports whether pid is alive and, where the platform can say,
// an identity for it (its start time), so a reused pid is told apart.
func ProbeIdentity(pid int) (Liveness, string) {
	if pid <= 0 {
		return Gone, ""
	}
	return osProbe(pid)
}
