package kernel

import (
	"strings"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func (k *Kernel) SetOrigin(machine, role, harness string) {
	k.Ledger.Origin = &ledger.Origin{
		Machine:  machine,
		Role:     role,
		Harness:  strings.TrimSpace(harness),
		Worktree: strings.TrimPrefix(LocalLocation(k.Dir), LocationLocal),
	}
}
