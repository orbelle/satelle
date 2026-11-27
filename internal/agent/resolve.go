package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/integration/common"
)

const (
	ProbeSystem = "You are a connectivity probe. Use no tools."
	ProbePrompt = "Reply with the single word ok."
)

func Resolve(b Binding, dir string) error {
	bin, args, err := common.CommandFields(b)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("command %q does not resolve: %w", bin, err)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			_, v, ok := strings.Cut(a, "=")
			if !ok {
				continue
			}
			a = v
		}
		p, ok := commandPath(a, dir)
		if !ok {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("path %q its command names does not exist", a)
		}
	}
	return nil
}

func commandPath(a, dir string) (string, bool) {
	switch {
	case strings.HasPrefix(a, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return a, true
		}
		return filepath.Join(home, a[2:]), true
	case filepath.IsAbs(a):
		return a, true
	case strings.HasPrefix(a, "./"), strings.HasPrefix(a, "../"):
		return filepath.Join(dir, a), true
	}
	return "", false
}

func Probe(ctx context.Context, b Binding, timeout time.Duration) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tmp, err := os.MkdirTemp("", "satelle-probe-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)
	pb := b
	pb.Tools = append([]string{}, common.ReadTools...)
	pb.Deny = nil
	res, err := Runner{}.Dispatch(ctx, Perform, Request{Binding: pb, System: ProbeSystem, Prompt: ProbePrompt, Dir: tmp})
	if err != nil {
		return res, err
	}
	if strings.TrimSpace(res.Text) == "" {
		return res, errors.New("answered with nothing")
	}
	return res, nil
}
