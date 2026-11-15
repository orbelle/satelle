package remotenode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
)

var SwapDir = "/var/swap"

var SwapRunner = func(ctx context.Context, argv []string) error {
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("%s: exit status %d: %s", argv[0], exit.ExitCode(), bounded(out))
		}
		return fmt.Errorf("%s: could not start", argv[0])
	}
	return nil
}

func bounded(out []byte) string {
	const limit = 512
	if len(out) > limit {
		out = out[:limit]
	}
	return string(out)
}

func EnableSwap(ctx context.Context, size string) (disable func() error, err error) {
	if size == "" {
		return func() error { return nil }, nil
	}
	var nonce [6]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, fmt.Errorf("swap: %w", err)
	}
	file := filepath.Join(SwapDir, "satelle-"+hex.EncodeToString(nonce[:]))
	remove := func(ctx context.Context) error { return SwapRunner(ctx, []string{"rm", "-f", file}) }
	steps := [][]string{
		{"mkdir", "-p", SwapDir},
		{"fallocate", "-l", size, file},
		{"chmod", "600", file},
		{"mkswap", file},
		{"swapon", file},
	}
	for i, argv := range steps {
		if rerr := SwapRunner(ctx, argv); rerr != nil {
			if i > 0 {
				_ = remove(context.WithoutCancel(ctx))
			}
			return nil, fmt.Errorf("swap %s: %w", size, rerr)
		}
	}
	return func() error {
		dctx := context.WithoutCancel(ctx)
		off := SwapRunner(dctx, []string{"swapoff", file})
		return errors.Join(off, remove(dctx))
	}, nil
}
