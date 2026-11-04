package hosted

// Work-state wire shapes carried by gRPC Sync (Apply / Snapshot). The server
// stamps origin=cli-sync itself — the client never supplies it.

import "encoding/json"

// WorkstateIngest is a local→server batch of stories/executions (items),
// ledger entries and the run-output documents of executions. Apply transports
// this over Sync; the JSON keys match the published api.WorkstateIngest shape.
type WorkstateIngest struct {
	Items   []json.RawMessage `json:"items"`
	Ledger  []json.RawMessage `json:"ledger"`
	Outputs []WorkstateOutput `json:"outputs,omitempty"`
}

// WorkstateOutput is one run-output document of an execution: its file name and
// text. The execution is in the batch or already in the project.
type WorkstateOutput struct {
	Execution string `json:"execution"`
	Name      string `json:"name"`
	Body      string `json:"body"`
}

// MaxRunOutputBytes is the largest run-output body Sync/Apply accepts; a larger
// one refuses the whole batch.
const MaxRunOutputBytes = 1 << 20

// WorkstateIngestResult is the Apply response: counts of upserted rows.
type WorkstateIngestResult struct {
	Items   int `json:"items"`
	Ledger  int `json:"ledger"`
	Outputs int `json:"outputs"`
}

// WorkstateItem is one mirrored work-state row from Snapshot.
type WorkstateItem struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Type   string          `json:"type"`
	Status string          `json:"status"`
	Title  string          `json:"title"`
	Origin string          `json:"origin"`
	Record json.RawMessage `json:"record"`
}

// WorkstateLedgerRow is one mirrored ledger row from Snapshot.
type WorkstateLedgerRow struct {
	ID      string          `json:"id"`
	StoryID string          `json:"story_id"`
	Kind    string          `json:"kind"`
	Type    string          `json:"type"`
	Origin  string          `json:"origin"`
	Record  json.RawMessage `json:"record"`
}
