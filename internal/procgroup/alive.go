package procgroup

type Liveness int

const (
	Gone Liveness = iota
	Alive
	Unknown
)

func ProbeIdentity(pid int) (Liveness, string) {
	if pid <= 0 {
		return Gone, ""
	}
	return osProbe(pid)
}
