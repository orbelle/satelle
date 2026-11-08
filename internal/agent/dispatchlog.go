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

// dispatchLog is one dispatch's log: the command line and every line
// exchanged with the harness, in the order written. Writes from the adapter's
// reader and writer goroutines are serialised. A nil log, or one whose writes
// fail, writes nothing and never fails the dispatch.
type dispatchLog struct {
	mu sync.Mutex
	f  *os.File
}

// openLog opens path for a dispatch's log (dirs 0700, file 0600); nil when
// path is "" or the file cannot be created.
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

// closeLog closes the log and prunes the oldest dispatch logs beyond the kept
// bound. path is the log's path: its directory's parent is the logs dir.
func closeLog(l *dispatchLog, path string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	_ = l.f.Close()
	l.mu.Unlock()
	_ = logsread.PruneDispatch(filepath.Dir(filepath.Dir(path)), config.DefaultLogsMaxFiles)
}

// line writes one line: prefix then text.
func (l *dispatchLog) line(prefix string, text []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = fmt.Fprintf(l.f, "%s%s\n", prefix, text)
}

// argv writes the command line a run starts.
func (l *dispatchLog) argv(argv []string) {
	l.line("argv: ", []byte(fmt.Sprintf("%q", argv)))
}

// output writes each line of b under prefix.
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
