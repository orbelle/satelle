package mirror

import (
	"encoding/json"
	"time"

	"github.com/bobmcallan/satelle/internal/item"
)

// The row shapes the CLI pushes inside a Snapshot (satelled's maintained
// interface). Each is stored as the raw JSON it arrived as; these types are
// what both sides marshal and read. Unknown fields are carried and ignored, and
// the legacy aliases (story_id, created_at, type) of older pushers are read too.

// Item is one row of Snapshot.Stories, Tasks, Executions or Queues: a folded
// work item.
type Item struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Body     string   `json:"body,omitempty"`
	Status   string   `json:"status"`
	Criteria []string `json:"criteria,omitempty"`
	// AcceptanceCriteria is the legacy one-string criteria; read when Criteria
	// is empty.
	AcceptanceCriteria string          `json:"acceptance_criteria,omitempty"`
	Tags               []string        `json:"tags"`
	ParentID           string          `json:"parent_id,omitempty"`
	Category           string          `json:"category,omitempty"`
	Workflow           string          `json:"workflow,omitempty"`
	Assignees          []string        `json:"assignees,omitempty"` // the explicit assignees
	Priority           string          `json:"priority,omitempty"`  // legacy; carried, not shown
	Inputs             *item.Selection `json:"inputs,omitempty"`    // a task's inputs, a queue's filter
	Trigger            *item.Trigger   `json:"trigger,omitempty"`   // a queue's trigger
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// Event is one row of Snapshot.LedgerEvents: a generic ledger event, keyed by
// its id.
type Event struct {
	ID      string          `json:"id"`
	Item    string          `json:"item"`
	Kind    string          `json:"kind"`
	At      time.Time       `json:"at"`
	Actor   string          `json:"actor,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Prev    string          `json:"prev,omitempty"`
	Hash    string          `json:"hash,omitempty"`
	// Legacy aliases of older pushers.
	StoryID   string    `json:"story_id,omitempty"`
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Normalize fills Item and At from the legacy aliases.
func (e *Event) Normalize() {
	if e.Item == "" {
		e.Item = e.StoryID
	}
	if e.At.IsZero() {
		e.At = e.CreatedAt
	}
}

// Attachment is one row of Snapshot.StoryDocs: a document attached to an item.
// Its id is "<item>/<name>": a pusher sends one row per name, the newest
// version, with Versions counting them. Body is set for a text attachment
// within the pusher's bound; BodyOmitted says it was left out.
type Attachment struct {
	ID          string    `json:"id"`
	Item        string    `json:"item"`
	Name        string    `json:"name"`
	MediaType   string    `json:"media_type,omitempty"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256,omitempty"`
	At          time.Time `json:"at,omitzero"`
	Actor       string    `json:"actor,omitempty"`
	Body        string    `json:"body,omitempty"`
	BodyOmitted bool      `json:"body_omitted,omitempty"`
	Versions    int       `json:"versions,omitempty"` // how many versions of Name the item has; the row is the newest
	Type        string    `json:"type,omitempty"`     // legacy media type
}

// Seat is one row of Snapshot.Seats: a session bound to an item. Its id is
// "<item>/<session>".
type Seat struct {
	ID       string    `json:"id"`
	Item     string    `json:"item"`
	Session  string    `json:"session,omitempty"`
	Location string    `json:"location,omitempty"`
	Base     string    `json:"base,omitempty"`
	BoundAt  time.Time `json:"bound_at,omitzero"`
	// Machine and Host are the machine the seat was bound on (item.Seat);
	// empty on seats bound before seats recorded it.
	Machine string `json:"machine,omitempty"`
	Host    string `json:"host,omitempty"`
	// User is the satelle user who bound the seat (item.Seat); empty on
	// seats bound before seats recorded it.
	User string `json:"user,omitempty"`
	// Legacy seat rows named the item by id and flagged story seats.
	StorySeat bool `json:"story_seat,omitempty"`
	Stale     bool `json:"stale,omitempty"`
}

// DocKindWorkflows is the Snapshot.Docs kind that carries a workflow.
const DocKindWorkflows = "workflows"

// Doc is one row of Snapshot.Docs. A workflow arrives as kind
// DocKindWorkflows with its source in Body and its parsed form in Workflow, so
// the web draws routes without knowing any name.
type Doc struct {
	Kind     string    `json:"kind"`
	Name     string    `json:"name"`
	Path     string    `json:"path,omitempty"`
	Headline string    `json:"headline,omitempty"`
	Body     string    `json:"body"`
	ModTime  time.Time `json:"mod_time,omitzero"`
	Source   string    `json:"source,omitempty"`
	Workflow *Workflow `json:"workflow,omitempty"`
}

// Workflow is a workflow's states, gates and explicit transitions.
type Workflow struct {
	Name string `json:"name"`
	// Panel names the satelled tab that lists the workflow's items instead
	// of Stories; empty when it declares none.
	Panel       string               `json:"panel,omitempty"`
	States      []WorkflowState      `json:"states"`
	Gates       []WorkflowGate       `json:"gates,omitempty"`
	Transitions []WorkflowTransition `json:"transitions"`
}

// WorkflowState is one state; the flags are the schema's.
type WorkflowState struct {
	Name           string `json:"name"`
	Start          bool   `json:"start,omitempty"`
	Terminal       bool   `json:"terminal,omitempty"`
	Park           bool   `json:"park,omitempty"`
	Resumes        bool   `json:"resumes,omitempty"`
	PerformerAgent string `json:"performer_agent,omitempty"`
	PerformerSkill string `json:"performer_skill,omitempty"`
}

// WorkflowGate is one named gate.
type WorkflowGate struct {
	Name    string   `json:"name"`
	Skill   string   `json:"skill,omitempty"`
	Judges  []string `json:"judges,omitempty"`
	Combine string   `json:"combine,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Trigger string   `json:"trigger,omitempty"`
	Carry   bool     `json:"carry,omitempty"`
}

// WorkflowTransition is one explicit move and its gate groups, in order.
type WorkflowTransition struct {
	From   []string        `json:"from"`
	To     string          `json:"to"`
	Groups []WorkflowGroup `json:"groups,omitempty"`
}

// WorkflowGroup is a set of gates run in one mode.
type WorkflowGroup struct {
	Mode  string   `json:"mode"`
	Gates []string `json:"gates"`
}

// Settings is Snapshot.Settings: the repo's configuration as rows.
type Settings struct {
	RepoRoot string       `json:"repo_root"`
	Rows     []SettingRow `json:"rows"`
}

// SettingRow is one configuration key and its value.
type SettingRow struct {
	FieldID string `json:"field_id"`
	Label   string `json:"label"`
	Help    string `json:"help,omitempty"`
	Value   string `json:"value"`
	Section string `json:"section"`
}

// GateRun is the payload of a gate.run ledger event, as the web reads it.
type GateRun struct {
	Transition string `json:"transition"`
	Gate       string `json:"gate"`
	Binding    string `json:"binding,omitempty"`
	Result     string `json:"result"`
	Notes      string `json:"notes,omitempty"`
	TokensIn   int    `json:"tokens_in"`
	TokensOut  int    `json:"tokens_out"`
	DurationMS int64  `json:"duration_ms"`
	// Attempt is shared by every result of one presentation (gate.Result.Attempt):
	// the kernel's round key. Empty on events recorded before it was set.
	Attempt string `json:"attempt,omitempty"`
}

// PerformerRun is the part of a performer.run ledger event's payload the web
// reads (kernel.PerformerRun): the state performed and the binding that ran.
// The event is appended when the run ends.
type PerformerRun struct {
	State   string `json:"state"`
	Binding string `json:"binding,omitempty"`
}

// The kernel event kinds the web reads (gate.KindRun, item.KindTransitioned,
// item.KindAttachmentAdd, item.KindUpdated, kernel.KindPerformerRun; uipush's
// tests pin them equal). Every other kind is
// shown on the timeline as it is.
const (
	EventGateRun         = "gate.run"
	EventTransitioned    = "item.transitioned"
	EventAttachmentAdded = "attachment.added"
	EventUpdated         = "item.updated"
	EventPerformerRun    = "performer.run"
	// GateReject is the gate.run result that refuses a transition
	// (gate.Reject).
	GateReject = "reject"
	// GateError is the gate.run result of a judge that could not be asked: it
	// refuses a transition too, but is not a verdict (gate.Error).
	GateError = "error"
	// TransitionSep joins from and to in a gate.run transition key
	// (gate.TransitionKey).
	TransitionSep = "->"
)
