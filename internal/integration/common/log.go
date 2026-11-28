package common

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Log struct {
	mu    sync.Mutex
	f     *os.File
	clean func(string) string
}

func (l *Log) Redacting(clean func(string) string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.clean = clean
	l.mu.Unlock()
}

func OpenLog(path string) *Log {
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
	return &Log{f: f}
}

func (l *Log) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	_ = l.f.Close()
	l.mu.Unlock()
}

func (l *Log) Line(prefix string, text []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.clean != nil {
		text = []byte(l.clean(string(text)))
	}
	_, _ = fmt.Fprintf(l.f, "%s%s\n", prefix, text)
}

func (l *Log) Argv(argv []string) {
	l.Line("argv: ", []byte(fmt.Sprintf("%q", argv)))
}

func (l *Log) Output(prefix string, b []byte) {
	if l == nil {
		return
	}
	for _, ln := range bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n")) {
		if len(ln) > 0 {
			l.Line(prefix, ln)
		}
	}
}
