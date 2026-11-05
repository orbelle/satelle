package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/procgroup"
)

// SessionEnv is the process identity SATELLE_SESSION. Acquire stamps it on the
// lease; hooks prefer it over the harness payload so a dispatched performer
// inherits the driver's stamp rather than its own session UUID.
const SessionEnv = "SATELLE_SESSION"

// SessionFromEnv returns the trimmed SATELLE_SESSION value, or empty.
func SessionFromEnv() string {
	return strings.TrimSpace(os.Getenv(SessionEnv))
}

// PublishSession records id against this process and its parent so a later
// satelle CLI in the same harness tree (story set, etc.) can ResolveSession
// without the env being set yet. The harness session_id is the value; we do
// not invent one. Each file records the process's creation identity beside the
// id, so a later process that reuses the pid cannot be mistaken for the one that
// published it (sty_36d6f155).
func PublishSession(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	dir := sessionPublishDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	for _, pid := range []int{os.Getpid(), os.Getppid()} {
		if pid <= 1 {
			continue
		}
		_, identity := procgroup.ProbeIdentity(pid)
		_ = os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)), []byte(id+"\t"+identity+"\n"), 0o600)
	}
}

// ResolveSession returns the session identity for this process: SATELLE_SESSION
// if set, else the session id a harness adapter publishes in the environment
// (the shell of a harness whose hook processes cannot see it), else a published
// id on this pid or an ancestor. Empty when no channel has a value (ordinary
// unstamped use).
func ResolveSession() string {
	if id := SessionFromEnv(); id != "" {
		return id
	}
	if id := agentcli.SessionIDFromEnv(os.Environ()); id != "" {
		return id
	}
	return publishedSession()
}

// PublishSessionModel records the model a session role reported, alongside
// the executable that reported it (the cross-provider guard's evidence,
// sty_7069bced) and an optional reason — set when model is the explicit
// "unknown" marker, naming why (an adapter-named capability-table entry,
// sty_719c4a7b AC6). role is "in-loop" | "orchestrator" | "creator". Unlike
// PublishSession this needs no pid-walk: the caller already knows sessionID
// (from ResolveSession/bindSessionID), so the file is keyed directly.
func PublishSessionModel(sessionID, role, model, executable, reason string) {
	sessionID = strings.TrimSpace(sessionID)
	role = strings.TrimSpace(role)
	if sessionID == "" || role == "" {
		return
	}
	dir := sessionModelDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	body := strings.TrimSpace(model) + "\t" + strings.TrimSpace(executable) + "\t" + strings.TrimSpace(reason) + "\n"
	_ = os.WriteFile(filepath.Join(dir, sessionModelFile(sessionID, role)), []byte(body), 0o600)
}

// ResolveSessionModel returns the model a session role previously published
// via PublishSessionModel, the executable that reported it, and the reason
// (empty unless model is "unknown" and a reason was published). Both model and
// executable are empty when nothing was published for that (sessionID, role)
// pair — the caller treats that exactly like an explicit "unknown".
func ResolveSessionModel(sessionID, role string) (model, executable, reason string) {
	sessionID = strings.TrimSpace(sessionID)
	role = strings.TrimSpace(role)
	if sessionID == "" || role == "" {
		return "", "", ""
	}
	b, err := os.ReadFile(filepath.Join(sessionModelDir(), sessionModelFile(sessionID, role)))
	if err != nil {
		return "", "", ""
	}
	parts := strings.SplitN(strings.TrimSpace(string(b)), "\t", 3)
	model = parts[0]
	if len(parts) > 1 {
		executable = parts[1]
	}
	if len(parts) > 2 {
		reason = parts[2]
	}
	return model, executable, reason
}

// sessionModelFile is the on-disk name for one (sessionID, role) pair. role is
// a small closed set of path-safe tokens ("in-loop", "orchestrator",
// "creator"); sessionID is a harness-issued UUID-shaped id, but the dot
// separator keeps the two halves unambiguous even if that ever changes.
func sessionModelFile(sessionID, role string) string {
	return "model." + sessionID + "." + role
}

// sessionModelDir is sessionPublishDir's sibling for published session
// models — same lifecycle and permissions, kept in its own subdirectory so a
// directory listing of one is never confused with the other.
func sessionModelDir() string {
	return filepath.Join(sessionPublishDir(), "models")
}

// PublishSessionRoot records, for sessionID, the repo root whose hooks serve it
// (sty_8f10499d, epic:gate-wake). A hook publishes it each time it fires; a
// satelle command that acts on another repo reads it back to find the repo its
// own session is served from, which the command's environment and working
// directory cannot say. Written only when it changed.
func PublishSessionRoot(sessionID, root string) {
	sessionID, root = strings.TrimSpace(sessionID), strings.TrimSpace(root)
	if sessionID == "" || root == "" || PublishedSessionRoot(sessionID) == root {
		return
	}
	dir := sessionPublishDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "root."+sessionID), []byte(root+"\n"), 0o600)
}

// PublishedSessionRoot reads back what PublishSessionRoot recorded ("" when no
// hook has spoken for the session).
func PublishedSessionRoot(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(sessionPublishDir(), "root."+sessionID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func sessionPublishDir() string {
	if h := strings.TrimSpace(os.Getenv("SATELLE_HOME")); h != "" {
		return filepath.Join(h, "sessions")
	}
	if r := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); r != "" {
		return filepath.Join(r, "satelle-sessions")
	}
	return filepath.Join(os.TempDir(), "satelle-sessions-"+strconv.Itoa(os.Getuid()))
}

func publishedSession() string {
	dir := sessionPublishDir()
	seen := map[int]bool{}
	pid := os.Getpid()
	for i := 0; i < 16 && pid > 1 && !seen[pid]; i++ {
		seen[pid] = true
		if id := readPublishedPID(dir, pid); id != "" {
			return id
		}
		next, ok := readParentPID(pid)
		if !ok {
			if i == 0 {
				pid = os.Getppid()
				continue
			}
			break
		}
		pid = next
	}
	return ""
}

// readPublishedPID returns the id published for pid, "" when there is none or the
// file no longer speaks for that process. A file for a process that has gone, or
// whose pid another process now holds, is removed as it is found: nothing else
// cleans the directory.
func readPublishedPID(dir string, pid int) string {
	path := filepath.Join(dir, strconv.Itoa(pid))
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	id, identity, _ := strings.Cut(strings.TrimSpace(string(b)), "\t")
	id, identity = strings.TrimSpace(id), strings.TrimSpace(identity)
	if id == "" {
		return ""
	}
	switch publishedFileSpeaks(pid, identity) {
	case fileSpeaks:
		return id
	case fileStale:
		_ = os.Remove(path)
	}
	return ""
}

type publishedVerdict int

const (
	// fileSpeaks: the file's process is the one that published it.
	fileSpeaks publishedVerdict = iota
	// fileStale: the process has gone or the pid was reused — safe to delete.
	fileStale
	// fileUnverifiable: the platform can tell processes apart but the file
	// recorded no identity (written before it did), so it is not trusted yet.
	fileUnverifiable
)

// publishedFileSpeaks judges one pid file by the process probe procgroup owns.
// A platform that yields no identity (or cannot probe) accepts a file on
// liveness alone, as before.
func publishedFileSpeaks(pid int, recorded string) publishedVerdict {
	live, current := procgroup.ProbeIdentity(pid)
	switch {
	case live == procgroup.Gone:
		return fileStale
	case current == "":
		return fileSpeaks
	case recorded == "":
		return fileUnverifiable
	case recorded != current:
		return fileStale
	}
	return fileSpeaks
}

// SessionLive reports whether a live process still publishes id as its session.
// A seat stamped with an id nothing publishes is a ghost: its session is gone,
// or the stamp never was a session (sty_36d6f155). It scans the publish
// directory, removing the stale files it passes, so call it on the deny and
// re-acquire paths only.
func SessionLive(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	dir := sessionPublishDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || e.IsDir() {
			continue
		}
		if readPublishedPID(dir, pid) == id {
			return true
		}
	}
	return false
}

// readParentPID reads PPid from /proc/<pid>/stat. The comm field may contain
// spaces and parentheses, so parse from the last ')' (same rule as supervisor).
func readParentPID(pid int) (int, bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	i := strings.LastIndex(string(raw), ")")
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(string(raw)[i+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}
