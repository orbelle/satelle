package hosted

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func remoteTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	store := &memStore{cred: Credential{ServerURL: ts.URL, AccessToken: "t", RefreshToken: "r"}, set: true}
	return NewClient(ts.URL, store, ts.Client())
}

func TestFireRemoteMapsTheServersAnswers(t *testing.T) {
	var gotBody map[string]string
	var gotPath, gotMethod, gotAuth string
	status, reply := http.StatusOK, `{}`
	c := remoteTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))

	res, err := c.FireRemote(context.Background(), "acme/ops", "sty_1", "pr-approve-request")
	if err != nil || res.Pending {
		t.Fatalf("200: res=%+v err=%v, want requested and not pending", res, err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/remotes/acme%2Fops/fire" && gotPath != "/api/v1/remotes/acme/ops/fire" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	if gotBody["story_id"] != "sty_1" || gotBody["trigger"] != "pr-approve-request" || gotAuth != "Bearer t" {
		t.Errorf("body = %v auth = %q", gotBody, gotAuth)
	}

	status, reply = http.StatusAccepted, `{"outcome":"pending","position":2}`
	res, err = c.FireRemote(context.Background(), "r", "sty_1", "pr-approve-request")
	if err != nil || !res.Pending || res.Position != 2 {
		t.Fatalf("202: res=%+v err=%v, want a held wake at position 2", res, err)
	}

	status, reply = http.StatusConflict, `{"error":"limit_spend","reason":"cap reached"}`
	_, err = c.FireRemote(context.Background(), "r", "sty_1", "pr-approve-request")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != http.StatusConflict || ae.Code != "limit_spend" {
		t.Fatalf("409: err = %v, want an APIError carrying limit_spend", err)
	}

	status, reply = http.StatusUnauthorized, ``
	if _, err = c.FireRemote(context.Background(), "r", "sty_1", "pr-approve-request"); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("401: err = %v, want ErrLoginRequired", err)
	}
}

// sty_0b062fb1: the 200 body is decoded. A recorded wake names its trigger id; a
// repeat answer (the story already has a live run, nothing recorded) carries that
// run's standing; a body that names neither is still a started wake.
func TestFireRemoteDecodesTheWakeAnswer(t *testing.T) {
	status, reply := http.StatusOK, `{"id":"trg_y","outcome":"started"}`
	c := remoteTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	res, err := c.FireRemote(context.Background(), "r", "sty_1", "t")
	if err != nil || res.TriggerID != "trg_y" || res.Outcome != "started" || res.Repeat() || res.Pending {
		t.Fatalf("started: res=%+v err=%v, want trigger trg_y, not a repeat", res, err)
	}

	reply = `{"outcome":"started","detail":"this story already has a run on this remote; no second run was added","standing":{"state":"concluded","node_id":"nod_x","outcome":"reject"}}`
	res, err = c.FireRemote(context.Background(), "r", "sty_1", "t")
	if err != nil || !res.Repeat() || res.Standing.NodeID != "nod_x" || res.Standing.State != RunConcluded || res.TriggerID != "" {
		t.Fatalf("repeat: res=%+v err=%v, want a repeat carrying nod_x's standing", res, err)
	}

	status, reply = http.StatusAccepted, `{"id":"trg_h","outcome":"pending","position":2,"pending":3}`
	res, err = c.FireRemote(context.Background(), "r", "sty_1", "t")
	if err != nil || !res.Pending || res.Position != 2 || res.TriggerID != "trg_h" || res.Repeat() {
		t.Fatalf("held: res=%+v err=%v, want held wake trg_h at position 2", res, err)
	}

	status, reply = http.StatusOK, `not json`
	if res, err = c.FireRemote(context.Background(), "r", "sty_1", "t"); err != nil || res.Repeat() || res.Pending {
		t.Fatalf("undecodable: res=%+v err=%v, want a started wake", res, err)
	}
}

// satelle-server starts a runner-backed remote (no wake_url) only through a
// declared request trigger (sty_1f6e6667). This fake refuses a fire whose body
// names no trigger, or an undeclared one, the way the server does, and accepts
// one that names a declared trigger.
func TestFireRemoteSendsTheDeclaredRequestTrigger(t *testing.T) {
	declared := map[string]bool{"pr-approve-request": true}
	var got []map[string]string
	c := remoteTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		got = append(got, body)
		if !declared[body["trigger"]] {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"name a declared request trigger to start this remote"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))

	res, err := c.FireRemote(context.Background(), "rmt_c9ecdce8d1ba", "sty_1", "pr-approve-request")
	if err != nil || res.Pending {
		t.Fatalf("declared trigger: res=%+v err=%v, want the wake accepted", res, err)
	}
	if len(got) != 1 || got[0]["story_id"] != "sty_1" || got[0]["trigger"] != "pr-approve-request" || len(got[0]) != 2 {
		t.Fatalf("body = %v, want exactly {story_id: sty_1, trigger: pr-approve-request}", got)
	}

	for _, tc := range []struct{ name, trigger string }{{"absent", ""}, {"undeclared", "nightly"}} {
		_, err := c.FireRemote(context.Background(), "rmt_c9ecdce8d1ba", "sty_1", tc.trigger)
		var ae *APIError
		if !errors.As(err, &ae) || ae.Status != http.StatusConflict || !strings.Contains(err.Error(), "name a declared request trigger") {
			t.Errorf("%s trigger: err = %v, want the server's 409 refusal as an APIError", tc.name, err)
		}
	}
	if _, has := got[1]["trigger"]; has {
		t.Errorf("an empty trigger must be left out of the body: %v", got[1])
	}
}

func TestTeamRunStatusReadsTheStanding(t *testing.T) {
	var gotPath, gotQuery, gotMethod, gotAuth string
	status, reply := http.StatusOK, `{"state":"concluded","node_id":"nod_1","step":"in_progress","end_status":"ended","outcome":"accept"}`
	c := remoteTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotMethod, gotAuth = r.URL.Path, r.URL.RawQuery, r.Method, r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	st, err := c.TeamRunStatus(context.Background(), "r1", "sty_1")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v1/remotes/r1/stories/sty_1/run" || gotQuery != "" || gotAuth != "Bearer t" {
		t.Errorf("request = %s %s ?%s auth %q", gotMethod, gotPath, gotQuery, gotAuth)
	}
	if st.State != RunConcluded || st.Outcome != "accept" || st.NodeID != "nod_1" || st.EndStatus != "ended" {
		t.Errorf("standing = %+v", st)
	}

	status, reply = http.StatusOK, `{"state":"queued","position":2}`
	if st, err = c.TeamRunStatus(context.Background(), "r1", "sty_1"); err != nil || st.State != RunQueued || st.Position != 2 {
		t.Errorf("queued: standing = %+v err = %v", st, err)
	}

	// 404: no run, or the remote is unknown or invisible — state none, not an error.
	status, reply = http.StatusNotFound, `{"error":"not_found"}`
	if st, err = c.TeamRunStatus(context.Background(), "r1", "sty_1"); err != nil || st.State != RunNone {
		t.Errorf("404: standing = %+v err = %v, want state none", st, err)
	}

	// Concluded with no outcome: an unreported end, read from state, carrying its
	// end status and reason.
	status, reply = http.StatusOK, `{"state":"concluded","node_id":"nod_1","end_status":"killed","reason":"operator"}`
	if st, err = c.TeamRunStatus(context.Background(), "r1", "sty_1"); err != nil ||
		st.State != RunConcluded || st.Outcome != "" || st.EndStatus != "killed" || st.Reason != "operator" {
		t.Errorf("concluded without outcome: standing = %+v err = %v", st, err)
	}

	status, reply = http.StatusUnauthorized, ``
	if _, err = c.TeamRunStatus(context.Background(), "r1", "sty_1"); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("401: err = %v, want ErrLoginRequired", err)
	}
}

func TestStartPerformerNode(t *testing.T) {
	var gotBody map[string]string
	var gotMethod string
	status, reply := http.StatusCreated, `{"node_id":"nod_7","relay_url":"wss://relay.example/relay"}`
	c := remoteTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	id, relay, err := c.StartPerformerNode(context.Background(), "r1", "sty_1", "coder", "")
	if err != nil || id != "nod_7" || relay != "wss://relay.example/relay" {
		t.Fatalf("got (%q, %q, %v)", id, relay, err)
	}
	if gotMethod != http.MethodPost || gotBody["mode"] != "performer" || gotBody["story_id"] != "sty_1" || gotBody["binding"] != "coder" {
		t.Errorf("request = %s %v", gotMethod, gotBody)
	}
	if _, has := gotBody["path"]; has {
		t.Errorf("an empty path must be omitted from the request: %v", gotBody)
	}

	gotBody = nil
	if _, _, err = c.StartPerformerNode(context.Background(), "r1", "sty_1", "coder", "remotes/step-pi"); err != nil {
		t.Fatalf("with a path: %v", err)
	}
	if gotBody["mode"] != "performer" || gotBody["story_id"] != "sty_1" || gotBody["binding"] != "coder" || gotBody["path"] != "remotes/step-pi" {
		t.Errorf("request with a path = %v", gotBody)
	}

	status, reply = http.StatusNotFound, `{"error":"not_found"}`
	_, _, err = c.StartPerformerNode(context.Background(), "r1", "sty_1", "coder", "")
	if err == nil || !strings.Contains(err.Error(), "sty_e272c46f") {
		t.Fatalf("404: err = %v, want it to name satelle-server#sty_e272c46f", err)
	}

	status, reply = http.StatusConflict, `{"error":"limit_spend","reason":"cap"}`
	_, _, err = c.StartPerformerNode(context.Background(), "r1", "sty_1", "coder", "")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != "limit_spend" {
		t.Fatalf("409: err = %v, want the server's code", err)
	}

	status, reply = http.StatusCreated, `{}`
	if _, _, err = c.StartPerformerNode(context.Background(), "r1", "sty_1", "coder", ""); err == nil {
		t.Fatal("a 201 naming no node must be an error")
	}
}

func TestGetRemoteNodeKey(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	reply := `{"public_key":"` + base64.StdEncoding.EncodeToString(key) + `"}`
	var gotPath string
	c := remoteTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(reply))
	}))
	got, err := c.GetRemoteNode(context.Background(), "nod_7")
	if err != nil || string(got) != string(key) || gotPath != "/api/v1/remote-nodes/nod_7" {
		t.Fatalf("got (%q, %v) at %s", got, err, gotPath)
	}
	reply = `{"state":"starting"}`
	if got, err = c.GetRemoteNode(context.Background(), "nod_7"); err != nil || got != nil {
		t.Fatalf("an absent public_key must be (nil, nil), got (%q, %v)", got, err)
	}
	reply = `{"public_key":"!!not base64"}`
	if _, err = c.GetRemoteNode(context.Background(), "nod_7"); err == nil {
		t.Fatal("a malformed key must be an error")
	}
}
