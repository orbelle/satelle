package remotenode

import (
	"encoding/json"
	"fmt"
)

const (
	typeReady = "ready"
	typeReply = "reply"
)

type envelope struct {
	Type string          `json:"type"`
	Body json.RawMessage `json:"body,omitempty"`
}

func encode(typ string, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{Type: typ, Body: raw})
}

func decode(b []byte) (envelope, error) {
	var e envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return envelope{}, fmt.Errorf("remotenode: malformed message: %w", err)
	}
	if e.Type == "" {
		return envelope{}, fmt.Errorf("remotenode: message has no type")
	}
	return e, nil
}

type FetchedRepo struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

type Report struct {
	Version           string            `json:"version"`
	DefinitionVersion string            `json:"definition_version"`
	Workflows         []string          `json:"workflows"`
	Allow             []string          `json:"allow"`
	Repos             []FetchedRepo     `json:"repos"`
	Bindings          []string          `json:"bindings"`
	Now               int64             `json:"now"`
	Hashes            map[string]string `json:"hashes,omitempty"`
	Error             string            `json:"error,omitempty"`
}

type reply struct {
	Body  json.RawMessage `json:"body,omitempty"`
	Error string          `json:"error,omitempty"`
}
