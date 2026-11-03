package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/gatehandle"
	"github.com/bobmcallan/satelle/internal/help"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfdot"
)

// AC2: completion reaches the driver only by a notification the harness
// delivers. A status command that answers "pending" — or "done" — for a handle
// is the polling path this story exists to remove, so these tests fail if the
// driver has any verb or command to complete a wait with.

const pollMarker = "ZZ-VERDICT-MARKER-91f3"

// finishedUndelivered leaves a finished run nobody has been told about, whose
// output carries a marker no read surface may reveal.
func finishedUndelivered(t *testing.T) gatehandle.Meta {
	t.Helper()
	store := gateStoreForTest(t)
	m, err := store.Create(gatehandle.Meta{Verb: "story-set", Story: "sty_poll", Argv: []string{"story", "set", "sty_poll", "--status", "done"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(store.OutPath(m.ID), []byte(pollMarker+"\n"), 0o644)
	_ = os.WriteFile(store.ErrPath(m.ID), []byte("accepted plan→in_progress "+pollMarker+"\n"), 0o644)
	if err := store.Finish(m.ID, gatehandle.Result{}); err != nil {
		t.Fatal(err)
	}
	return m
}

func revealsHandle(out string, m gatehandle.Meta) bool {
	return strings.Contains(out, pollMarker) || strings.Contains(out, m.ID)
}

// Every read surface — CLI commands and registered read verbs — run against a
// finished-but-undelivered handle, and none may reveal the verdict or that the
// handle exists. Invoking the command that started the wait again must not
// answer from the handle either: it starts its own run.
func TestNoSurfaceLetsTheDriverPollAHandle(t *testing.T) {
	_ = tempRepo(t)
	clearHarnessEnv(t)
	out, err := runRoot(t, "story", "create", "--title", "Poll fixture", "--acceptance", "1. x")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	var created map[string]any
	if jerr := json.Unmarshal([]byte(out), &created); jerr != nil {
		t.Fatalf("parse create: %v\n%s", jerr, out)
	}
	id, _ := created["id"].(string)
	m := finishedUndelivered(t)

	reads := [][]string{
		{"story", "get", id}, {"story", "list"}, {"story", "route", id}, {"story", "docs", id},
		{"story", "messages", id}, {"story", "seat"}, {"story", "cost", id}, {"story", "diff", id},
		{"story", "proof", id}, {"task", "list"}, {"execution", "list"}, {"ledger", "list"},
		{"ledger", "list", "--story", id}, {"status"}, {"doctor"}, {"runtime", "list"},
		{"workflow", "list"}, {"doc", "list"},
	}
	for _, args := range reads {
		got, _ := runRoot(t, args...)
		if revealsHandle(got, m) {
			t.Errorf("`satelle %s` revealed a finished, undelivered gate:\n%s", strings.Join(args, " "), got)
		}
	}

	// Every registered read verb, asked about the story and about nothing.
	readSuffixes := []string{"-get", "-list", "-docs", "-doc", "-messages", "-route", "-seat", "-diff", "-cost", "-proof", "-actual"}
	for _, name := range verb.Catalog() {
		isRead := false
		for _, s := range readSuffixes {
			isRead = isRead || strings.HasSuffix(name, s)
		}
		if !isRead || name == "story-actual" { // story-actual records; the rest only read
			continue
		}
		for _, req := range []string{`{"id":"` + id + `"}`, `{}`} {
			raw, _ := verb.Dispatch(context.Background(), name, json.RawMessage(req))
			if revealsHandle(string(raw), m) {
				t.Errorf("verb %s %s revealed a finished, undelivered gate:\n%s", name, req, raw)
			}
		}
	}

	// The verb that started the wait, invoked again, runs a fresh gate — it does
	// not answer from the old handle.
	args := []string{"gatetest", "--say", "again"}
	useGateHandOff(t, "20s", args)
	again, _ := runRoot(t, args...)
	if revealsHandle(again, m) {
		t.Errorf("re-invoking the verb answered from the earlier handle:\n%s", again)
	}
	if !strings.Contains(again, "accepted: again") {
		t.Errorf("re-invoking the verb did not run its own gate:\n%s", again)
	}
}

// The structural half: the only code allowed to read a handle's outcome is the
// parent replaying its own run (gatecaller.go) and the harness-hook delivery
// (gatedeliver.go, and gateresume.go for a harness whose turn cannot carry the
// verdict — the hook starts a watcher that claims and delivers, and answers no
// caller). A verb, the web surface or a new command importing the store is the
// first step toward a status command.
func TestOnlyDeliveryAndReplayReadAHandle(t *testing.T) {
	allowed := map[string]bool{
		filepath.Join("internal", "gatehandle"):           true,
		filepath.Join("internal", "cli", "gatecaller.go"): true,
		// Imports only the process probe (ProbeIdentity), to tell a session's pid
		// file from one a later process wrote on a reused pid (sty_36d6f155); it
		// reads no handle.
		filepath.Join("internal", "config", "session.go"):  true,
		filepath.Join("internal", "cli", "gatedeliver.go"): true,
		filepath.Join("internal", "cli", "gateresume.go"):  true,
		// Where delivery looks for a session's handles across repos (sty_8f10499d):
		// it lists and claims for the hooks and the watcher, and answers no caller.
		filepath.Join("internal", "cli", "gatexrepo.go"):                 true,
		filepath.Join("internal", "cli", "gatexrepo_test.go"):            true,
		filepath.Join("internal", "cli", "gateresume_test.go"):           true,
		filepath.Join("internal", "cli", "gateclaude_test.go"):           true,
		filepath.Join("internal", "cli", "gatecaller_test.go"):           true,
		filepath.Join("internal", "cli", "gatepoll_test.go"):             true,
		filepath.Join("internal", "cli", "gateverdict_test.go"):          true,
		filepath.Join("internal", "cli", "gatestopwake_test.go"):         true,
		filepath.Join("internal", "cli", "gatesettle_test.go"):           true,
		filepath.Join("internal", "cli", "harness_pi_gatewake_test.go"):  true,
		filepath.Join("internal", "cli", "cursor_gate_delivery_test.go"): true,
		// gatecaller_test.go's slowest tests, behind the clislow tag (sty_210df6c3).
		filepath.Join("internal", "cli", "gatecaller_slow_test.go"): true,
	}
	root := filepath.Join("..", "..")
	var offenders []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if allowed[rel] || allowed[filepath.Dir(rel)] {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr == nil && strings.Contains(string(b), `"github.com/bobmcallan/satelle/internal/gatehandle"`) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if len(offenders) > 0 {
		t.Fatalf("these files import the gate-handle store; only gatecaller.go and gatedeliver.go may read a handle's outcome — anything else is a polling surface: %v", offenders)
	}
}

// AC5: help and the engage text both name the notification path and forbid
// polling a backgrounded command — the two phrases an agent must read.
func TestGateWaitTextNamesNotificationPathAndForbidsPolling(t *testing.T) {
	const path = "delivered into this session as a notification"
	const ban = "poll, sleep-loop, or re-run a backgrounded command"

	top, ok := help.Get("agent-dispatch")
	if !ok {
		t.Fatal("agent-dispatch help topic missing")
	}
	for label, body := range map[string]string{
		"help agent-dispatch":       top.Body,
		"standing reminder":         hookPromptReminder,
		"engaged form (with route)": engagedFormForTest(t),
	} {
		if !strings.Contains(body, path) {
			t.Errorf("%s does not name the notification path (%q)", label, path)
		}
		if !strings.Contains(body, ban) {
			t.Errorf("%s does not forbid polling a backgrounded command (%q)", label, ban)
		}
	}
}

func engagedFormForTest(t *testing.T) string {
	t.Helper()
	now := time.Now().UTC()
	info := seatInfo{
		ItemID: "sty_x", State: "in_progress", HeartbeatAt: now.Add(-6 * time.Second), Engaged: true,
		Advance: []wfdot.Advance{{To: "integration", Gates: []string{"satelle-code-ac-review"}}},
	}
	out := formatEngagedPrompt(info, now)
	if out == "" {
		t.Fatal("the engaged form degraded to nothing: the note must survive the ceiling ladder")
	}
	return out
}
