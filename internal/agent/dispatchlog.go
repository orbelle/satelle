package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/logsread"
)

type dispatchLog struct {
	mu sync.Mutex
	f  *os.File
}

func openLog(path string) *dispatchLog {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil
	}
	return &dispatchLog{f: f}
}

func closeLog(l *dispatchLog, path string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	_ = l.f.Close()
	l.mu.Unlock()
	_ = logsread.PruneDispatch(filepath.Dir(filepath.Dir(path)), config.DefaultLogsMaxFiles)
}

func (l *dispatchLog) line(prefix string, text []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.f, "%s%s\n", prefix, text)
}

func (l *dispatchLog) argv(argv []string) {
	l.line("argv: ", []byte(fmt.Sprintf("%q", argv)))
}

func (l *dispatchLog) output(prefix string, b []byte) {
	if l == nil {
		return
	}
	for _, ln := range bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n")) {
		if len(ln) > 0 {
			l.line(prefix, ln)
		}
	}
}
