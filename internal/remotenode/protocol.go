// Package remotenode runs a remote node end to end. satelle-server starts the
// node (`satelle node join`), which fetches the definition it runs from with its
// own node token (NodeClient), installs its substrate, enrolls on the sealed
// channel (package remotechan), fetches the declared repos read-only, reports
// ready and serves jobs. The controller end (Attach) waits for that report.
//
// The node reads its secrets and ends its run through the same client, so
// satelle.dev, not a controller, decides what a node may read and when its token
// stops working.
//
// Everything that crosses the channel is a JSON envelope inside a sealed
// remotechan message. Ready travels outside any job; a request is a job named by
// a type ("agent" here, any other a later story registers) and its reply is the
// same job's message, with the node heartbeating while it works.
package remotenode

import (
	"encoding/json"
	"fmt"
)

// Envelope types for the messages outside a job.
const (
	typeReady = "ready" // node → local: the report, or why it cannot be ready
	typeReply = "reply" // node → local: a job's result
)

// envelope is what a remotechan message body carries.
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

// FetchedRepo is one repo as the node fetched it.
type FetchedRepo struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

// Report is what a node says about itself once it is ready to run the
// definition's workflows.
type Report struct {
	// Version is the satelle the node runs.
	Version string `json:"version"`
	// DefinitionVersion is the version of the definition the node fetched and runs
	// from (the X-Satelle-Definition-Version of its bundle).
	DefinitionVersion string `json:"definition_version"`
	// Workflows are the categories of the remote's effective workflow: its
	// bundle's done.toml merged over the embedded default, or that default alone.
	Workflows []string `json:"workflows"`
	// Allow is the egress allowlist the definition declares.
	Allow []string      `json:"allow"`
	Repos []FetchedRepo `json:"repos"`
	// Bindings are the agents.toml bindings that resolved on the node.
	Bindings []string `json:"bindings"`
	// Now is the node's clock (unix nanoseconds) when it reported, and Hashes is
	// config.ProcessHashes of its process then, after the preflight: the baseline
	// a StoryReportJob is later compared with.
	Now    int64             `json:"now"`
	Hashes map[string]string `json:"hashes,omitempty"`
	// Error, when set, is why the node could not become ready; the other fields
	// are then unreliable.
	Error string `json:"error,omitempty"`
}

// reply is a job's result: Body on success, Error on failure.
type reply struct {
	Body  json.RawMessage `json:"body,omitempty"`
	Error string          `json:"error,omitempty"`
}
