package api

import "encoding/json"

// Hand-written mirrors of checkout_sync.proto (package satelle.sync.v1).
// Update these structs when the proto changes. They are independently owned
// reference types — same posture as types.go — and must not import internal/.
// satelle-server copies the proto; this repo does not implement the server.

// ApplyRequest is the Sync.Apply input: a project-scoped batch of work items
// and ledger entries. Project is the hosted project id or slug. Record bytes
// on each item/entry are UTF-8 JSON (the opaque CLI payload).
type ApplyRequest struct {
	Project string        `json:"project"`
	Items   []WorkItem    `json:"items"`
	Ledger  []LedgerEntry `json:"ledger"`
	Outputs []RunOutput   `json:"outputs,omitempty"`
}

// RunOutput is one run-output document of an execution in the batch or the
// project. A body over 1 MiB, or an unknown execution, refuses the batch.
type RunOutput struct {
	Execution string `json:"execution"`
	Name      string `json:"name"`
	Body      []byte `json:"body"`
}

// ApplyResponse is the Sync.Apply result: counts of upserted rows. JSON keys
// match WorkstateIngestResult so the gRPC and REST write surfaces cannot drift.
type ApplyResponse struct {
	Items   int `json:"items"`
	Ledger  int `json:"ledger"`
	Outputs int `json:"outputs"`
}

// SnapshotRequest is the Sync.Snapshot input. Kind optionally filters items
// (story|execution). The paging fields are documented on SnapshotRequest in
// checkout_sync.proto.
type SnapshotRequest struct {
	Project    string `json:"project"`
	Kind       string `json:"kind,omitempty"`
	PageSize   int32  `json:"page_size,omitempty"`
	PageToken  string `json:"page_token,omitempty"`
	SinceToken string `json:"since_token,omitempty"`
	// StoryIDs optionally scopes the pass: at most 1000, none empty.
	StoryIDs []string `json:"story_ids,omitempty"`
}

// SnapshotResponse is the Sync.Snapshot result: current items and ledger the
// caller may read. The token fields are documented on SnapshotResponse in
// checkout_sync.proto.
type SnapshotResponse struct {
	Items         []WorkItem    `json:"items"`
	Ledger        []LedgerEntry `json:"ledger"`
	NextPageToken string        `json:"next_page_token,omitempty"`
	SyncToken     string        `json:"sync_token,omitempty"`
}

// WorkItem is one work-state row on the checkout-sync wire. JSON keys match
// WorkstateItem. Record is the opaque CLI payload (UTF-8 JSON).
type WorkItem struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Type   string          `json:"type"`
	Status string          `json:"status"`
	Title  string          `json:"title"`
	Origin string          `json:"origin"`
	Record json.RawMessage `json:"record"`
}

// LedgerEntry is one ledger row on the checkout-sync wire. JSON keys match
// WorkstateLedgerRow. Record is the opaque CLI payload (UTF-8 JSON).
type LedgerEntry struct {
	ID      string          `json:"id"`
	StoryID string          `json:"story_id"`
	Kind    string          `json:"kind"`
	Type    string          `json:"type"`
	Origin  string          `json:"origin"`
	Record  json.RawMessage `json:"record"`
}

// RefreshRequest is the Sync.Refresh input: the stored refresh token.
// The RPC must not require a valid bearer; this token is the credential.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// RefreshResponse is the Sync.Refresh result: a rotated access/refresh pair.
// ExpiresIn is seconds until the access token expires (0 if the server omits it).
type RefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// SyncAuthMetadata returns the gRPC metadata map Apply and Snapshot carry.
// The key is lowercase authorization (gRPC metadata keys are case-insensitive
// but canonically lowercase). Value is "Bearer <accessToken>". Token issue
// stays on HTTP /oauth/*; refresh on the gRPC path is Sync.Refresh. There is
// no gRPC login.
func SyncAuthMetadata(accessToken string) map[string]string {
	return map[string]string{"authorization": "Bearer " + accessToken}
}
