// Package workitem is satelle's unit-of-work dynamic primitive — stories and
// tasks. Both share one shape and one backing table, distinguished by Kind,
// mirroring satellites' unification of story/task behind a single documents
// row (type='story'|'task'). A story is a goal; a task is a project-level
// to-do; they differ only in id prefix and ledger-event kind.
//
// Backed by sqlite (modernc.org/sqlite, no cgo); SQL is libSQL-compatible.
package workitem

import (
	"fmt"
	"strings"
	"time"
	"uuid"
)

// Kind discriminates stories from tasks. It selects the id prefix and the
// table partition (a `kind` column), so one Store serves both.
type Kind string

const (
	KindStory Kind = "story"
	KindTask  Kind = "task"
	// KindExecution is an isolated RUN of a task (sty_ef08ce2a). A task is a
	// stable authored header/work-definition; each execution is a separate item
	// parented to its task, carrying the run lifecycle backlog→in_progress→done.
	// "Re-running" a task means creating a NEW execution — a done execution is
	// terminal and never moved backward (satelle-done-is-last).
	KindExecution Kind = "execution"
)

// Status values. New items default to StatusBacklog — every satelle workflow
// starts at backlog (see satelle-workflow-review). The set is open-ended (a
// caller may set any status string); these are the names satelle itself uses.
const (
	StatusBacklog    = "backlog"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
	// StatusCancelled is the lifecycle name of a cancelled item, the counterpart
	// of StatusDone for readers that must tell a finished item from a live one.
	// It is only the lifecycle fallback for when no route resolves; a resolved
	// route's own cancel state is authored in its declaration of done.
	StatusCancelled = "cancelled"
	// Park/resume state names are authored in the route's declaration of done
	// (`park:` / `cancel:`) — never hardcoded here (config-over-code).
)

// Item is one story or task row.
type Item struct {
	ID                 string    `json:"id"`
	Kind               Kind      `json:"kind"`
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
	// Archived is record DISPOSITION, orthogonal to workflow Status (sty_cd209b8a):
	// an archived item is excluded from the default List but still returned by Get
	// with its full lineage. It is set by the Archive verb, never by an authored
	// file — so it is not marshalled into the item's markdown.
	Archived bool `json:"archived,omitempty"`
	// ParkOrigin is the status a story held when it entered a park node
	// (sty_f75286dc). Authoritative for resume-to-origin enforcement — not
	// derived from the ledger. Empty when not parked (or after resume).
	ParkOrigin string `json:"park_origin,omitempty"`
	// Assignee is the hosted principal id that may engage this item
	// (sty_8ccaa906). Empty means unassigned. Machine-set, not authored
	// markdown — omitempty keeps it off file views.
	Assignee string `json:"assignee,omitempty"`
	// Inputs are the typed inputs a TASK declares (inputs_decl: in its file);
	// InputValues are the values an EXECUTION was created with (inputs: in its
	// file). satelle declares, validates and passes them on, and never interprets
	// them (see inputs.go). Both are omitempty, so an item without inputs is
	// byte-identical to one from before they existed.
	Inputs      []InputDecl    `json:"inputs_decl,omitempty"`
	InputValues map[string]any `json:"inputs,omitempty"`
	// Triggers are the local triggers a TASK declares (triggers: in its file),
	// each running one execution of it when it fires (see triggers.go). Omitempty,
	// so a task without them is unchanged.
	Triggers []TriggerDecl `json:"triggers,omitempty"`
}

// idPrefix returns the id prefix for a kind: sty_ for stories, tsk_ for tasks,
// exe_ for executions.
func (k Kind) idPrefix() string {
	switch k {
	case KindTask:
		return "tsk_"
	case KindExecution:
		return "exe_"
	default:
		return "sty_"
	}
}

// Holdable reports whether a kind of record may be held back from a push until
// its code is on the git remote ([sync] hold_unpushed). Executions are
// append-only: two machines' executions are two records, never one record two
// machines contend for, so there is nothing to hold (sty_52b93691). (Hosted
// holds are gone, sty_40853d02.)
func (k Kind) Holdable() bool { return k != KindExecution }

// Leasable reports whether a kind of record takes an engagement lease when it
// moves into (or is re-engaged at) a performing state. Stories, tasks and task
// executions all do: the lease is how the edit and commit gates see the item as
// engaged. Only a story occupies the story seat; that is decided at acquire, not
// here (sty_7f93ae45).
func (k Kind) Leasable() bool {
	return k == KindStory || k == KindTask || k == KindExecution
}

// Holdable reports whether the record with this id may be held back from a push.
// It is the id form of Kind.Holdable, for the callers that have an id and no
// record.
func Holdable(id string) bool {
	return !strings.HasPrefix(id, KindExecution.idPrefix())
}

// newID returns a fresh id for the kind in the <prefix><8hex> form.
//
// NewV4 explicitly, not New (sty_5515036d): the id keeps only the first 8 hex
// characters, so those bits must be random. A v7 UUID leads with a millisecond
// timestamp, which would make ids minted in the same instant collide; New is
// documented as "at this time" equivalent to v4, and this truncation cannot rely
// on that staying true.
func (k Kind) newID() string {
	return fmt.Sprintf("%s%s", k.idPrefix(), uuid.NewV4().String()[:8])
}

// valid reports whether k is a known kind.
func (k Kind) valid() bool {
	return k == KindStory || k == KindTask || k == KindExecution
}
