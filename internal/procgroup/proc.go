package procgroup

import "os"

type Proc struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

func Self() Proc {
	pid := os.Getpid()
	_, start := ProbeIdentity(pid)
	return Proc{PID: pid, Start: start}
}

func Probe(p Proc) Liveness {
	l, start := ProbeIdentity(p.PID)
	switch {
	case l == Gone:
		return Gone
	case l != Alive || start == "" || p.Start == "":
		return Unknown
	case start != p.Start:
		return Gone
	}
	return Alive
}

const maxAncestors = 64
