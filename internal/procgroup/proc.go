package procgroup

import "os"

// Proc names one process across time: its pid and the start identity
// ProbeIdentity reports for it, so a pid reused by another process is told
// apart.
type Proc struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

// Self is the calling process.
func Self() Proc {
	pid := os.Getpid()
	_, start := ProbeIdentity(pid)
	return Proc{PID: pid, Start: start}
}

// Probe reports whether p still runs: alive only when its pid runs with the
// recorded start identity; gone when the pid does not run, or runs with another
// start identity (a reused pid); otherwise unknown (the platform could not
// say, or p recorded no identity).
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

// maxAncestors bounds Ancestors' walk.
const maxAncestors = 64
