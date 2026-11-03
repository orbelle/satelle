package hosted

// The starter's calls on a team remote (sty_7028d996): waking it for a story and
// reading where that story's run stands. Both go on the CLI token. A node's own
// calls are the node's (remotenode.NodeClient), not these.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// remoteRoute is the base of one published remote.
func remoteRoute(remoteID string) string { return "/api/v1/remotes/" + url.PathEscape(remoteID) }

// FireResult is the server's answer to a wake that it accepted: the trigger row
// satelle-server sends on 200 and 202 alike (internal/remotes Trigger).
type FireResult struct {
	// Pending is true when the wake was held (202) because the remote is at its
	// concurrency limit; Position is its place in the queue.
	Pending  bool `json:"-"`
	Position int  `json:"position,omitempty"`
	// TriggerID is the recorded wake (trg_…); Outcome and Detail are the server's
	// word on it (started, pending).
	TriggerID string `json:"id,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	Detail    string `json:"detail,omitempty"`
	// Standing is set only on a repeat wake: the story already has a live or
	// pending run, so the server recorded nothing, started no run, and answered
	// with that earlier run's standing (satelle-server internal/remotes/wake.go
	// fireWake → repeatAnswer).
	Standing *RunStanding `json:"standing,omitempty"`
}

// Repeat reports whether the wake started no run because the story already had
// one: the answer carries that earlier run's standing.
func (r FireResult) Repeat() bool { return r.Standing != nil }

// FireRemote wakes the remote remoteID for storyID (POST /api/v1/remotes/{id}/fire)
// through its declared request trigger: the body is {"story_id", "trigger"}, and
// satelle-server starts a runner-backed remote (one with no wake_url) only when
// trigger names a request trigger it declares (sty_1f6e6667). An empty trigger is
// left out of the body. 200 (a node started, or a repeat answer naming the story's
// existing run) and 202 (the wake is held) both mean it was requested; any other answer, notably 409 with an outcome such as
// limit_spend, launch_failed or an undeclared trigger, is an *APIError carrying
// the server's code and reason.
func (c *Client) FireRemote(ctx context.Context, remoteID, storyID, trigger string) (FireResult, error) {
	fire := map[string]string{"story_id": storyID}
	if trigger != "" {
		fire["trigger"] = trigger
	}
	payload, err := json.Marshal(fire)
	if err != nil {
		return FireResult{}, err
	}
	resp, err := c.doAuthed(ctx, http.MethodPost, remoteRoute(remoteID)+"/fire", payload, "application/json")
	if err != nil {
		return FireResult{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxCallBody))
	switch resp.StatusCode {
	case http.StatusOK:
		var res FireResult
		_ = json.Unmarshal(body, &res) // a body that does not decode is still a started wake
		return res, nil
	case http.StatusAccepted:
		var res FireResult
		_ = json.Unmarshal(body, &res) // a body without the position is still a held wake
		res.Pending = true
		return res, nil
	case http.StatusUnauthorized:
		return FireResult{}, ErrLoginRequired
	default:
		return FireResult{}, fmt.Errorf("hosted: fire remote %s: %w", remoteID, newAPIError(resp.StatusCode, body))
	}
}

// The standing a story has with a remote, as the server reports it. Queued and
// running are in progress; concluded is over, with the end status and, for a node
// that reported one, its outcome.
const (
	RunNone      = "none"
	RunQueued    = "queued"
	RunRunning   = "running"
	RunConcluded = "concluded"
)

// RunStanding is where a story stands with a remote's runs.
type RunStanding struct {
	State     string          `json:"state"`
	Position  int             `json:"position,omitempty"` // queued only; 1 is next
	NodeID    string          `json:"node_id,omitempty"`
	Step      string          `json:"step,omitempty"`
	EndStatus string          `json:"end_status,omitempty"` // ended, failed or killed
	Outcome   string          `json:"outcome,omitempty"`    // accept, reject, performed or no-op; concluded only
	Findings  json.RawMessage `json:"findings,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}

// TeamRunStatus reads where storyID stands with remoteID (GET
// /api/v1/remotes/{id}/stories/{story_id}/run, on the user's bearer): the run
// state the server holds for the story (satelle-server sty_b15b0fb2). A 404 is
// state none: the story has no run, or the remote is unknown or not visible. A
// concluded run is read from State alone; one with no Outcome is an unreported
// end, described by EndStatus and Reason. This is the one reader of a team run.
func (c *Client) TeamRunStatus(ctx context.Context, remoteID, storyID string) (RunStanding, error) {
	path := remoteRoute(remoteID) + "/stories/" + url.PathEscape(storyID) + "/run"
	resp, err := c.doAuthed(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return RunStanding{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxCallBody))
	switch resp.StatusCode {
	case http.StatusOK:
		var out RunStanding
		if err := json.Unmarshal(body, &out); err != nil {
			return RunStanding{}, fmt.Errorf("hosted: decode run of %s on %s: %w", storyID, remoteID, err)
		}
		return out, nil
	case http.StatusNotFound:
		return RunStanding{State: RunNone}, nil
	case http.StatusUnauthorized:
		return RunStanding{}, ErrLoginRequired
	default:
		return RunStanding{}, fmt.Errorf("hosted: read run of %s on %s: %w", storyID, remoteID, newAPIError(resp.StatusCode, body))
	}
}
