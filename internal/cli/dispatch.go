package cli

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/bobmcallan/satelle/internal/verb"
)

// dispatch invokes a verb with the given request value (marshalled to JSON)
// and renders the response to the command's stdout. This is the one path
// every data command takes — CLI command → verb.Dispatch → store — mirroring
// how the web server will render from the same verbs. Rendering is plain
// indented JSON unless the verb opts into compact mode (render, in
// render.go) — configuration, not this function's concern.
func dispatch(cmd *cobra.Command, name string, req any) error {
	_, err := dispatchResponse(cmd, name, req)
	return err
}

// dispatchResponse is dispatch that also returns the verb's response, nil when
// the call was handed to a detached run.
func dispatchResponse(cmd *cobra.Command, name string, req any) (json.RawMessage, error) {
	var body json.RawMessage
	if req != nil {
		b, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body = b
	}
	// A story whose history this checkout left on the server is fetched first, so
	// any event the verb appends builds on the hosted head (sty_00212637).
	fetchBeforeWrite(cmd, requestStoryID(req))
	resp, err := verb.Dispatch(cmd.Context(), name, body)
	if err != nil {
		return nil, err
	}
	return resp, renderResponse(cmd, name, requestStoryID(req), resp)
}

// requestStoryID extracts the id/story_id a request body already names, so a
// compact fold that offloads content (a long cell, a noisy diff hunk) links
// the stored blob to the story it came from — the same ref semantics every
// other retrieve.Store.Put call in this codebase uses. "" when the request
// carries neither (or isn't a map) — Put still works, it just leaves the blob
// unlinked (an orphan; harmless, content-addressed).
func requestStoryID(req any) string {
	m, ok := req.(map[string]any)
	if !ok {
		return ""
	}
	for _, k := range []string{"id", "story_id"} {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// printJSON pretty-prints a raw JSON message to the command's stdout.
func printJSON(cmd *cobra.Command, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		// Not valid JSON for some reason — emit it verbatim rather than failing.
		fmt.Fprintln(cmd.OutOrStdout(), string(raw))
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), buf.String())
	return nil
}

// putIf adds key→val to req when val is non-empty. Used to build create/list
// request bodies, omitting unset flags so verb defaults apply.
func putIf(req map[string]any, key, val string) {
	if val != "" {
		req[key] = val
	}
}

// putChanged copies a string flag into req[key] only if the user set it —
// giving `set` partial-update semantics (an unpassed flag leaves the field
// untouched, distinct from passing an empty value to clear it).
func putChanged(req map[string]any, f *pflag.FlagSet, flag, key string) {
	if f.Changed(flag) {
		v, _ := f.GetString(flag)
		req[key] = v
	}
}
