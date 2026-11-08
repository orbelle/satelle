// Package logsread is the single reader for satelle runtime logs, and the one
// owner of where a per-dispatch log lives and how many are kept. Storage is
// the home-keyed directory; .satelle/logs is only a pointer.
package logsread

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const prefix = "dispatch-"

// FormatDispatchName is the one filename convention for per-dispatch logs.
func FormatDispatchName(agent, storyID string, nano int64) string {
	return fmt.Sprintf("dispatch-%s-%d-%s.log", agent, nano, storyID)
}

// ParseDispatchName splits a dispatch filename. ok is false if it does not match.
func ParseDispatchName(name string) (agent, storyID string, nano int64, ok bool) {
	base := filepath.Base(name)
	if !strings.HasPrefix(base, prefix) || !strings.HasSuffix(base, ".log") {
		return "", "", 0, false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(base, prefix), ".log")
	// agent-unixnano-storyid — story id is sty_… so split from the right.
	i := strings.LastIndex(body, "-")
	if i < 0 {
		return "", "", 0, false
	}
	storyID = body[i+1:]
	rest := body[:i]
	j := strings.LastIndex(rest, "-")
	if j < 0 {
		return "", "", 0, false
	}
	agent = rest[:j]
	n, err := strconv.ParseInt(rest[j+1:], 10, 64)
	if err != nil || agent == "" || storyID == "" {
		return "", "", 0, false
	}
	return agent, storyID, n, true
}

// LastNLines returns the last n non-empty lines of raw.
func LastNLines(raw string, n int) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	parts := strings.Split(raw, "\n")
	var lines []string
	for _, p := range parts {
		if p != "" {
			lines = append(lines, p)
		}
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// File is one log file considered by Select.
type File struct {
	Path  string
	Agent string
	Story string
	Nano  int64
	Mtime time.Time
}

// ListDispatch returns dispatch logs under logsDir/dispatch.
func ListDispatch(logsDir string) ([]File, error) {
	dir := filepath.Join(logsDir, "dispatch")
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []File
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		agent, story, nano, ok := ParseDispatchName(e.Name())
		if !ok {
			continue
		}
		p := filepath.Join(dir, e.Name())
		st, err := os.Stat(p)
		mt := time.Time{}
		if err == nil {
			mt = st.ModTime()
		}
		out = append(out, File{Path: p, Agent: agent, Story: story, Nano: nano, Mtime: mt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Nano > out[j].Nano })
	return out, nil
}

// Select returns the newest dispatch file matching story and optional role.
func Select(logsDir, story, role string) (File, bool, error) {
	files, err := ListDispatch(logsDir)
	if err != nil {
		return File{}, false, err
	}
	for _, f := range files {
		if story != "" && f.Story != story {
			continue
		}
		if role != "" && f.Agent != role {
			continue
		}
		return f, true, nil
	}
	return File{}, false, nil
}

// LatestDispatchAtOrBefore picks the newest dispatch log for story (and
// optional agent) whose unixnano stamp is ≤ at.
func LatestDispatchAtOrBefore(logsDir, story, agent string, at time.Time) (File, bool, error) {
	files, err := ListDispatch(logsDir)
	if err != nil {
		return File{}, false, err
	}
	bound := at.UnixNano()
	for _, f := range files {
		if story != "" && f.Story != story {
			continue
		}
		if agent != "" && f.Agent != agent {
			continue
		}
		if f.Nano <= bound {
			return f, true, nil
		}
	}
	return File{}, false, nil
}

// DispatchPath is where one dispatch's log is written: logsDir/dispatch under
// the FormatDispatchName convention, the place ListDispatch reads.
func DispatchPath(logsDir, agent, storyID string, nano int64) string {
	return filepath.Join(logsDir, "dispatch", FormatDispatchName(agent, storyID, nano))
}

// PruneDispatch removes the oldest dispatch logs under logsDir beyond the
// newest keep.
func PruneDispatch(logsDir string, keep int) error {
	files, err := ListDispatch(logsDir)
	if err != nil {
		return err
	}
	if keep < 0 {
		keep = 0
	}
	var first error
	for i := keep; i < len(files); i++ {
		if err := os.Remove(files[i].Path); err != nil && first == nil && !os.IsNotExist(err) {
			first = err
		}
	}
	return first
}
