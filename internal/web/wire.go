package web

import (
	"encoding/json"
	"sort"
	"time"
)

// Item is the item row a client pushes into a mirror partition: the
// mirror's wire shape, kept as it is (satelled's maintained interface).
type Item struct {
	ID                 string    `json:"id"`
	Kind               string    `json:"kind"`
	Title              string    `json:"title"`
	Body               string    `json:"body,omitempty"`
	Status             string    `json:"status"`
	Priority           string    `json:"priority,omitempty"`
	Category           string    `json:"category,omitempty"`
	ParentID           string    `json:"parent_id,omitempty"`
	AcceptanceCriteria string    `json:"acceptance_criteria,omitempty"`
	Tags               []string  `json:"tags"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// Entry is the ledger_event row a client pushes into a partition.
type Entry struct {
	ID        string          `json:"id"`
	StoryID   string          `json:"story_id,omitempty"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor,omitempty"`
	Body      string          `json:"body,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// sortEntries orders es oldest-first: by CreatedAt, then by ID.
func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].CreatedAt.Equal(es[j].CreatedAt) {
			return es[i].ID < es[j].ID
		}
		return es[i].CreatedAt.Before(es[j].CreatedAt)
	})
}

// docEntry is a doc row a client pushes into a partition.
type docEntry struct {
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	Headline   string    `json:"headline,omitempty"`
	Body       string    `json:"body"`
	ModTime    time.Time `json:"mod_time"`
	Provenance string    `json:"provenance"`
	Source     string    `json:"source"`
}

// stageVM is one entry of a row's progress cell. The pages that fill it are
// rebuilt on the kernel in B4; today rows carry none.
type stageVM struct {
	Name      string
	State     string
	Gates     *gateCountsVM
	Parked    int
	ParkName  string
	Title     string
	Performer string
}

// gateCountsVM is one step's exit gate counts.
type gateCountsVM struct {
	Gates                   int
	Rejected                int
	Passed, Failed, Running int
	Live                    bool
	NotRun                  bool
	Tone                    string
	Edge                    string
}

// seatGateVM is a seat's running gate round.
type seatGateVM struct {
	Total, Passed, Failed int
}

// workflowRowVM is one Workflow panel row.
type workflowRowVM struct {
	Name       string
	ExpandName string
	Headline   string
	Scope      string
	AppliesTo  []string
	Updated    time.Time
	Provenance string
	Source     string
}
