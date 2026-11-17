package mirror

import (
	"encoding/json"
	"time"

	"github.com/bobmcallan/satelle/internal/item"
)

type Item struct {
	ID                 string          `json:"id"`
	Kind               string          `json:"kind"`
	Title              string          `json:"title"`
	Body               string          `json:"body,omitempty"`
	Status             string          `json:"status"`
	Criteria           []string        `json:"criteria,omitempty"`
	AcceptanceCriteria string          `json:"acceptance_criteria,omitempty"`
	Tags               []string        `json:"tags"`
	ParentID           string          `json:"parent_id,omitempty"`
	Category           string          `json:"category,omitempty"`
	Workflow           string          `json:"workflow,omitempty"`
	Assignees          []string        `json:"assignees,omitempty"`
	Priority           string          `json:"priority,omitempty"`
	Inputs             *item.Selection `json:"inputs,omitempty"`
	Trigger            *item.Trigger   `json:"trigger,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type Event struct {
	ID        string          `json:"id"`
	Item      string          `json:"item"`
	Kind      string          `json:"kind"`
	At        time.Time       `json:"at"`
	Actor     string          `json:"actor,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Prev      string          `json:"prev,omitempty"`
	Hash      string          `json:"hash,omitempty"`
	StoryID   string          `json:"story_id,omitempty"`
	Body      string          `json:"body,omitempty"`
	CreatedAt time.Time       `json:"created_at,omitzero"`
}

func (e *Event) Normalize() {
	if e.Item == "" {
		e.Item = e.StoryID
	}
	if e.At.IsZero() {
		e.At = e.CreatedAt
	}
}

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
	Versions    int       `json:"versions,omitempty"`
	Type        string    `json:"type,omitempty"`
}

type Seat struct {
	ID        string    `json:"id"`
	Item      string    `json:"item"`
	Session   string    `json:"session,omitempty"`
	Location  string    `json:"location,omitempty"`
	Base      string    `json:"base,omitempty"`
	BoundAt   time.Time `json:"bound_at,omitzero"`
	Machine   string    `json:"machine,omitempty"`
	Host      string    `json:"host,omitempty"`
	User      string    `json:"user,omitempty"`
	StorySeat bool      `json:"story_seat,omitempty"`
	Stale     bool      `json:"stale,omitempty"`
}

const DocKindWorkflows = "workflows"

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

type Workflow struct {
	Name        string               `json:"name"`
	Panel       string               `json:"panel,omitempty"`
	States      []WorkflowState      `json:"states"`
	Gates       []WorkflowGate       `json:"gates,omitempty"`
	Transitions []WorkflowTransition `json:"transitions"`
}

type WorkflowState struct {
	Name           string `json:"name"`
	Start          bool   `json:"start,omitempty"`
	Terminal       bool   `json:"terminal,omitempty"`
	Park           bool   `json:"park,omitempty"`
	Resumes        bool   `json:"resumes,omitempty"`
	PerformerAgent string `json:"performer_agent,omitempty"`
	PerformerSkill string `json:"performer_skill,omitempty"`
}

type WorkflowGate struct {
	Name    string   `json:"name"`
	Skill   string   `json:"skill,omitempty"`
	Judges  []string `json:"judges,omitempty"`
	Combine string   `json:"combine,omitempty"`
	Paths   []string `json:"paths,omitempty"`
	Trigger string   `json:"trigger,omitempty"`
	Carry   bool     `json:"carry,omitempty"`
}

type WorkflowTransition struct {
	From   []string        `json:"from"`
	To     string          `json:"to"`
	Groups []WorkflowGroup `json:"groups,omitempty"`
}

type WorkflowGroup struct {
	Mode  string   `json:"mode"`
	Gates []string `json:"gates"`
}

type Settings struct {
	RepoRoot string       `json:"repo_root"`
	Rows     []SettingRow `json:"rows"`
}

type SettingRow struct {
	FieldID string `json:"field_id"`
	Label   string `json:"label"`
	Help    string `json:"help,omitempty"`
	Value   string `json:"value"`
	Section string `json:"section"`
}

type GateRun struct {
	Transition string `json:"transition"`
	Gate       string `json:"gate"`
	Binding    string `json:"binding,omitempty"`
	Result     string `json:"result"`
	Notes      string `json:"notes,omitempty"`
	TokensIn   int    `json:"tokens_in"`
	TokensOut  int    `json:"tokens_out"`
	DurationMS int64  `json:"duration_ms"`
	Attempt    string `json:"attempt,omitempty"`
}

type GateStart struct {
	Transition string `json:"transition"`
	Gate       string `json:"gate"`
	Attempt    string `json:"attempt"`
}

type PerformerRun struct {
	State   string `json:"state"`
	Binding string `json:"binding,omitempty"`
}

const (
	EventGateRun         = "gate.run"
	EventGateStart       = "gate.start"
	EventTransitioned    = item.KindTransitioned
	EventAttachmentAdded = item.KindAttachmentAdd
	EventUpdated         = item.KindUpdated
	EventPerformerRun    = item.KindPerformerRun
	GateReject           = "reject"
	GateError            = "error"
	TransitionSep        = "->"
)
