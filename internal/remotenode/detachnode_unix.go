//go:build unix

package remotenode

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func StartDetachedRun(o StartDetachedOptions) error {
	if o.Out == nil {
		o.Out = io.Discard
	}
	var payload []byte
	if o.Stdin != nil {
		b, err := io.ReadAll(io.LimitReader(o.Stdin, maxSecretPayload+1))
		if err != nil {
			return fmt.Errorf("reading the run's stdin: %w", err)
		}
		if len(b) > maxSecretPayload {
			return fmt.Errorf("the secrets on stdin are over %d bytes", maxSecretPayload)
		}
		payload = b
	}
	out := filepath.Join(o.Dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	script := `"$0" node run-local --dir "$1"`
	if o.Progress {
		script += " --progress"
	}
	script += ` >"$2" 2>&1; echo $? >"$3"`
	cmd := exec.Command("sh", "-c", script, o.Self, o.Dir, filepath.Join(out, detachedLog), filepath.Join(out, detachedExit))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdin = r
	if err := cmd.Start(); err != nil {
		return errors.Join(err, r.Close(), w.Close())
	}
	rerr := r.Close()
	_, werr := w.Write(payload)
	cerr := w.Close()
	pid := cmd.Process.Pid
	if err := errors.Join(rerr, werr, cerr, cmd.Process.Release()); err != nil {
		return fmt.Errorf("handing the node's run its stdin: %w", err)
	}
	fmt.Fprintf(o.Out, "node run started detached as process %d\n", pid)
	return nil
}
