package api_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/api"
)

// assertByteIdenticalJSON marshals two values (one from hosted, one from api)
// and asserts the JSON output is byte-identical.
func assertByteIdenticalJSON(t *testing.T, hostedVal, apiVal any) {
	t.Helper()
	hBytes, err := json.Marshal(hostedVal)
	if err != nil {
		t.Fatalf("marshal hosted value: %v", err)
	}
	rBytes, err := json.Marshal(apiVal)
	if err != nil {
		t.Fatalf("marshal api value: %v", err)
	}
	if string(hBytes) != string(rBytes) {
		t.Errorf("JSON mismatch:\n  hosted: %s\n  api:    %s", hBytes, rBytes)
	}
}

// TestOAuthErrorFieldNames verifies OAuthError marshals with the correct
// JSON keys (error, error_description) matching the /oauth/token spec.
func TestOAuthErrorFieldNames(t *testing.T) {
	oe := api.OAuthError{Code: "invalid_grant", Description: "bad token"}
	b, err := json.Marshal(oe)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"error"`, `"error_description"`} {
		if !strings.Contains(s, key) {
			t.Errorf("OAuthError JSON missing key %q: got %s", key, s)
		}
	}
}

// TestErrorEnvelopeFieldNames verifies ErrorEnvelope marshals with the
// correct JSON keys matching the server error response shape.
func TestErrorEnvelopeFieldNames(t *testing.T) {
	env := api.ErrorEnvelope{Error: "not_found", Message: "project does not exist"}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"error"`, `"message"`} {
		if !strings.Contains(s, key) {
			t.Errorf("ErrorEnvelope JSON missing key %q: got %s", key, s)
		}
	}
}

// TestNoCrossImports verifies that internal/ does not import api/ (production
// code must be independent; the test importing both sides is fine).
func TestNoCrossImports(t *testing.T) {
	entries, err := os.ReadDir("../internal")
	if err != nil {
		t.Fatalf("read internal/: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := os.ReadDir("../internal/" + e.Name())
		if err != nil {
			continue
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".go") {
				continue
			}
			path := "../internal/" + e.Name() + "/" + f.Name()
			content, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if strings.Contains(string(content), "github.com/bobmcallan/satelle/api") {
				t.Errorf("internal package %s/%s imports api/ — cross-import forbidden", e.Name(), f.Name())
			}
		}
	}
}

// TestWorkstateRESTPathsGone verifies the retired REST workstate routes are
// not republished (sty_f85fac97). Schemas stay — they describe Sync shapes.
func TestWorkstateRESTPathsGone(t *testing.T) {
	yamlBytes, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	yaml := string(yamlBytes)
	for _, route := range []string{
		"/api/v1/projects/{project}/workstate:",
		"/api/v1/projects/{project}/workstate/items:",
		"/api/v1/projects/{project}/workstate/ledger:",
		"ingestWorkstate",
		"listWorkstateItems",
		"listWorkstateLedger",
	} {
		if strings.Contains(yaml, route) {
			t.Errorf("openapi.yaml still publishes retired REST workstate %q", route)
		}
	}
}
