//go:build integration && unix

package tests

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/hook"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func TestThisReposSessionStartOpensWithTheFramingLine(t *testing.T) {
	s := newSandbox(t)
	copyCheckout(t, "..", s.dir)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "this repo's tree")

	root := filepath.Join(s.dir, ".satelle")
	description := strings.TrimSpace(readFraming(t, root).Meta["description"])
	if description == "" {
		t.Fatal("this repo's reference principle has no description")
	}
	cfg, err := kernel.LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, h := range hook.Harnesses {
		limit := cfg.Harness[h].ContextLimitBytes
		if limit <= 0 {
			continue
		}
		checked++
		payload := fmt.Sprintf(`{"session_id":"s-frame","cwd":%q,"workspace_roots":[%q],"hook_event_name":"sessionStart"}`, s.dir, s.dir)
		r := s.exec(satelleBin, payload, "hook", "context", "--harness", h)
		if r.code != 0 {
			t.Fatalf("%s: hook context exit %d:\n%s%s", h, r.code, r.out, r.err)
		}
		var env struct {
			Hook struct {
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
			Cursor string `json:"additional_context"`
			Grok   string `json:"systemMessage"`
			Agy    []struct {
				System struct {
					Message string `json:"systemMessage"`
				} `json:"systemMessage"`
			} `json:"injectSteps"`
		}
		if err := json.Unmarshal([]byte(r.out), &env); err != nil {
			t.Fatalf("%s: %v\n%s", h, err, r.out)
		}
		ctx := env.Hook.Context
		switch h {
		case hook.Cursor:
			ctx = env.Cursor
		case hook.Grok:
			ctx = env.Grok
		case hook.Agy:
			if len(env.Agy) > 0 {
				ctx = env.Agy[0].System.Message
			}
		}
		if !strings.HasPrefix(ctx, description+"\n") {
			t.Errorf("%s: this repo's session start does not open with the framing line:\n%.400s", h, ctx)
		}
		end := strings.Index(r.out, description) + len(description)
		if end < len(description) || end > limit {
			t.Errorf("%s: the framing line ends at byte %d of the output, past the limit of %d", h, end, limit)
		}
		if len(r.out) > limit {
			t.Errorf("%s: this repo's session start is %d bytes, over its limit of %d", h, len(r.out), limit)
		}
	}
	if checked == 0 {
		t.Fatal("this repo sets no session-start limit for any harness")
	}
}
