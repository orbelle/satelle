package verb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/board"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/workitem"
)

func init() {
	Register(&Verb{
		Name:        "story-message",
		Description: "Append a directed agent message to a story",
		Invoke:      storyMessage,
	})
	Register(&Verb{
		Name:        "story-messages",
		Description: "List directed agent messages on a story (read-only)",
		Invoke:      storyMessages,
	})
	Register(&Verb{
		Name:        "story-message-mark",
		Description: "Record a message's delivery state: delivered, answered or undeliverable",
		Invoke:      storyMessageMarkVerb,
	})
}

// AgentMessage is the stored/returned shape of one agent_message row
// (decision-agent-messaging.md). Story id and created_at live on the ledger
// row; the JSON payload carries from/to/body/engagement_sha.
type AgentMessage struct {
	ID   string `json:"id,omitempty"`
	From string `json:"from"`
	To   string `json:"to"`
	// Cc is an ADDITIONAL address the row is readable under, while To stays who
	// the message is FOR. It exists because a directed conversation can still be
	// context for a third party: the rework relay ledgers real `coder`/`consult`
	// directions AND needs the transcript in the edge reviewer's payload, and a
	// single To field can only do one of those (sty_8e0b29a0). Empty is the
	// overwhelming case and reads exactly as before — no reader admits "".
	Cc            string    `json:"cc,omitempty"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	EngagementSHA string    `json:"engagement_sha"`
	// Envelope is who sent the message and from where, its msg_ id, reply link
	// and deadline (sty_73c9aa10). To is the stored address token: a role name,
	// "*", "story:<id>" or "session:<id>" (board.Address).
	board.Envelope
	// State, AnswerID, UpdatedAt and Overdue are output only: the state folded
	// from the message's message_mark rows, and the overdue flag derived at read
	// time. They are never part of a stored payload.
	State     string    `json:"state,omitempty"`
	AnswerID  string    `json:"answer_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	Overdue   bool      `json:"overdue,omitempty"`
}

type storyMessageReq struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to,omitempty"`
	Cc   string `json:"cc,omitempty"`
	Body string `json:"body"`
	// Deadline is a Go duration from now or an RFC3339 instant; InReplyTo a msg_
	// id on this story; the From* fields identify the sending session.
	Deadline        string `json:"deadline,omitempty"`
	InReplyTo       string `json:"in_reply_to,omitempty"`
	FromSessionID   string `json:"from_session_id,omitempty"`
	FromSessionName string `json:"from_session_name,omitempty"`
	FromLocation    string `json:"from_location,omitempty"`
}

type storyMessagesReq struct {
	ID      string `json:"id"`
	Since   string `json:"since,omitempty"`
	To      string `json:"to,omitempty"`
	State   string `json:"state,omitempty"`
	Overdue bool   `json:"overdue,omitempty"`
}

func storyMessage(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	store, err := requireWorkItem()
	if err != nil {
		return nil, err
	}
	ls, err := requireLedger()
	if err != nil {
		return nil, err
	}
	var req storyMessageReq
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.ID)
	from := strings.TrimSpace(req.From)
	body := strings.TrimSpace(req.Body)
	if id == "" {
		return nil, fmt.Errorf("story-message: id required")
	}
	if from == "" {
		return nil, fmt.Errorf("story-message: from required")
	}
	if body == "" {
		return nil, fmt.Errorf("story-message: body required")
	}
	if len(body) > board.MaxBody {
		return nil, fmt.Errorf("story-message: body is %d bytes; the cap is %d", len(body), board.MaxBody)
	}
	it, err := store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if it.Kind != workitem.KindStory {
		return nil, fmt.Errorf("story-message: %s is not a story", id)
	}
	to, err := board.Address(req.To, id)
	if err != nil {
		return nil, fmt.Errorf("story-message: %w", err)
	}
	now := time.Now()
	env := board.Envelope{
		MsgID:           board.NewID(),
		InReplyTo:       strings.TrimSpace(req.InReplyTo),
		FromSessionID:   strings.TrimSpace(req.FromSessionID),
		FromSessionName: strings.TrimSpace(req.FromSessionName),
		FromLocation:    strings.TrimSpace(req.FromLocation),
	}
	deadline, err := board.ParseDeadline(req.Deadline, now)
	if err != nil {
		return nil, fmt.Errorf("story-message: %w", err)
	}
	if !deadline.IsZero() {
		env.Deadline = &deadline
	}
	if env.InReplyTo != "" {
		if _, ok, ferr := findMessage(ctx, id, env.InReplyTo); ferr != nil {
			return nil, ferr
		} else if !ok {
			return nil, fmt.Errorf("story-message: --reply-to %s is not a message on %s", env.InReplyTo, id)
		}
	}
	engagementSHA := ""
	if sha, _, ok := currentEngagementWindow(ctx, id); ok {
		engagementSHA = sha
	}
	payload, err := json.Marshal(AgentMessage{
		From:          from,
		To:            to,
		Cc:            strings.TrimSpace(req.Cc),
		Body:          body,
		EngagementSHA: engagementSHA,
		Envelope:      env,
	})
	if err != nil {
		return nil, err
	}
	e, err := ls.Append(ctx, ledger.AppendInput{
		StoryID: id,
		Kind:    ledger.KindAgentMessage,
		Actor:   from,
		Body:    body,
		Payload: payload,
	}, now)
	if err != nil {
		return nil, err
	}
	if env.InReplyTo != "" {
		// A reply answers the message it names; the mark carries the reply's id.
		if err := appendMark(ctx, ls, id, env.InReplyTo, board.Answered, env.MsgID, env.FromSessionID); err != nil {
			return nil, err
		}
	}
	m, _, err := findMessage(ctx, id, board.ID(e))
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// MarkRequest is one state change of a message, for in-process callers (the
// dispatch engine, and the delivery paths that follow it) and for the
// story-message-mark verb.
type MarkRequest struct {
	// ID is the story; Message the msg_ id on it.
	ID      string `json:"id"`
	Message string `json:"message"`
	State   string `json:"state"`
	// AnswerID names the reply that answered the message; only an answered mark
	// may carry it.
	AnswerID string `json:"answer_id,omitempty"`
	// By is the session recording the mark.
	By string `json:"by_session_id,omitempty"`
}

// StoryMessageMark records a delivered, answered or undeliverable mark for a
// message on a story and returns the message with its new state. It is the one
// writer of message_mark rows from outside this file. An unknown message, a
// waiting state (a message with no mark already is) or an answer on any state
// but answered is refused.
func StoryMessageMark(ctx context.Context, req MarkRequest) (AgentMessage, error) {
	ls, err := requireLedger()
	if err != nil {
		return AgentMessage{}, err
	}
	id, msg := strings.TrimSpace(req.ID), strings.TrimSpace(req.Message)
	if id == "" || msg == "" {
		return AgentMessage{}, fmt.Errorf("story-message-mark: story id and message id required")
	}
	state := board.State(strings.TrimSpace(req.State))
	if state == board.Waiting || !state.Valid() {
		return AgentMessage{}, fmt.Errorf("story-message-mark: state %q must be delivered, answered or undeliverable", req.State)
	}
	answer := strings.TrimSpace(req.AnswerID)
	if answer != "" && state != board.Answered {
		return AgentMessage{}, fmt.Errorf("story-message-mark: --answer only goes with state answered")
	}
	if _, ok, err := findMessage(ctx, id, msg); err != nil {
		return AgentMessage{}, err
	} else if !ok {
		return AgentMessage{}, fmt.Errorf("story-message-mark: %s is not a message on %s", msg, id)
	}
	if answer != "" {
		if _, ok, err := findMessage(ctx, id, answer); err != nil {
			return AgentMessage{}, err
		} else if !ok {
			return AgentMessage{}, fmt.Errorf("story-message-mark: --answer %s is not a message on %s", answer, id)
		}
	}
	if err := appendMark(ctx, ls, id, msg, state, answer, strings.TrimSpace(req.By)); err != nil {
		return AgentMessage{}, err
	}
	m, _, err := findMessage(ctx, id, msg)
	return m, err
}

func storyMessageMarkVerb(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	if _, err := requireWorkItem(); err != nil {
		return nil, err
	}
	var req MarkRequest
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	m, err := StoryMessageMark(ctx, req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// appendMark writes one message_mark row. Callers have already checked the
// message exists.
func appendMark(ctx context.Context, ls *ledger.Store, storyID, msgID string, state board.State, answerID, by string) error {
	payload, err := json.Marshal(board.MarkPayload{State: state, AnswerID: answerID, BySessionID: by})
	if err != nil {
		return err
	}
	_, err = ls.Append(ctx, ledger.AppendInput{
		StoryID: storyID,
		Kind:    ledger.KindMessageMark,
		Actor:   "satelle",
		Payload: payload,
		Refs:    board.MarkRefs(msgID),
	}, time.Now())
	return err
}

// findMessage returns the story's message with the given msg_ id, state folded.
func findMessage(ctx context.Context, storyID, msgID string) (AgentMessage, bool, error) {
	msgs, err := listAgentMessages(ctx, storyID)
	if err != nil {
		return AgentMessage{}, false, err
	}
	for _, m := range msgs {
		if m.ID == msgID {
			return m, true, nil
		}
	}
	return AgentMessage{}, false, nil
}

func storyMessages(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	store, err := requireWorkItem()
	if err != nil {
		return nil, err
	}
	var req storyMessagesReq
	if err := decode(raw, &req); err != nil {
		return nil, err
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return nil, fmt.Errorf("story-messages: id required")
	}
	it, err := store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if it.Kind != workitem.KindStory {
		return nil, fmt.Errorf("story-messages: %s is not a story", id)
	}
	var since time.Time
	if s := strings.TrimSpace(req.Since); s != "" {
		t, perr := time.Parse(time.RFC3339, s)
		if perr != nil {
			return nil, fmt.Errorf("story-messages: --since must be RFC3339: %w", perr)
		}
		since = t
	}
	to := strings.TrimSpace(req.To)
	state := strings.TrimSpace(req.State)
	if state != "" && !board.State(state).Valid() {
		return nil, fmt.Errorf("story-messages: --state %q must be waiting, delivered, answered or undeliverable", state)
	}
	msgs, err := listAgentMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]AgentMessage, 0, len(msgs))
	for _, m := range msgs {
		if !since.IsZero() && m.CreatedAt.Before(since) {
			continue
		}
		if (state != "" && m.State != state) || (req.Overdue && !m.Overdue) {
			continue
		}
		// Cc widens READ access only, and only when set: an empty Cc can never
		// match a non-empty filter, so a row without one is filtered exactly as
		// it was before the field existed (sty_8e0b29a0).
		if to != "" && m.To != to && m.To != "*" && m.Cc != to && m.Cc != "*" {
			continue
		}
		out = append(out, m)
	}
	return json.Marshal(out)
}

func listAgentMessages(ctx context.Context, storyID string) ([]AgentMessage, error) {
	ls, err := requireLedger()
	if err != nil {
		return nil, err
	}
	entries, err := ls.ListByStory(ctx, storyID, ledger.KindAgentMessage)
	if err != nil {
		return nil, err
	}
	marks, err := ls.ListByStory(ctx, storyID, ledger.KindMessageMark)
	if err != nil {
		return nil, err
	}
	return foldAgentMessages(entries, marks, time.Now()), nil
}

// foldAgentMessages is rowToAgentMessage over every row with the state the
// message_mark rows fold to and the overdue flag at now, oldest first. The fold
// is board.Fold's; this only carries its answer onto the stored shape.
func foldAgentMessages(entries, marks []ledger.Entry, now time.Time) []AgentMessage {
	folded := make(map[string]board.Message, len(entries))
	for _, f := range board.Fold(entries, marks) {
		folded[f.ID] = f
	}
	out := make([]AgentMessage, 0, len(entries))
	for _, e := range entries {
		m := rowToAgentMessage(e)
		if f, ok := folded[m.ID]; ok {
			m.State, m.AnswerID, m.UpdatedAt = string(f.State), f.AnswerID, f.UpdatedAt
			m.Overdue = board.Overdue(f, now)
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func rowToAgentMessage(e ledger.Entry) AgentMessage {
	m := AgentMessage{CreatedAt: e.CreatedAt, Body: e.Body}
	if len(e.Payload) > 0 {
		_ = json.Unmarshal(e.Payload, &m)
		m.CreatedAt = e.CreatedAt
	}
	m.ID = board.ID(e)
	return m
}

// MessagesSince returns engagement-windowed agent messages whose `to` is in
// addresses or is "*". Nil on any read error, or when the story is engaged but
// has no baseline — never fails a transition. Oldest first. The engine owns
// budget truncation.
func MessagesSince(ctx context.Context, itemID string, addresses []string) []AgentMessage {
	want := map[string]bool{"*": true}
	for _, a := range addresses {
		if a = strings.TrimSpace(a); a != "" {
			want[a] = true
		}
	}
	msgs := EngagementMessages(ctx, itemID)
	out := make([]AgentMessage, 0, len(msgs))
	for _, m := range msgs {
		// want never holds "" (addresses are trimmed and empties skipped), so a
		// row with no Cc is admitted on To alone — byte-identical to before the
		// field existed (sty_8e0b29a0).
		if !want[m.To] && !want[m.Cc] {
			continue
		}
		out = append(out, m)
	}
	return out
}

// UnansweredDirected returns the messages addressed to performer by name that
// it has not answered, oldest first (msgs is oldest first). A later message
// FROM performer answers everything before it, whoever it was addressed to —
// msgs must be the unfiltered window (EngagementMessages), not one filtered to
// the performer's inbox. Broadcasts do not count, and neither do the rework
// relay's own transcript rows (cc="*"): those are conversation, not a
// directive. The rework relay reads it to decide who speaks first, and the
// same-status re-dispatch reads it to decide whether a one-shot performer is
// owed a run (reDispatchOwed).
func UnansweredDirected(msgs []AgentMessage, performer string) []AgentMessage {
	var out []AgentMessage
	for _, m := range msgs {
		switch {
		case m.From == performer:
			out = nil
		case m.To == performer && m.Cc != "*":
			out = append(out, m)
		}
	}
	return out
}

// EngagementMessages returns every engagement-windowed agent message whatever
// its addressee, oldest first; nil on the same conditions as MessagesSince. It
// is for a reader that must see a role's own outbound messages, which
// MessagesSince filters out by address.
func EngagementMessages(ctx context.Context, itemID string) []AgentMessage {
	if strings.TrimSpace(itemID) == "" {
		return nil
	}
	windowSHA, windowAt, ok := currentEngagementWindow(ctx, itemID)
	if !ok {
		return nil
	}
	msgs, err := listAgentMessages(ctx, itemID)
	if err != nil {
		return nil
	}
	out := make([]AgentMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.CreatedAt.Before(windowAt) {
			continue
		}
		if m.EngagementSHA != "" && windowSHA != "" && m.EngagementSHA != windowSHA {
			continue
		}
		out = append(out, m)
	}
	return out
}

// currentEngagementWindow is the SHA and time MessagesSince windows on, and
// the SHA story-message stamps. Resume re-anchor wins over the first
// baseline so write and read cannot diverge (sty_2db624d0); a re-anchor taken
// before the first baseline is no anchor (sty_12ce4271). The pre-engagement
// choice is made here too, so the stamp and the read share one decision.
func currentEngagementWindow(ctx context.Context, storyID string) (sha string, at time.Time, ok bool) {
	if at, pre := preEngagementWindow(ctx, storyID); pre {
		return "", at, true
	}
	p, _, baseAt, err := firstEngagementBaseline(ctx, storyID)
	if err != nil {
		return "", time.Time{}, false
	}
	sha, at = p.HeadSHA, baseAt
	if rsha, rat, rok := latestResumeReanchor(ctx, storyID); rok {
		return rsha, rat, true
	}
	return sha, at, true
}

// preEngagementWindow reports the window start for a story sitting at the
// START state of its derived route with no engagement under way: it has never
// been engaged (no baseline), or it returned to the start state after its
// newest baseline or resume re-anchor (cancel or park, then re-entry). A
// performer that proposes from that state (sty_5262592e) is then reachable,
// and messages from an earlier engagement stay outside the window because at
// is the latest entry into the start state. Time is the only filter in this
// window; no SHA is stamped. An engaged story is never at the start state, so
// it falls through to the SHA-stamped window unchanged.
func preEngagementWindow(ctx context.Context, storyID string) (at time.Time, ok bool) {
	store, err := requireWorkItem()
	if err != nil {
		return time.Time{}, false
	}
	item, err := store.Get(ctx, storyID)
	if err != nil {
		return time.Time{}, false
	}
	spec, _, _, sok := governingSpec(ctx, item)
	if !sok || spec.Start() == "" || item.Status != spec.Start() {
		return time.Time{}, false
	}
	entered, hasEntry := latestEntryInto(ctx, storyID, item.Status)
	var anchor time.Time
	if _, _, baseAt, berr := firstEngagementBaseline(ctx, storyID); berr == nil {
		anchor = baseAt
	}
	if _, rat, rok := latestResumeReanchorExcept(ctx, storyID, item.Status); rok && rat.After(anchor) {
		anchor = rat
	}
	if !anchor.IsZero() && !(hasEntry && entered.After(anchor)) {
		return time.Time{}, false
	}
	if hasEntry {
		return entered, true
	}
	return item.CreatedAt, true
}

// latestEntryInto is the time of the newest status_transition into state.
func latestEntryInto(ctx context.Context, storyID, state string) (time.Time, bool) {
	entries := parkOriginEntries(ctx, storyID)
	for i := len(entries) - 1; i >= 0; i-- {
		if TransitionTo(entries[i]) == state {
			return entries[i].CreatedAt, true
		}
	}
	return time.Time{}, false
}
