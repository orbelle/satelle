package cli

// Snapshot-based substrate sync (sty_fe5a8ed4): the CLI side. Everything that
// decides anything lives in internal/snapsync; this file only builds the two
// route adapters (config, documents) around the hosted client, runs the shared
// driver per area, and prints.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/snapsync"
	"github.com/bobmcallan/satelle/internal/subsync"
)

// configRecords stores snapshot records on the config route. Its manifest is the
// one the caller already fetched, so a run reads it once.
type configRecords struct {
	client   *hosted.Client
	project  string
	versions map[string]int // record path -> head version
}

func newConfigRecords(client *hosted.Client, project string, manifest []hosted.ConfigItem) *configRecords {
	r := &configRecords{client: client, project: project, versions: map[string]int{}}
	for _, it := range manifest {
		if _, ok := snapsync.AreaOfRecordPath(it.Path); ok {
			r.versions[it.Path] = it.Version
		}
	}
	return r
}

func (r *configRecords) Head(_ context.Context, area string) (int, error) {
	return r.versions[snapsync.RecordPath(area)], nil
}

func (r *configRecords) Read(ctx context.Context, area string, version int) ([]byte, bool, error) {
	body, _, err := r.client.ConfigFileContent(ctx, r.project, snapsync.RecordPath(area), version)
	if errors.Is(err, hosted.ErrConfigFileMissing) {
		return nil, false, nil
	}
	return body, err == nil, err
}

func (r *configRecords) Put(ctx context.Context, area string, body []byte) (int, error) {
	res, err := r.client.PushConfigFile(ctx, r.project, snapsync.RecordPath(area), body)
	if err != nil {
		return 0, err
	}
	r.versions[snapsync.RecordPath(area)] = res.Version
	return res.Version, nil
}

// configBlobs is the config route as the driver's file store.
type configBlobs struct {
	client   *hosted.Client
	project  string
	manifest []hosted.ConfigItem
}

func (b *configBlobs) Heads(context.Context) (map[string]string, error) {
	return headSHAByPath(b.manifest), nil
}

func (b *configBlobs) Get(ctx context.Context, path string) ([]byte, error) {
	body, _, err := b.client.ConfigFileContent(ctx, b.project, path, 0)
	if errors.Is(err, hosted.ErrConfigFileMissing) {
		return nil, snapsync.ErrBlobMissing
	}
	return body, err
}

func (b *configBlobs) Put(ctx context.Context, path string, content []byte) (bool, error) {
	res, err := b.client.PushConfigFile(ctx, b.project, path, content)
	return res.Created, err
}

// docBlobs is the documents route as the driver's file store.
type docBlobs struct {
	client  *hosted.Client
	project string
}

func (b *docBlobs) Heads(ctx context.Context) (map[string]string, error) {
	changes, err := b.client.ListDocumentChanges(ctx, b.project, "")
	if err != nil {
		return nil, err
	}
	return headSHAByPath(changes.Items), nil
}

func (b *docBlobs) Get(ctx context.Context, path string) ([]byte, error) {
	body, _, err := b.client.DocumentFileContent(ctx, b.project, path)
	if errors.Is(err, hosted.ErrDocumentFileMissing) {
		return nil, snapsync.ErrBlobMissing
	}
	return body, err
}

func (b *docBlobs) Put(ctx context.Context, path string, content []byte) (bool, error) {
	res, err := b.client.PushDocumentFile(ctx, b.project, path, content)
	if errors.Is(err, hosted.ErrDocumentConflict) {
		return false, fmt.Errorf("conflict — resolve on the server and retry")
	}
	return res.Created, err
}

// The commands a push refused for a hosted copy that is ahead names. snapsync
// picks between them by the test `sync status` reports diverged with: a pull of
// a behind area overwrites nothing local, so it is the way forward; a diverged
// area still holds this machine's own changes, and only reconcile shows both
// sides and backs up the one it overrides.
const (
	pushBehindRemedy   = "satelle sync pull"
	pushDivergedRemedy = "satelle sync reconcile"
)

// newSnapDriver builds the shared driver for one route. ign is the command's
// loaded .satelleignore (config.LoadIgnore), passed in rather than cached.
func newSnapDriver(cfg config.Config, client *hosted.Client, out io.Writer, server, project, repoRoot, dataDir string, ign config.Ignore, rec snapsync.Records, blobs snapsync.Blobs) *snapsync.Driver {
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		abs = repoRoot
	}
	return &snapsync.Driver{
		Server: server, Project: project, RepoRoot: abs, DataDir: dataDir,
		By: client.Location(), Records: rec, Blobs: blobs, Out: out, PullCmd: pushBehindRemedy, DivergedCmd: pushDivergedRemedy,
		Owns:     func(area, p string) bool { return config.SyncAreaOf(cfg, repoRoot, p, ign) == area },
		SkipBody: config.UneditedDefault,
		Defaults: func(area string) (map[string]map[string]bool, error) {
			return config.AreaDefaultShas(cfg, repoRoot, area, ign)
		},
		StampOnly: func(area string) (map[string][]byte, error) {
			files, err := config.AreaStampOnlyDefaults(cfg, repoRoot, area, ign)
			if err != nil {
				return nil, err
			}
			out := make(map[string][]byte, len(files))
			for _, f := range files {
				out[f.Path] = f.Content
			}
			return out, nil
		},
		Ignore: ign,
	}
}

// pushOutcome is a snapshot push's blob counts, summed over areas, and what each
// area's push did: claimed a snapshot, or lost the race for one after claiming.
type pushOutcome struct {
	created, unchanged, notUploaded int
	claimed, raced                  map[string]bool
}

// mark records that area's push did what set names, on first use of the map.
func (o *pushOutcome) mark(set *map[string]bool, area string) {
	if *set == nil {
		*set = map[string]bool{}
	}
	(*set)[area] = true
}

// runSnapshotPush prepares every area's push first — so a refusal in any area
// uploads nothing anywhere — then runs them. handled=false means snapshots are
// unavailable on this server and the caller should use the plain upload.
//
// snapshotOnly (--snapshot-only, sty_a66e1846) closes that fallback: where the
// hosted snapshot cannot be read the push is refused instead, because the plain
// upload is the one path that cannot tell a behind or diverged machine from an
// in-step one, and an unattended caller must never overwrite on doubt.
func runSnapshotPush(ctx context.Context, d *snapsync.Driver, areaFiles map[string][]snapsync.PushFile, areas []string, snapshotOnly bool) (out pushOutcome, handled bool, err error) {
	if err := refusePullOnly(d.RepoRoot); err != nil {
		return out, true, err
	}
	var jobs []*snapsync.PushJob
	var refusals []error
	for _, area := range areas {
		job, perr := d.PreparePush(ctx, area, areaFiles[area])
		var unavailable *snapsync.UnavailableError
		var behind *snapsync.ErrBehind
		var unmerged *snapsync.ErrUnmerged
		switch {
		case perr == nil:
			jobs = append(jobs, job)
		case errors.As(perr, &unavailable):
			if snapshotOnly {
				return out, true, snapshotOnlyRefusal(perr)
			}
			if d.Out != nil {
				fmt.Fprintf(d.Out, "%v — pushing without a snapshot.\n", perr)
			}
			return out, false, nil
		case errors.As(perr, &behind), errors.As(perr, &unmerged):
			refusals = append(refusals, perr)
		default:
			return out, true, perr
		}
	}
	if len(refusals) > 0 {
		// Joined, not flattened: the text is one refusal per line, and a caller
		// can still tell which refusals they were.
		return out, true, errors.Join(refusals...)
	}
	// A file the server refuses (too large, say) stops neither its own area nor
	// the areas after it: every job runs, and the refusals come back joined so the
	// command names each path and exits non-zero (sty_195dd215).
	var notStored []error
	for _, job := range jobs {
		res, perr := d.Push(ctx, job)
		var uploadErr *snapsync.UploadError
		if perr != nil && !errors.As(perr, &uploadErr) {
			var behind *snapsync.ErrBehind
			if errors.As(perr, &behind) {
				out.mark(&out.raced, job.Area)
			}
			return out, true, perr
		}
		if perr != nil {
			notStored = append(notStored, perr)
		}
		if res.Claimed {
			out.mark(&out.claimed, job.Area)
		}
		out.created += res.Created
		out.unchanged += res.Unchanged
		out.notUploaded += res.NotUploaded
	}
	return out, true, errors.Join(notStored...)
}

// snapshotOnlyRefusal is the error a --snapshot-only push exits with when it
// could not read the hosted snapshot or manifest. Nothing was uploaded.
func snapshotOnlyRefusal(cause error) error {
	return fmt.Errorf("snapshot-only: %w — not pushing without a behind/diverged check", cause)
}

// pushConfigSnapshots is the config push when the hosted copy can hold snapshots.
func pushConfigSnapshots(cmd *cobra.Command, cfg config.Config, repoRoot, dataDir string, ign config.Ignore, client *hosted.Client, server, project string, manifest []hosted.ConfigItem, files []config.ConfigFile) (pushOutcome, bool, error) {
	byArea := map[string][]snapsync.PushFile{}
	for _, f := range files {
		byArea[f.Area] = append(byArea[f.Area], snapsync.PushFile{Path: f.Path, Content: f.Content})
	}
	// Only a non-local scope reaches a push, and under one every config area moves.
	areas := append([]string{}, config.ConfigAreas...)
	d := newSnapDriver(cfg, client, cmd.OutOrStdout(), server, project, repoRoot, dataDir, ign,
		newConfigRecords(client, project, manifest),
		&configBlobs{client: client, project: project, manifest: manifest})
	d.Prune = pruneRequested(cmd)
	d.Force = forceRequested(cmd)
	out, handled, err := runSnapshotPush(cmd.Context(), d, byArea, areas, snapshotOnlyRequested(cmd))
	if handled {
		// A refused push records why it was refused (behind, unmerged) just as a
		// completed one records in step.
		recordAreaStates(cmd.Context(), cfg, repoRoot, d, areas)
	}
	return out, handled, err
}

// pushDocumentsSnapshot is the documents push when the hosted copy can hold
// snapshots. The records ride the config route, so it reads that manifest; a
// server without one gets the plain upload.
func pushDocumentsSnapshot(cmd *cobra.Command, cfg config.Config, repoRoot, dataDir string, ign config.Ignore, client *hosted.Client, server, project string, files []config.ConfigFile) (pushOutcome, bool, error) {
	manifest, err := client.ConfigManifest(cmd.Context(), project)
	if err != nil {
		if errors.Is(err, hosted.ErrLoginRequired) {
			return pushOutcome{}, true, err
		}
		if snapshotOnlyRequested(cmd) {
			return pushOutcome{}, true, snapshotOnlyRefusal(err)
		}
		return pushOutcome{}, false, nil
	}
	pf := make([]snapsync.PushFile, 0, len(files))
	for _, f := range files {
		pf = append(pf, snapsync.PushFile{Path: f.Path, Content: f.Content})
	}
	d := newSnapDriver(cfg, client, cmd.OutOrStdout(), server, project, repoRoot, dataDir, ign,
		newConfigRecords(client, project, manifest),
		&docBlobs{client: client, project: project})
	d.Prune = pruneRequested(cmd)
	d.Force = forceRequested(cmd)
	out, handled, err := runSnapshotPush(cmd.Context(), d, map[string][]snapsync.PushFile{"documents": pf}, []string{"documents"}, snapshotOnlyRequested(cmd))
	if handled {
		recordAreaStates(cmd.Context(), cfg, repoRoot, d, []string{"documents"})
	}
	return out, handled, err
}

// pruneFlagUsage is the one help text every command that takes --prune shares.
const pruneFlagUsage = "When the hosted copy has no snapshot yet, publish this tree as the whole truth: hosted files this tree lacks are dropped instead of carried forward."

// pruneRequested reads --prune from the command that is running. A command
// that does not define it (a test's bare command) reads as false.
func pruneRequested(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("prune")
	return f != nil && f.Value.String() == "true"
}

// forceFlagUsage is the one help text every command that takes --force on a
// snapshot push shares.
const forceFlagUsage = "Publish this tree as the next snapshot even when the hosted copy is ahead of it: files only the hosted copy has are dropped (there is no merge), and other machines get this tree on their next deploy. An unmerged conflict copy is still refused. Applies to the config and documents pushes only."

// forceRequested reads --force from the command that is running. A command
// that does not define it (a test's bare command) reads as false.
func forceRequested(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("force")
	return f != nil && f.Value.String() == "true"
}

// snapshotOnlyFlagUsage is the one help text the config and documents pushes
// share for --snapshot-only.
const snapshotOnlyFlagUsage = "Push only through the snapshot check: refuse, uploading nothing and exiting non-zero, when the area is behind, diverged or unmerged or the hosted snapshot or manifest cannot be read, instead of falling back to a plain upload. satelled passes this on every unattended push."

// snapshotOnlyRequested reads --snapshot-only from the command that is running.
// A command that does not define it (a test's bare command) reads as false.
func snapshotOnlyRequested(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("snapshot-only")
	return f != nil && f.Value.String() == "true"
}

// configDeploySnapshots holds the areas a deploy is driving from a snapshot.
type configDeploySnapshots struct {
	driver *snapsync.Driver
	jobs   []*snapsync.PullJob
	areas  map[string]bool
}

// owns reports whether a manifest path is governed by a snapshot, and so is
// restored only when that snapshot names it.
func (s *configDeploySnapshots) owns(cfg config.Config, repoRoot, path string) bool {
	if s == nil {
		return false
	}
	return s.areas[config.SyncAreaOf(cfg, repoRoot, path, s.driver.Ignore)]
}

// planConfigDeploySnapshots plans each config area the hosted copy holds a
// snapshot for. An area with no snapshot — or a server that cannot read them —
// is simply not governed, and deploys every head as it always did.
//
// A reconcile (rc non-nil, sty_b0873ec3) plans only the areas it names, rewrites
// only what the plan changes, and hands each job the operator's choices.
func planConfigDeploySnapshots(cmd *cobra.Command, cfg config.Config, repoRoot, dataDir string, client *hosted.Client, server, project string, manifest []hosted.ConfigItem, rc *reconcileApply) (*configDeploySnapshots, error) {
	has := map[string]bool{}
	for _, it := range manifest {
		if area, ok := snapsync.AreaOfRecordPath(it.Path); ok {
			has[area] = true
		}
	}
	ign, err := config.LoadIgnore(dataDir)
	if err != nil {
		return nil, err
	}
	s := &configDeploySnapshots{areas: map[string]bool{}}
	s.driver = newSnapDriver(cfg, client, cmd.OutOrStdout(), server, project, repoRoot, dataDir, ign,
		newConfigRecords(client, project, manifest),
		&configBlobs{client: client, project: project, manifest: manifest})
	s.driver.Materialize = rc == nil
	for _, area := range config.ConfigAreas {
		if !has[area] || (rc != nil && !rc.areas[area]) {
			continue
		}
		local, err := pullLocalShas(cfg, repoRoot, server, project, s.driver, area)
		if err != nil {
			// An unreadable local file (an agents layer that does not decode) must
			// not stop a deploy that exists to restore the tree.
			if rc != nil {
				return nil, fmt.Errorf("%s: cannot read the local files: %w", area, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: cannot read the local files (%v) — deploying every head.\n", area, err)
			continue
		}
		job, err := s.driver.PlanPull(cmd.Context(), area, local)
		var unavailable *snapsync.UnavailableError
		if errors.As(err, &unavailable) && rc == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %v — deploying every head.\n", area, err)
			continue
		}
		if err != nil {
			return nil, err
		}
		if job == nil {
			continue
		}
		rc.arm(job)
		if !rc.admit(job) {
			continue
		}
		s.areas[area] = true
		s.jobs = append(s.jobs, job)
	}
	return s, nil
}

// stage runs every job's side effects and returns the files to restore.
func (s *configDeploySnapshots) stage(ctx context.Context) ([]subsync.File, error) {
	// Every area's choices are applied, and what they replace saved, before any
	// area removes or parks a file, so a save that fails leaves the tree as it was.
	for _, job := range s.jobs {
		if err := s.driver.ApplyChoices(ctx, job); err != nil {
			return nil, err
		}
	}
	var files []subsync.File
	for _, job := range s.jobs {
		ws, err := s.driver.Stage(ctx, job)
		if err != nil {
			return nil, err
		}
		files = append(files, ws...)
	}
	return files, nil
}

// commit records each area's base after the restore succeeded.
func (s *configDeploySnapshots) commit(res subsync.Result) error {
	failed := map[string]bool{}
	for _, f := range res.Failed {
		failed[f.Path] = true
	}
	for _, job := range s.jobs {
		if err := s.driver.Commit(job, failed); err != nil {
			return err
		}
	}
	return nil
}

// recordStates records where each deployed area now stands against the hosted
// copy, asked of the driver rather than assumed from the deploy having committed.
func (s *configDeploySnapshots) recordStates(ctx context.Context, cfg config.Config, repoRoot string) {
	areas := make([]string, 0, len(s.jobs))
	for _, job := range s.jobs {
		areas = append(areas, job.Area)
	}
	recordAreaStates(ctx, cfg, repoRoot, s.driver, areas)
}

// pullLocalShas is the sha of each local file of the area as a pull compares it
// with the hosted snapshot (sty_fe5a8ed4): what a push would send, except that a
// fresh clone's placeholder settings count as no local file at all.
func pullLocalShas(cfg config.Config, repoRoot, server, project string, d *snapsync.Driver, area string) (map[string]string, error) {
	local, err := snapsync.LocalShas(cfg, repoRoot, area, d.Ignore)
	if err != nil {
		return nil, err
	}
	if area == "settings" && len(local) > 0 {
		// A fresh clone's satelle.toml is the binding `project bind` wrote, which
		// a push withholds: with no base to say otherwise it is a placeholder, so
		// the hosted settings are restored rather than parked as a conflict.
		if _, synced, berr := hosted.LoadAreaBase(server, project, d.RepoRoot, area); berr == nil && !synced {
			if placeholder, perr := config.PlaceholderSettings(cfg, repoRoot, d.Ignore); perr == nil && placeholder {
				return nil, nil
			}
		}
	}
	return local, nil
}

// pullDocumentsSnapshot is the documents pull when the hosted copy holds a
// documents snapshot. handled=false sends the caller to the cursor-driven pull.
// A reconcile (rc non-nil) hands the job the operator's choices instead.
func pullDocumentsSnapshot(cmd *cobra.Command, cfg config.Config, repoRoot, dataDir string, client *hosted.Client, server, project string, rc *reconcileApply) (bool, error) {
	dp, handled, err := planDocumentsPull(cmd, cfg, repoRoot, dataDir, client, server, project, rc)
	if dp == nil {
		return handled, err
	}
	return true, dp.run(cmd, cfg, repoRoot, dataDir, server, project)
}

// documentsPull is a planned documents snapshot pull: the driver and the job it
// planned. A reconcile plans it first so the documents choices are saved before
// any area stages, then runs it after the config areas.
type documentsPull struct {
	d   *snapsync.Driver
	job *snapsync.PullJob
}

// planDocumentsPull plans the documents pull. A nil result means there is nothing
// to run: handled=false sends the caller to the cursor-driven pull, handled=true
// carries err.
func planDocumentsPull(cmd *cobra.Command, cfg config.Config, repoRoot, dataDir string, client *hosted.Client, server, project string, rc *reconcileApply) (*documentsPull, bool, error) {
	manifest, err := client.ConfigManifest(cmd.Context(), project)
	if err != nil {
		if errors.Is(err, hosted.ErrLoginRequired) {
			return nil, true, err
		}
		return nil, false, nil
	}
	ign, err := config.LoadIgnore(dataDir)
	if err != nil {
		return nil, true, err
	}
	d := newSnapDriver(cfg, client, cmd.OutOrStdout(), server, project, repoRoot, dataDir, ign,
		newConfigRecords(client, project, manifest),
		&docBlobs{client: client, project: project})
	local, err := snapsync.LocalShas(cfg, repoRoot, "documents", ign)
	if err != nil {
		return nil, true, err
	}
	job, err := d.PlanPull(cmd.Context(), "documents", local)
	var unavailable *snapsync.UnavailableError
	if errors.As(err, &unavailable) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if job == nil {
		return nil, false, nil
	}
	rc.arm(job)
	if !rc.admit(job) {
		return nil, true, nil
	}
	return &documentsPull{d: d, job: job}, true, nil
}

// run stages and restores the planned documents pull and commits its base. The
// choices are applied by Stage if the caller has not already applied them.
func (dp *documentsPull) run(cmd *cobra.Command, cfg config.Config, repoRoot, dataDir, server, project string) error {
	d, job := dp.d, dp.job
	out := cmd.OutOrStdout()
	writes, err := d.Stage(cmd.Context(), job)
	if err != nil {
		return err
	}
	res, err := subsync.Restore(dataDir, writes)
	if err != nil {
		return fmt.Errorf("restore documents: %w", err)
	}
	for _, f := range res.Failed {
		fmt.Fprintf(out, "  could not write %s: %v\n", f.Path, f.Err)
	}
	failed := map[string]bool{}
	for _, f := range res.Failed {
		failed[f.Path] = true
	}
	if err := d.Commit(job, failed); err != nil {
		return err
	}
	recordAreaStates(cmd.Context(), cfg, repoRoot, d, []string{"documents"})
	// Hosted heads Restore would refuse (a partition poisoned with backups/
	// entries, sty_84f14ace) are not part of any snapshot; they are still reported,
	// so a pull that skipped something never reads as "up to date".
	skipped := 0
	if heads, herr := d.Heads(cmd.Context()); herr == nil {
		for p := range heads {
			if subsync.ExcludedLocalBy(p, d.Ignore) {
				skipped++
			}
		}
	}
	tail := ""
	if skipped > 0 {
		tail = fmt.Sprintf(", %d skipped (local-only path)", skipped)
	}
	switch {
	case job.Changed() == 0 && len(res.Failed) == 0 && skipped == 0:
		fmt.Fprintf(out, "Documents up to date on %s.\n", server)
	case res.Written == 0:
		fmt.Fprintf(out, "Documents pull on %s%s.\n", server, tail)
	default:
		fmt.Fprintf(out, "Pulled %d document(s) from project %q on %s into %s%s.\n", res.Written, project, server, dataDir, tail)
	}
	return nil
}

// lastSyncedNotes renders the recorded base of each non-local area for
// `sync scopes`: area -> "snapshot N", and the unmerged paths per area.
func lastSyncedNotes(cfg config.Config, repoRoot string) (map[string]string, map[string][]string) {
	versions := map[string]string{}
	unmerged := map[string][]string{}
	server := resolveServer("")
	if server == "" {
		return versions, unmerged
	}
	project, err := resolveBoundProject(cfg, repoRoot)
	if err != nil {
		return versions, unmerged
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		abs = repoRoot
	}
	for _, area := range append(append([]string{}, config.ConfigAreas...), "documents") {
		b, ok, err := hosted.LoadAreaBase(server, project, abs, area)
		if err != nil || !ok {
			continue
		}
		versions[area] = fmt.Sprintf("last synced snapshot %d", b.Version)
		for p := range b.Unmerged {
			unmerged[area] = append(unmerged[area], p)
		}
		sort.Strings(unmerged[area])
	}
	return versions, unmerged
}
