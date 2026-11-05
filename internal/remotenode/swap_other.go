//go:build !linux

package remotenode

import (
	"context"
	"fmt"
)

// EnableSwap makes a swapfile on linux only: a node runs on linux. Elsewhere a
// definition asking for swap fails the start rather than running without it.
func EnableSwap(_ context.Context, size string) (disable func() error, err error) {
	if size == "" {
		return func() error { return nil }, nil
	}
	return nil, fmt.Errorf("swap %s: a swapfile is made on linux only", size)
}
