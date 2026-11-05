// Scratch gives every dispatched or live agent session its own throwaway
// directory outside the repo tree (sty_e7aaf8b1). No skill/principle needs to
// tell an agent where to write evidence or debris — TMPDIR/SATELLE_SCRATCH
// name it by mechanism, and the charter states it in every isolated briefing.
package agentstep

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
)

// dispatchID is 8 random hex — unique per call, and deliberately short: the
// scratch path must leave room for a Unix socket (108-byte sun_path) beneath
// it, e.g. chromedp's <TMPDIR>/chromedp-runner<n>/SingletonSocket. It is not
// sortable; diagnosis ordering comes from the scratch_kept ledger row and the
// directory mtime. Uniqueness is guaranteed by the exclusive create in
// newScratch, not by the id alone.
func dispatchID() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// newDispatchID is the id source newScratch draws from; a var so tests can
// force a collision.
var newDispatchID = dispatchID

// storyComponent is the path segment for a scratch dir's story leg. An empty
// storyID (a dispatch with no item, e.g. a bootstrap check) still gets an
// isolated directory rather than colliding with every other adhoc caller.
func storyComponent(storyID string) string {
	if storyID == "" {
		return "adhoc-" + newDispatchID()
	}
	return storyID
}

// scratchParentIn is the story-level parent of a dispatch scratch directory
// under an explicit temp root. newScratch and the path-budget tests share it so
// the construction cannot drift.
func scratchParentIn(base, repoRoot, storyID string) string {
	return config.StoryScratchDirIn(base, repoRoot, storyComponent(storyID))
}

// newScratch creates <tmp>/satelle/<repo-key>/<story>/<dispatch-id>/, mode
// 0700, and returns its path. The leaf is created exclusively, so no two
// dispatches share a directory; a collision regenerates the id. repoRoot may be
// empty in tests; os.TempDir() still anchors the tree.
func newScratch(repoRoot, storyID string) (string, error) {
	parent := scratchParentIn(os.TempDir(), repoRoot, storyID)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("scratch: create %s: %w", parent, err)
	}
	for range 8 {
		dir := filepath.Join(parent, newDispatchID())
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			return dir, nil
		}
		if !os.IsExist(err) {
			return "", fmt.Errorf("scratch: create %s: %w", dir, err)
		}
	}
	return "", fmt.Errorf("scratch: no unique directory under %s", parent)
}

// scratchEnv returns the TMPDIR / SATELLE_SCRATCH pair for dir. Both keys are
// RESERVED — they win over any binding-authored env of the same name, since a
// dispatch's scratch location is mechanism, not something a binding's [env]
// table should be able to redirect.
func scratchEnv(dir string) map[string]string {
	if dir == "" {
		return nil
	}
	return map[string]string{
		"TMPDIR":          dir,
		config.ScratchEnv: dir,
	}
}

// overlayScratchEnv returns a copy of env with ${SATELLE_SCRATCH} in every value
// replaced by dir, then the reserved scratchEnv pair overlaid. It is the single
// place a dispatch's scratch path reaches binding env, so every dispatch shape
// (one-shot, live session, summariser) behaves the same. env is not mutated.
func overlayScratchEnv(env map[string]string, dir string) map[string]string {
	out := make(map[string]string, len(env)+2)
	ref := "${" + config.ScratchEnv + "}"
	for k, v := range env {
		if dir != "" {
			v = strings.ReplaceAll(v, ref, dir)
		}
		out[k] = v
	}
	for k, v := range scratchEnv(dir) {
		out[k] = v
	}
	return out
}

// dispatchEnv is the env every dispatch shape hands its child:
// overlayScratchEnv, then every secret reference (secretref) resolved at this
// point of use (sty_bd8d963c). resolved — reference → value — is the set the
// run's output is redacted of (agentcli.Request.Redact); an unresolvable
// reference fails the dispatch, naming the reference and never a value.
func dispatchEnv(ctx context.Context, env map[string]string, dir string) (map[string]string, map[string]string, error) {
	return overlayScratchEnv(env, dir), nil, nil
}

// scratchBriefing is the charter sentence every isolated/live agent receives
// naming its scratch directory. Generated text, appended in buildRequest — no
// skill or principle file carries this instruction (sty_e7aaf8b1 AC2).
func scratchBriefing(dir string) string {
	if dir == "" {
		return ""
	}
	return fmt.Sprintf("Your scratch directory is `%s` ($%s, also $TMPDIR). "+
		"Write every temporary or evidence file there, never in the repository tree. "+
		"To attach text to the story, use `satelle story attach <id> --name ... --body \"...\"`, "+
		"or `--file $%s/<file>` for a large document.",
		dir, config.ScratchEnv, config.ScratchEnv)
}

// finishScratch disposes of a dispatch's scratch directory: removed when the
// dispatch succeeded and nothing was swept into it, kept (for diagnosis, or
// because leftovers now live under it) otherwise.
func finishScratch(dir string, keep bool) {
	if dir == "" || keep {
		return
	}
	_ = os.RemoveAll(dir)
}
