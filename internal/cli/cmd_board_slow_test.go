//go:build clislow

package cli

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/bobmcallan/satelle/internal/board"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/verb"
)

// TestSlowBoardCLIRoundTrip drives the whole surface through the real commands
// (sty_73c9aa10 AC2, AC4, AC7) — and under a hosted dialer that fails the test
// if anything dials out, with no satelle.dev configured (AC9).
func TestSlowBoardCLIRoundTrip(t *testing.T) {
	tempRepo(t)
	t.Setenv(config.SessionEnv, "sess-sender")
	hosted.SetTestGRPCDial(func(context.Context, string) (*grpc.ClientConn, error) {
		t.Error("the message board dialled the hosted server")
		return nil, context.Canceled
	})
	t.Cleanup(func() { hosted.SetTestGRPCDial(nil) })

	one := createStory(t, "board story one")
	two := createStory(t, "board story two")

	// A bare --to keeps meaning a role; the sender is recorded.
	role := boardPost(t, one, "--from", "orchestrator", "--to", "planner", "--body", "role message")
	if role.To != "planner" || role.FromSessionID != "sess-sender" || role.FromSessionName == "" {
		t.Errorf("role message = %+v", role)
	}
	if !strings.HasPrefix(role.FromLocation, "loc_") {
		t.Errorf("location %q is not a loc_ id", role.FromLocation)
	}
	if !regexp.MustCompile(`^msg_[0-9a-f]{8}$`).MatchString(role.ID) {
		t.Errorf("id %q", role.ID)
	}
	sess := boardPost(t, one, "--from", "human", "--to", "session:peer", "--body", "to a session", "--deadline", "1ms")
	story := boardPost(t, two, "--from", "human", "--to", "story", "--body", "to the work")
	prefixed := boardPost(t, two, "--from", "human", "--to", "role:reviewer", "--body", "prefixed role")
	if sess.To != "session:peer" || story.To != "story:"+two || prefixed.To != "reviewer" {
		t.Errorf("addressees: %q %q %q", sess.To, story.To, prefixed.To)
	}
	if sess.Deadline == nil || sess.Deadline.Location() != time.UTC {
		t.Errorf("deadline = %v, want absolute UTC", sess.Deadline)
	}

	// A body over 64 KiB is refused, on the command.
	if out, err := runRoot(t, "story", "message", one, "--from", "human", "--body", strings.Repeat("x", board.MaxBody+1)); err == nil {
		t.Errorf("an oversize body was accepted: %.80s", out)
	}

	// A reply answers the original.
	reply := boardPost(t, one, "--from", "planner", "--to", "orchestrator", "--body", "done", "--reply-to", role.ID)
	if reply.InReplyTo != role.ID {
		t.Errorf("reply links to %q", reply.InReplyTo)
	}

	// Explicit marks, and what the CLI refuses.
	boardRun(t, "story", "message-mark", two, story.ID, "--state", "delivered")
	boardRun(t, "story", "message-mark", two, prefixed.ID, "--state", "answered", "--answer", story.ID)
	boardRun(t, "story", "message-mark", one, sess.ID, "--state", "undeliverable")
	for name, args := range map[string][]string{
		"an unknown message": {"story", "message-mark", one, "msg_00000000", "--state", "delivered"},
		"an invalid state":   {"story", "message-mark", one, role.ID, "--state", "read"},
		"no state":           {"story", "message-mark", one, role.ID},
		"a bad reply-to":     {"story", "message", one, "--from", "human", "--body", "x", "--reply-to", "msg_00000000"},
	} {
		if out, err := runRoot(t, args...); err == nil {
			t.Errorf("%s was accepted: %.80s", name, out)
		}
	}

	// story messages: sender, addressee, state, deadline, overdue.
	var listed []verb.AgentMessage
	if err := json.Unmarshal([]byte(boardRun(t, "story", "messages", one)), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 {
		t.Fatalf("story messages = %d, want 3", len(listed))
	}
	states := map[string]string{}
	for _, m := range listed {
		states[m.ID] = m.State
		if m.FromSessionName == "" {
			t.Errorf("message %s lists no sender name", m.ID)
		}
	}
	if states[role.ID] != "answered" || states[sess.ID] != "undeliverable" || states[reply.ID] != "waiting" {
		t.Errorf("states = %v", states)
	}
	if got := boardRows(t, "board", "--story", one, "--overdue"); len(got) != 1 || got[0].ID != sess.ID || !got[0].Overdue {
		t.Errorf("board --overdue = %v", boardIDs(got))
	}

	// board spans every story, with each filter.
	all := boardRows(t, "board")
	if len(all) != 5 {
		t.Fatalf("board = %v, want the 5 messages across both stories", boardIDs(all))
	}
	stories := map[string]bool{}
	for _, r := range all {
		stories[r.StoryID] = true
	}
	if !stories[one] || !stories[two] {
		t.Errorf("board covers %v, want both stories", stories)
	}
	if got := boardRows(t, "board", "--state", "waiting"); len(got) != 1 || got[0].ID != reply.ID {
		t.Errorf("board --state waiting = %v", boardIDs(got))
	}
	if got := boardRows(t, "board", "--state", "undeliverable"); len(got) != 1 || got[0].ID != sess.ID {
		t.Errorf("board --state undeliverable = %v", boardIDs(got))
	}
	if got := boardRows(t, "board", "--story", two); len(got) != 2 {
		t.Errorf("board --story = %v, want 2", boardIDs(got))
	}
	if got := boardRows(t, "board", "--session", "peer"); len(got) != 1 || got[0].ID != sess.ID {
		t.Errorf("board --session peer = %v", boardIDs(got))
	}
	if got := boardRows(t, "board", "--session", "sess-sender"); len(got) != 5 {
		t.Errorf("board --session sess-sender = %v, want every message it sent", boardIDs(got))
	}
	if out, err := runRoot(t, "board", "--state", "late"); err == nil {
		t.Errorf("a bad --state was accepted: %.80s", out)
	}
}
