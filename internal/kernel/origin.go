package kernel

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/ledger"
)

func (k *Kernel) SetOrigin(machine, role, harness string) error {
	if !slices.Contains(ledger.Roles(), role) {
		return fmt.Errorf("kernel: %q is not a worker role (%s)", role, strings.Join(ledger.Roles(), ", "))
	}
	k.Ledger.Origin = &ledger.Origin{
		Machine:  machine,
		Role:     role,
		Harness:  strings.TrimSpace(harness),
		Worktree: strings.TrimPrefix(LocalLocation(k.Dir), LocationLocal),
	}
	return nil
}
