package kernel

import (
	"strings"

	"github.com/bobmcallan/satelle/internal/ledger"
)

// SetOrigin makes every event this kernel's store builds carry the writer's
// origin: the machine id, the worker role, the harness when one was detected,
// and this kernel's worktree. It is provenance for tracing faults, not an
// authorisation fact.
func (k *Kernel) SetOrigin(machine, role, harness string) {
	k.Ledger.Origin = &ledger.Origin{
		Machine:  machine,
		Role:     role,
		Harness:  strings.TrimSpace(harness),
		Worktree: strings.TrimPrefix(LocalLocation(k.Dir), LocationLocal),
	}
}
