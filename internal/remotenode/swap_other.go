//go:build !linux

package remotenode

import (
	"context"
	"fmt"
)

func EnableSwap(_ context.Context, size string) (disable func() error, err error) {
	if size == "" {
		return func() error { return nil }, nil
	}
	return nil, fmt.Errorf("swap %s: a swapfile is made on linux only", size)
}
