package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/board"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/verb"
)

// boardRun runs one command, requires success and returns its stdout.
func boardRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runRoot(t, args...)
	if err != nil {
		t.Fatalf("satelle %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func boardPost(t *testing.T, args ...string) verb.AgentMessage {
	t.Helper()
	var m verb.AgentMessage
	if err := json.Unmarshal([]byte(boardRun(t, append([]string{"story", "message"}, args...)...)), &m); err != nil {
		t.Fatalf("parse message: %v", err)
	}
	return m
}

func boardRows(t *testing.T, args ...string) []verb.BoardRow {
	t.Helper()
	var rows []verb.BoardRow
	if err := json.Unmarshal([]byte(boardRun(t, args...)), &rows); err != nil {
		t.Fatalf("parse board: %v", err)
	}
	return rows
}

func boardIDs(rows []verb.BoardRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// inStoreCommand runs fn inside a probe command that opens the store, because
// the in-process verbs read the app a store-backed command opened.
func inStoreCommand(t *testing.T, fn func(ctx context.Context)) {
	t.Helper()
	probe := &cobra.Command{
		Use:         "zz-board-probe",
		Annotations: needsStore(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			fn(cmd.Context())
			return nil
		},
	}
	root := NewRootCmd()
	resetFlagState(root)
	root.AddCommand(probe)
	root.SetArgs([]string{"zz-board-probe"})
	if err := executeRoot(root); err != nil {
		t.Fatal(err)
	}
}

// TestReworkEvidenceFingerprintIgnoresMessageMarks (sty_73c9aa10 AC6): marks
// appended after the evidence was fingerprinted — a dispatch marking relays
// delivered, an explicit answered — do not read as progress.
func TestReworkEvidenceFingerprintIgnoresMessageMarks(t *testing.T) {
	tempRepo(t)
	db, err := store.Open(runtimeDBPath(t))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	const id = "sty_fpmark"
	add := func(kind string, payload any, refs json.RawMessage) {
		t.Helper()
		raw, _ := json.Marshal(payload)
		if _, err := db.Ledger.Append(ctx, ledger.AppendInput{StoryID: id, Kind: kind, Actor: "t", Body: kind, Payload: raw, Refs: refs}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	fp := func() string {
		t.Helper()
		got, err := reworkEvidenceFingerprint(ctx, db.Ledger, id)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	add(ledger.KindTelemetryEvent, map[string]any{"kind": "plan-consumed"}, nil)
	add(ledger.KindAgentMessage, map[string]any{"msg_id": "msg_aaaaaaaa", "from": "orchestrator", "to": "planner", "body": "relay"}, nil)
	before := fp()
	add(ledger.KindMessageMark, board.MarkPayload{State: board.Delivered}, board.MarkRefs("msg_aaaaaaaa"))
	add(ledger.KindMessageMark, board.MarkPayload{State: board.Answered, AnswerID: "msg_bbbbbbbb"}, board.MarkRefs("msg_aaaaaaaa"))
	if after := fp(); after != before {
		t.Fatal("message marks changed the rework evidence fingerprint")
	}
	add(ledger.KindTelemetryEvent, map[string]any{"kind": "ac-evidence"}, nil)
	if after := fp(); after == before {
		t.Fatal("control: real evidence should change the fingerprint")
	}
}

// TestMessagesResolverCarriesBoardState: the resolver the dispatch engine reads
// hands it each message's folded state, so only a waiting one is marked.
func TestMessagesResolverCarriesBoardState(t *testing.T) {
	tempRepo(t)
	id := createStory(t, "resolver story")
	m := boardPost(t, id, "--from", "orchestrator", "--to", "planner", "--body", "for the planner")
	boardRun(t, "story", "message-mark", id, m.ID, "--state", "delivered")
	fresh := boardPost(t, id, "--from", "orchestrator", "--to", "planner", "--body", "still waiting")
	inStoreCommand(t, func(ctx context.Context) {
		got := messagesResolver()(ctx, id, []string{"planner"})
		if len(got) != 2 {
			t.Errorf("resolver returned %d messages, want 2", len(got))
		}
		for _, g := range got {
			want := "waiting"
			if g.ID == m.ID {
				want = "delivered"
			}
			if g.ID != m.ID && g.ID != fresh.ID || g.State != want {
				t.Errorf("message %s state = %q, want %q", g.ID, g.State, want)
			}
		}
	})
}

// TestDeliveredCallbackAppendsMarks: the callback the engine is wired with
// appends real message_mark rows that fold to delivered, naming the session
// that recorded them (sty_73c9aa10 AC5).
func TestDeliveredCallbackAppendsMarks(t *testing.T) {
	tempRepo(t)
	t.Setenv(config.SessionEnv, "sess-dispatch")
	id := createStory(t, "delivery story")
	a := boardPost(t, id, "--from", "orchestrator", "--to", "planner", "--body", "one")
	b := boardPost(t, id, "--from", "orchestrator", "--to", "planner", "--body", "two")
	inStoreCommand(t, func(ctx context.Context) {
		if err := messagesDelivered()(ctx, id, []string{a.ID, b.ID}); err != nil {
			t.Errorf("delivered callback: %v", err)
		}
		if err := messagesDelivered()(ctx, id, []string{"msg_00000000"}); err == nil {
			t.Error("a mark for an unknown message was reported as written")
		}
	})
	var listed []verb.AgentMessage
	if err := json.Unmarshal([]byte(boardRun(t, "story", "messages", id)), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("messages = %d, want 2", len(listed))
	}
	for _, m := range listed {
		if m.State != "delivered" {
			t.Errorf("message %s state = %q, want delivered", m.ID, m.State)
		}
	}
}
