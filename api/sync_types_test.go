package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/api"
	"github.com/bobmcallan/satelle/internal/hosted"
)

func TestWorkItemJSONMatchesREST(t *testing.T) {
	rec := json.RawMessage(`{"id":"sty_x"}`)
	h := hosted.WorkstateItem{
		ID: "sty_x", Kind: "story", Type: "stories", Status: "backlog",
		Title: "T", Origin: "cli-sync", Record: rec,
	}
	r := api.WorkItem{
		ID: "sty_x", Kind: "story", Type: "stories", Status: "backlog",
		Title: "T", Origin: "cli-sync", Record: rec,
	}
	assertByteIdenticalJSON(t, h, r)
}

func TestLedgerEntryJSONMatchesREST(t *testing.T) {
	rec := json.RawMessage(`{"id":"led_x"}`)
	h := hosted.WorkstateLedgerRow{
		ID: "led_x", StoryID: "sty_x", Kind: "transition", Type: "ledger",
		Origin: "cli-sync", Record: rec,
	}
	r := api.LedgerEntry{
		ID: "led_x", StoryID: "sty_x", Kind: "transition", Type: "ledger",
		Origin: "cli-sync", Record: rec,
	}
	assertByteIdenticalJSON(t, h, r)
}

func TestApplyResponseJSONMatchesREST(t *testing.T) {
	h := hosted.WorkstateIngestResult{Items: 2, Ledger: 1, Outputs: 1}
	r := api.ApplyResponse{Items: 2, Ledger: 1, Outputs: 1}
	assertByteIdenticalJSON(t, h, r)
}

func TestSyncAuthMetadata(t *testing.T) {
	got := api.SyncAuthMetadata("tok")
	if got["authorization"] != "Bearer tok" {
		t.Fatalf("SyncAuthMetadata = %#v", got)
	}
	if len(got) != 1 {
		t.Fatalf("unexpected extra keys: %#v", got)
	}
}

// TestEverySyncProtoHasGeneratedOutput guards the buf template split
// (sty_73c9aa10): the board has its own template and package, and every other
// proto in api/ still generates into syncpb. A template that silently drops a
// sync proto (or lets board.proto leak into syncpb) fails here.
func TestEverySyncProtoHasGeneratedOutput(t *testing.T) {
	protos, err := filepath.Glob("*.proto")
	if err != nil || len(protos) == 0 {
		t.Fatalf("glob api/*.proto: %v (%d found)", err, len(protos))
	}
	for _, p := range protos {
		name := strings.TrimSuffix(p, ".proto")
		dir := filepath.Join("..", "internal", "hosted", "syncpb")
		if _, err := os.Stat(filepath.Join(dir, name+".pb.go")); err != nil {
			t.Errorf("%s has no generated output in syncpb: %v", p, err)
		}
	}
}

func TestCheckoutSyncProtoContract(t *testing.T) {
	protoBytes, err := os.ReadFile("checkout_sync.proto")
	if err != nil {
		t.Fatalf("read checkout_sync.proto: %v", err)
	}
	proto := string(protoBytes)

	for _, want := range []string{
		"service Sync",
		"rpc Apply(",
		"rpc Snapshot(",
		"rpc Refresh(",
		"RefreshRequest",
		"RefreshResponse",
		"refresh_token",
		"access_token",
		"expires_in",
		"page_size = 3",
		"page_token = 4",
		"since_token = 5",
		"story_ids = 6",
		"next_page_token = 3",
		"sync_token = 4",
	} {
		if !strings.Contains(proto, want) {
			t.Errorf("checkout_sync.proto missing %q", want)
		}
	}
	for _, forbid := range []string{
		"rpc Subscribe",
		"rpc Login",
		"rpc Authenticate",
	} {
		if strings.Contains(proto, forbid) {
			t.Errorf("checkout_sync.proto must not contain %q", forbid)
		}
	}
	if !strings.Contains(proto, "satelle-server copies this proto") {
		t.Error("checkout_sync.proto header must name the satelle-server copy posture")
	}

	readmeBytes, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	readme := string(readmeBytes)
	for _, want := range []string{
		"UNAUTHENTICATED",
		"authorization",
		"Bearer",
		"/oauth/token",
		"No gRPC login",
		"checkout_sync.proto",
		"Sync.Refresh",
		"64 MiB",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("api/README.md missing checkout-sync marker %q", want)
		}
	}
	if strings.Contains(readme, "grant **over HTTP**") {
		t.Error("api/README.md gRPC section still instructs HTTP refresh")
	}
}
