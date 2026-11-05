package cli

// Story attachments travel with the story (sty_4d42e13e). A story's markdown
// attachments — plan, step summaries, the type:change patch — live only in the
// machine's runtime stories dir, so without this leg another machine reads the
// story and its ledger but not what the story changed. Work-state sync carries
// them through the bound project's personal documents collection at
// stories/<id>/<file>.md, byte for byte. Under a team scope with a team
// workspace resolved they also go to that workspace's documents collection at the
// same path (sty_61872918), the only collection a team remote reads.
//
// This REVERSES sty_948ad5df's decision that change patches stay off the wire.
// The reversal is deliberate (epic:substrate-continuity: the personal project is
// the substrate's only off-machine home) and bounded: nothing leaves the machine
// under a local scope; binary attachments never leave it;
// and no attachment body ever rides the workstate items/ledger Apply — the
// documents routes are the only way out.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// attachmentRoot is the documents-collection prefix story attachments live under.
// subsync.ExcludedLocal keeps it out of the config-document sync, so the two
// never see each other's files.
const attachmentRoot = "stories/"

func attachmentPath(id, file string) string { return attachmentRoot + id + "/" + file }

// attachmentKey is a path's key in the uploaded-state record: "<id>/<file>".
func attachmentKey(path string) string { return strings.TrimPrefix(path, attachmentRoot) }

// parseAttachmentPath splits stories/<id>/<file>.md; ok is false for any other
// document path, including one that would escape the attachment dir.
func parseAttachmentPath(p string) (id, file string, ok bool) {
	rest, found := strings.CutPrefix(p, attachmentRoot)
	if !found {
		return "", "", false
	}
	id, file, found = strings.Cut(rest, "/")
	if !found || !strings.HasPrefix(id, "sty_") || !strings.HasSuffix(file, ".md") ||
		strings.ContainsAny(file, "/\\") || file == ".." || id == ".." {
		return "", "", false
	}
	return id, file, true
}

func contentSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// isLocalStory reports whether id is a story row in this machine's store.
func isLocalStory(ctx context.Context, a *app.App, id string) bool {
	it, err := a.Store.Stories.Get(ctx, id)
	return err == nil && it.Kind == workitem.KindStory
}

// pendingAttachment is a local markdown attachment the hosted store has not
// confirmed at its current content.
type pendingAttachment struct {
	file    verb.AttachmentFile
	sha     string
	hostRel string
}

// teamStateKey is the uploaded-state / pull-cursor "project" for a team
// workspace's collection. The ws: prefix keeps it from colliding with a project id.
func teamStateKey(workspaceID string) string { return "ws:" + workspaceID }

// teamWorkspaceID resolves the team workspace a team scope also sends
// attachments to. id is "" — with one line on errw saying the team copy is
// skipped — under a team scope with no team workspace resolved; it is "" and
// silent under any other scope. The personal leg never depends on it.
func teamWorkspaceID(ctx context.Context, errw io.Writer, cfg config.Config, syncScope config.Scope, client func() *hosted.Client) (name, id string) {
	if syncScope != config.TeamScope {
		return "", ""
	}
	name = resolveTeamWorkspaceName(cfg, "")
	if name == "" {
		fmt.Fprintln(errw, "attachments: the scope is team but no team workspace is set ([hosted] workspace) — team copy skipped")
		return "", ""
	}
	id, err := client().ActiveWorkspaceID(ctx, name)
	if err != nil {
		fmt.Fprintf(errw, "attachments: the scope is team but team workspace %q did not resolve (%v) — team copy skipped\n", name, err)
		return "", ""
	}
	return name, id
}

// attachmentDest is one documents collection the push leg writes: the bound
// project's personal collection, and — under a team scope when a team
// workspace resolves — the team workspace's. Both receive the same files under the
// same rules; only the uploaded-state key and the PUT route differ.
type attachmentDest struct {
	tag      string // names the destination in output: "" personal, " (team <name>)"
	stateKey string // the uploaded-state "project": the project id, or "ws:<id>"
	put      func(ctx context.Context, path string, content []byte) error

	pending   map[string][]pendingAttachment
	unchanged int
	held      int
}

// pushStoryAttachments is the attachment leg of `sync workstate push`. It runs on
// every exit of the push that reached the server, including the ones that sent no
// rows, because which files to upload is decided by the per-file uploaded-state
// record alone, never by which item or ledger rows the push carried. A failed
// upload is reported and left out of the record, so the next push retries it.
// Nothing here can fail the push: its errors go to errw.
//
// Each file goes to the personal collection and, with stories shared and a team
// workspace resolved, to the team workspace's collection too (sty_61872918): a
// team remote reads only its workspace's collection. Which stories are eligible,
// which files are binary and which stories are held is decided once; the sha skip,
// the upload and the record are per destination.
func pushStoryAttachments(ctx context.Context, out, errw io.Writer, a *app.App, syncScope config.Scope, optIn map[string]bool, clientFor func() *hosted.Client, server, project string, scope scopeSet) {
	if !optIn["stories"] {
		return
	}
	ids, err := verb.StoryAttachmentIDs()
	if err != nil {
		fmt.Fprintf(errw, "attachments: %v\n", err)
		return
	}
	cands := map[string][]pendingAttachment{}
	var binary int
	for _, id := range ids {
		if !scope.HasItem(id) || !isLocalStory(ctx, a, id) {
			continue
		}
		files, nbin, ferr := verb.StoryAttachmentFiles(id)
		if ferr != nil {
			fmt.Fprintf(errw, "attachments: %s: %v\n", id, ferr)
			continue
		}
		binary += nbin
		for _, f := range files {
			cands[id] = append(cands[id], pendingAttachment{file: f, sha: contentSHA(f.Content), hostRel: attachmentPath(id, f.Name)})
		}
	}

	dests := []*attachmentDest{{
		stateKey: project,
		put: func(ctx context.Context, path string, content []byte) error {
			_, err := clientFor().PushDocumentFile(ctx, project, path, content)
			return err
		},
	}}
	if len(cands) > 0 {
		if name, wsID := teamWorkspaceID(ctx, errw, a.Config, syncScope, clientFor); wsID != "" {
			dests = append(dests, &attachmentDest{
				tag:      " (team " + name + ")",
				stateKey: teamStateKey(wsID),
				put: func(ctx context.Context, path string, content []byte) error {
					_, err := clientFor().PushWorkspaceDocumentFile(ctx, wsID, path, content)
					return err
				},
			})
		}
	}
	for _, d := range dests {
		d.pending = map[string][]pendingAttachment{}
		uploaded, lerr := hosted.LoadUploadedAttachments(server, d.stateKey, a.RepoRoot)
		if lerr != nil {
			fmt.Fprintf(errw, "attachments%s: load uploaded state: %v\n", d.tag, lerr)
			return
		}
		for id, cs := range cands {
			for _, c := range cs {
				if uploaded[attachmentKey(c.hostRel)] == c.sha {
					d.unchanged++
					continue
				}
				d.pending[id] = append(d.pending[id], c)
			}
		}
	}

	// A story whose code is not on the remote yet is held for its attachments
	// exactly as it is for its rows; its files stay out of the record and go once
	// the hold clears. A hold is per story, so every destination holds the same
	// stories.
	for _, d := range dests {
		d.upload(ctx, out, errw, server, a.RepoRoot, binary)
	}
}

// upload sends the destination's pending files, records the confirmed ones and
// prints its summary line. A refused upload (any non-2xx the client turns into an
// error) is counted failed and left unrecorded, so the next push retries it.
func (d *attachmentDest) upload(ctx context.Context, out, errw io.Writer, server, repoRoot string, binary int) {
	var nUp, failed int
	confirmed := map[string]string{}
	stopped := false
	for _, id := range slices.Sorted(maps.Keys(d.pending)) {
		for _, p := range d.pending[id] {
			if stopped {
				failed++
				continue
			}
			if perr := d.put(ctx, p.hostRel, p.file.Content); perr != nil {
				failed++
				fmt.Fprintf(errw, "attachment upload failed%s %s: %v\n", d.tag, p.hostRel, perr)
				stopped = errors.Is(perr, hosted.ErrLoginRequired)
				continue
			}
			confirmed[attachmentKey(p.hostRel)] = p.sha
			nUp++
		}
	}
	if err := hosted.RecordUploadedAttachments(server, d.stateKey, repoRoot, confirmed); err != nil {
		fmt.Fprintf(errw, "attachments%s: record uploaded state: %v\n", d.tag, err)
	}

	// The personal line stays quiet on a settled push; the team line always says
	// what it holds unchanged, so a shared story's team copy is visibly in place.
	if nUp == 0 && failed == 0 && d.held == 0 && binary == 0 && (d.tag == "" || d.unchanged == 0) {
		return
	}
	line := fmt.Sprintf("attachments%s: %d uploaded, %d unchanged", d.tag, nUp, d.unchanged)
	if d.held > 0 {
		line += fmt.Sprintf(", %d held with their stories", d.held)
	}
	if failed > 0 {
		line += fmt.Sprintf(", %d failed (retried next push)", failed)
	}
	if binary > 0 {
		line += fmt.Sprintf(", %d binary not mirrored", binary)
	}
	fmt.Fprintln(out, line)
}

func setOf(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

// attachmentSource is one documents collection the pull leg reads, the mirror of
// attachmentDest.
type attachmentSource struct {
	tag      string // "" personal, " (team <name>)"
	noun     string // what a differing local file differs from: "hosted", "the team copy"
	stateKey string
	list     func(ctx context.Context, since string) (hosted.DocumentChanges, error)
	fetch    func(ctx context.Context, path string) ([]byte, string, error)
}

// pullStoryAttachments is the attachment leg of `sync workstate pull` and
// `snapshot`: it reads the personal collection and, with stories shared and a
// team workspace resolved, the team workspace's, each from its own cursor. The
// team pass runs last, so under --force a team copy that differs from the personal
// one wins: the team collection is what the team's remote runs read. Without
// --force a differing local file is kept, whichever collection offered it.
func pullStoryAttachments(ctx context.Context, out, errw io.Writer, a *app.App, syncScope config.Scope, client *hosted.Client, server, project string, scope scopeSet, force bool) {
	srcs := []attachmentSource{{
		noun:     "hosted",
		stateKey: project,
		list: func(ctx context.Context, since string) (hosted.DocumentChanges, error) {
			return client.ListDocumentChanges(ctx, project, since)
		},
		fetch: func(ctx context.Context, path string) ([]byte, string, error) {
			return client.DocumentFileContent(ctx, project, path)
		},
	}}
	if name, wsID := teamWorkspaceID(ctx, errw, a.Config, syncScope, func() *hosted.Client { return client }); wsID != "" {
		srcs = append(srcs, attachmentSource{
			tag:      " (team " + name + ")",
			noun:     "the team copy",
			stateKey: teamStateKey(wsID),
			list: func(ctx context.Context, since string) (hosted.DocumentChanges, error) {
				return client.ListWorkspaceDocumentChanges(ctx, wsID, since)
			},
			fetch: func(ctx context.Context, path string) ([]byte, string, error) {
				return client.WorkspaceDocumentFileContent(ctx, wsID, path)
			},
		})
	}
	for _, src := range srcs {
		pullAttachmentsFrom(ctx, out, errw, a, src, server, scope, force)
	}
}

// pullAttachmentsFrom lists one collection's changes since the stored cursor and
// writes each stories/<id>/<file>.md of a story this machine holds into the
// attachment store, so `satelle story doc` reads it. A local file that differs
// from the collection's is kept and named unless force is set; a local file the
// collection lacks is never touched. Failures are reported and hold the cursor
// back so the next pull retries; they never fail the pull. A scoped pass (scope
// non-nil) lists everything, takes only the scoped stories and leaves the cursor
// alone.
func pullAttachmentsFrom(ctx context.Context, out, errw io.Writer, a *app.App, src attachmentSource, server string, scope scopeSet, force bool) {
	cursor := ""
	if scope == nil && !force {
		c, err := hosted.LoadAttachmentCursor(server, src.stateKey, a.RepoRoot)
		if err != nil {
			fmt.Fprintf(errw, "attachments%s: load cursor: %v\n", src.tag, err)
			return
		}
		cursor = c
	}
	changes, err := src.list(ctx, cursor)
	if err != nil {
		fmt.Fprintf(errw, "attachments%s: list %s documents: %v\n", src.tag, src.noun, err)
		return
	}

	local := map[string]map[string]string{} // story id → file → sha, read once per story
	localFiles := func(id string) map[string]string {
		if m, ok := local[id]; ok {
			return m
		}
		m := map[string]string{}
		files, _, _ := verb.StoryAttachmentFiles(id)
		for _, f := range files {
			m[f.Name] = contentSHA(f.Content)
		}
		local[id] = m
		return m
	}

	var written, same, failed int
	var kept []string
	complete := true
	confirmed := map[string]string{}
	for _, item := range changes.Items {
		id, file, ok := parseAttachmentPath(item.Path)
		if !ok || !scope.HasItem(id) {
			continue
		}
		if !isLocalStory(ctx, a, id) {
			// The story may arrive on a later pass; do not move the cursor past its files.
			complete = false
			continue
		}
		rel := attachmentKey(item.Path)
		localSHA, have := localFiles(id)[file]
		if have && strings.EqualFold(localSHA, item.BlobSHA256) {
			same++
			confirmed[rel] = localSHA
			continue
		}
		body, _, ferr := src.fetch(ctx, item.Path)
		if ferr != nil {
			if errors.Is(ferr, hosted.ErrDocumentFileMissing) {
				continue // listed then deleted
			}
			fmt.Fprintf(errw, "attachment fetch failed%s %s: %v\n", src.tag, item.Path, ferr)
			failed++
			complete = false
			if errors.Is(ferr, hosted.ErrLoginRequired) {
				break
			}
			continue
		}
		hostedSHA := contentSHA(body)
		switch {
		case have && localSHA == hostedSHA:
			same++
			confirmed[rel] = hostedSHA
		case have && !force:
			kept = append(kept, item.Path)
		default:
			if werr := verb.WriteStoryAttachmentFile(id, file, body); werr != nil {
				fmt.Fprintf(errw, "attachment write failed%s %s: %v\n", src.tag, item.Path, werr)
				failed++
				complete = false
				continue
			}
			localFiles(id)[file] = hostedSHA
			confirmed[rel] = hostedSHA
			written++
		}
	}
	// What this machine now holds identical to the collection need not be uploaded back.
	if err := hosted.RecordUploadedAttachments(server, src.stateKey, a.RepoRoot, confirmed); err != nil {
		fmt.Fprintf(errw, "attachments%s: record uploaded state: %v\n", src.tag, err)
	}
	if scope == nil && complete && changes.Cursor != "" {
		if err := hosted.SaveAttachmentCursor(server, src.stateKey, a.RepoRoot, changes.Cursor); err != nil {
			fmt.Fprintf(errw, "attachments%s: save cursor: %v\n", src.tag, err)
		}
	}
	for _, p := range kept {
		fmt.Fprintf(out, "kept local %s — differs from %s; --force takes %s\n", p, src.noun, src.noun)
	}
	if written+same+len(kept)+failed == 0 {
		return
	}
	fmt.Fprintf(out, "attachments%s: %d written, %d unchanged, %d kept (local differs; --force to take %s)", src.tag, written, same, len(kept), src.noun)
	if failed > 0 {
		fmt.Fprintf(out, ", %d failed (retried next pull)", failed)
	}
	fmt.Fprintln(out)
}
