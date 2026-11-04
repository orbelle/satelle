//go:build integration

package tests

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/structure"
)

// fenceCase is one golden (stdin, wantExit, wantStdoutSub) for a coded-check skill.
type fenceCase struct {
	name  string
	setup func(t *testing.T, repo string) // optional prep (git, files)
	stdin string                          // if empty, built from sid via defaultStoryPayload
	sid   string                          // story id for payload + commits
	// pathPrefix is prepended to PATH so a case can shim a binary the check
	// shells out to (e.g. a `satelle story diff` stub for the recorded channel).
	// Relative to the case's repo dir.
	pathPrefix string
	wantExit   int
	wantStdout string
	// wantAlso are further substrings the stdout must carry (a case that asserts
	// several facts about one verdict, e.g. the ids read AND the ratio).
	wantAlso []string
}

// fenceFixtures maps every embedded skill that carries a ```check fence to its
// golden table. Discovery (TestEveryCheckFenceHasFixtures) fails if a skill has
// a fence but no entry here (sty_6830e78e AC3).
var fenceFixtures = map[string][]fenceCase{
	"satelle-panel-all-accept": {
		{
			name:     "accept when every declared seat accepts",
			stdin:    `{"seats":[{"seat":"seat-a","accept":true,"unavailable":false,"notes":""},{"seat":"seat-b","accept":true,"unavailable":false,"notes":""}],"declared":2}`,
			wantExit: 0,
		},
		{
			name:       "reject a dissenting seat",
			stdin:      `{"seats":[{"seat":"seat-a","accept":true,"unavailable":false,"notes":""},{"seat":"seat-b","accept":false,"unavailable":false,"notes":"no"}],"declared":2}`,
			wantExit:   1,
			wantStdout: "seat-b",
		},
		{
			name:       "reject an unavailable seat without shrinking the panel",
			stdin:      `{"seats":[{"seat":"seat-a","accept":true,"unavailable":false,"notes":""},{"seat":"seat-b","accept":false,"unavailable":true,"notes":"missing"}],"declared":2}`,
			wantExit:   1,
			wantStdout: "unavailable: seat-b",
		},
	},
	"satelle-panel-majority": {
		{
			name:     "accept a strict majority of declared",
			stdin:    `{"seats":[{"seat":"seat-a","accept":true,"unavailable":false,"notes":""},{"seat":"seat-b","accept":true,"unavailable":false,"notes":""},{"seat":"seat-c","accept":false,"unavailable":false,"notes":"no"}],"declared":3}`,
			wantExit: 0,
		},
		{
			name:       "reject a 2-2 split",
			stdin:      `{"seats":[{"seat":"seat-a","accept":true,"unavailable":false,"notes":""},{"seat":"seat-b","accept":true,"unavailable":false,"notes":""},{"seat":"seat-c","accept":false,"unavailable":false,"notes":"no"},{"seat":"seat-d","accept":false,"unavailable":false,"notes":"no"}],"declared":4}`,
			wantExit:   1,
			wantStdout: "dissent: seat-c",
		},
		{
			name:       "unavailable stays in the denominator",
			stdin:      `{"seats":[{"seat":"seat-a","accept":true,"unavailable":false,"notes":""},{"seat":"seat-b","accept":false,"unavailable":true,"notes":"missing"}],"declared":2}`,
			wantExit:   1,
			wantStdout: "unavailable: seat-b",
		},
	},
	"satelle-estimate-actual-review": {
		{
			name:       "accept enter in_progress with estimate tags",
			stdin:      `{"story":{"id":"sty_est00001","tags":["estimate-minutes:10","estimate-tokens:1000"]},"from":"backlog","to":"in_progress"}`,
			wantExit:   0,
			wantStdout: "",
		},
		{
			name:       "reject enter in_progress without estimate",
			stdin:      `{"story":{"id":"sty_est00002","tags":[]},"from":"backlog","to":"in_progress"}`,
			wantExit:   1,
			wantStdout: "no plan estimate",
			// AC6: the driver is told to run comparables BEFORE recording, and
			// the gate is named as the enforcement.
			wantAlso: []string{
				"run: satelle story comparables <id>, then satelle story estimate <id>",
				"satelle-estimate-actual-review enforces it",
			},
		},
		{
			name:       "accept an empty story entering in_progress with no estimate",
			stdin:      `{"story":{"id":"sty_est00030","category":"empty","tags":[]},"from":"plan","to":"in_progress"}`,
			wantExit:   0,
			wantStdout: "category empty: no estimate required",
		},
		{
			name:       "reject a feature story entering in_progress with no estimate",
			stdin:      `{"story":{"id":"sty_est00031","category":"feature","tags":[]},"from":"plan","to":"in_progress"}`,
			wantExit:   1,
			wantStdout: "no plan estimate",
		},
		{
			// A dollar tag is not an estimate, even beside comparables.
			name:       "reject an estimate-usd-only story",
			stdin:      `{"story":{"id":"sty_est00010","tags":["estimate-usd:25"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup:      comparablesShim(false, comparableRow{"sty_c1", 585123, 0}),
			wantExit:   1,
			wantStdout: "no plan estimate",
		},
		{
			// 250k against ~600k measured output is the sty_b8542a3a shape: the
			// gate must reject, naming the comparables, the ratio and the bound.
			name:       "reject an output estimate below the comparables' spread",
			stdin:      `{"story":{"id":"sty_est00011","tags":["estimate-output:250000","epic:x"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup:      comparablesShim(false, comparableRow{"sty_c1", 585123, 0}, comparableRow{"sty_c2", 600000, 0}, comparableRow{"sty_c3", 700000, 0}),
			wantExit:   1,
			wantStdout: "REJECT output",
			wantAlso: []string{
				"sty_c1", "sty_c2", "sty_c3",
				"ratio estimate/median-actual 0.42",
				"lowest comparable sty_c1 (output 585123)",
			},
		},
		{
			name:       "reject an output estimate above the comparables' spread",
			stdin:      `{"story":{"id":"sty_est00012","tags":["estimate-output:2000000"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup:      comparablesShim(false, comparableRow{"sty_c1", 585123, 0}, comparableRow{"sty_c2", 600000, 0}, comparableRow{"sty_c3", 700000, 0}),
			wantExit:   1,
			wantStdout: "REJECT output",
			wantAlso:   []string{"above the spread", "highest comparable sty_c3 (output 700000)"},
		},
		{
			name:       "accept an output estimate inside the spread, recording ids and ratio",
			stdin:      `{"story":{"id":"sty_est00013","tags":["estimate-output:500000"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup:      comparablesShim(false, comparableRow{"sty_c1", 585123, 0}, comparableRow{"sty_c2", 600000, 0}, comparableRow{"sty_c3", 700000, 0}),
			wantExit:   0,
			wantStdout: "ratio estimate/median-actual 0.83",
			wantAlso:   []string{"read 3 comparable(s)", "sty_c1", "sty_c2", "sty_c3", "within tolerance"},
		},
		{
			name:       "accept with a note when too few comparables carry the figure",
			stdin:      `{"story":{"id":"sty_est00014","tags":["estimate-output:250000"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup:      comparablesShim(false, comparableRow{"sty_c1", 585123, 0}),
			wantExit:   0,
			wantStdout: "no comparison available for output: 1 comparable(s) carry it, minimum 3",
		},
		{
			name:       "fresh-input with too few comparables is a note while output is still judged",
			stdin:      `{"story":{"id":"sty_est00015","tags":["estimate-fresh-input:20000","estimate-output:500000"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup:      comparablesShim(false, comparableRow{"sty_c1", 585123, 9000}, comparableRow{"sty_c2", 600000, 0}, comparableRow{"sty_c3", 700000, 0}),
			wantExit:   0,
			wantStdout: "no comparison available for fresh-input",
			wantAlso:   []string{"output: estimate 500000 vs 3 comparable(s)"},
		},
		{
			name:       "accept with a note when comparables cannot be read",
			stdin:      `{"story":{"id":"sty_est00016","tags":["estimate-output:250000"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup:      comparablesShim(true),
			wantExit:   0,
			wantStdout: "no comparison available: satelle story comparables could not be read",
		},
		{
			name:       "legacy unit is noted, not compared",
			stdin:      `{"story":{"id":"sty_est00017","tags":["estimate-tokens:5000"]},"from":"plan","to":"in_progress"}`,
			wantExit:   0,
			wantStdout: "legacy unit — no comparison available",
		},
		{
			// AC4: nothing configured → the documented defaults apply and are named.
			name:       "default tolerance and minimum apply when the table is absent",
			stdin:      `{"story":{"id":"sty_est00018","tags":["estimate-output:500000"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup:      comparablesShim(false, comparableRow{"sty_c1", 585123, 0}, comparableRow{"sty_c2", 600000, 0}, comparableRow{"sty_c3", 700000, 0}),
			wantExit:   0,
			wantStdout: "tolerance 1.5 (default), min_comparables 3 (default)",
		},
		{
			// AC4: the configured tolerance is what accepts 250k here — at the
			// default it rejects (see the case above).
			name:       "configured tolerance widens the accepted spread",
			stdin:      `{"story":{"id":"sty_est00019","tags":["estimate-output:250000"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup: withEstimateConfig("[estimate_review]\ntolerance = 3.0  # wide\nmin_comparables = 2\n",
				comparablesShim(false, comparableRow{"sty_c1", 585123, 0}, comparableRow{"sty_c2", 600000, 0}, comparableRow{"sty_c3", 700000, 0})),
			wantExit:   0,
			wantStdout: "tolerance 3.0 (configured), min_comparables 2 (configured)",
		},
		{
			// AC4: a configured minimum above the comparables on hand means no
			// comparison — an out-of-spread estimate is then accepted with a note.
			name:       "configured minimum comparables suppresses the comparison",
			stdin:      `{"story":{"id":"sty_est00020","tags":["estimate-output:250000"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup: withEstimateConfig("[estimate_review]\nmin_comparables = 5\n",
				comparablesShim(false, comparableRow{"sty_c1", 585123, 0}, comparableRow{"sty_c2", 600000, 0}, comparableRow{"sty_c3", 700000, 0})),
			wantExit:   0,
			wantStdout: "no comparison available for output: 3 comparable(s) carry it, minimum 5",
		},
		{
			name:       "reject a tolerance below 1",
			stdin:      `{"story":{"id":"sty_est00021","tags":["estimate-output:250000"]},"from":"plan","to":"in_progress"}`,
			pathPrefix: "bin",
			setup: withEstimateConfig("[estimate_review]\ntolerance = 0.5\n",
				comparablesShim(false, comparableRow{"sty_c1", 585123, 0})),
			wantExit:   1,
			wantStdout: "tolerance must be a number >= 1",
		},
		{
			name:       "accept enter done with actual tags",
			stdin:      `{"story":{"id":"sty_est00003","tags":["actual-minutes:10","actual-tokens:900"]},"from":"release","to":"done"}`,
			wantExit:   0,
			wantStdout: "",
		},
		{
			// sty_8eae81ac: the actual is computed from the ledger and written
			// when the terminal transition commits, so the shipped check never
			// rejects for a missing or large actual; with no measured_actual in
			// the payload it says so rather than printing zeros.
			name:       "accept enter done without actual, saying measured actual unavailable",
			stdin:      `{"story":{"id":"sty_est00004","tags":["estimate-minutes:10"]},"from":"release","to":"done"}`,
			wantExit:   0,
			wantStdout: "measured actual unavailable",
		},
		{
			name:     "untargeted edge (cancelled) is n/a accept",
			stdin:    `{"story":{"id":"sty_est00005","tags":[]},"from":"backlog","to":"cancelled"}`,
			wantExit: 0,
		},
	},
	"satelle-task-validate-before-review": {
		{
			name: "accept when parent task header exists",
			setup: func(t *testing.T, repo string) {
				mustWrite(t, filepath.Join(repo, ".satelle", "tasks", "tsk_fixture1.md"),
					"---\nid: tsk_fixture1\ntype: task\nstatus: done\n---\n\n# Fixture task\n")
			},
			stdin:    `{"story":{"id":"exe_aaa11111","parent_id":"tsk_fixture1","kind":"execution"},"from":"backlog","to":"in_progress"}`,
			wantExit: 0,
		},
		{
			name:       "reject when parent_id missing",
			stdin:      `{"story":{"id":"exe_bbb22222","kind":"execution"},"from":"backlog","to":"in_progress"}`,
			wantExit:   1,
			wantStdout: "no parent task",
		},
		{
			name:       "reject when parent file missing",
			stdin:      `{"story":{"id":"exe_ccc33333","parent_id":"tsk_missing1","kind":"execution"},"from":"backlog","to":"in_progress"}`,
			wantExit:   1,
			wantStdout: "does not exist",
		},
		{
			name: "reject missing frontmatter",
			setup: func(t *testing.T, repo string) {
				mustWrite(t, filepath.Join(repo, ".satelle", "tasks", "tsk_nofm.md"), "# No frontmatter\n")
			},
			stdin:      `{"story":{"id":"exe_eee55555","parent_id":"tsk_nofm","kind":"execution"},"from":"backlog","to":"in_progress"}`,
			wantExit:   1,
			wantStdout: "frontmatter",
		},
		{
			name: "reject missing id field",
			setup: func(t *testing.T, repo string) {
				mustWrite(t, filepath.Join(repo, ".satelle", "tasks", "tsk_noid.md"),
					"---\ntype: task\nstatus: done\n---\n\n# No id\n")
			},
			stdin:      `{"story":{"id":"exe_fff66666","parent_id":"tsk_noid","kind":"execution"},"from":"backlog","to":"in_progress"}`,
			wantExit:   1,
			wantStdout: "missing id",
		},
		{
			name: "reject wrong type",
			setup: func(t *testing.T, repo string) {
				mustWrite(t, filepath.Join(repo, ".satelle", "tasks", "tsk_badtype.md"),
					"---\nid: tsk_badtype\ntype: story\nstatus: done\n---\n\n# Bad type\n")
			},
			stdin:      `{"story":{"id":"exe_ggg77777","parent_id":"tsk_badtype","kind":"execution"},"from":"backlog","to":"in_progress"}`,
			wantExit:   1,
			wantStdout: "type: task",
		},
		{
			name: "reject missing status",
			setup: func(t *testing.T, repo string) {
				mustWrite(t, filepath.Join(repo, ".satelle", "tasks", "tsk_nostatus.md"),
					"---\nid: tsk_nostatus\ntype: task\n---\n\n# No status\n")
			},
			stdin:      `{"story":{"id":"exe_hhh88888","parent_id":"tsk_nostatus","kind":"execution"},"from":"backlog","to":"in_progress"}`,
			wantExit:   1,
			wantStdout: "missing status",
		},
		{
			name: "reject missing title heading",
			setup: func(t *testing.T, repo string) {
				mustWrite(t, filepath.Join(repo, ".satelle", "tasks", "tsk_notitle.md"),
					"---\nid: tsk_notitle\ntype: task\nstatus: done\n---\n\nNo heading here.\n")
			},
			stdin:      `{"story":{"id":"exe_iii99999","parent_id":"tsk_notitle","kind":"execution"},"from":"backlog","to":"in_progress"}`,
			wantExit:   1,
			wantStdout: "Title",
		},
		{
			name:     "non-in_progress edge is n/a accept",
			stdin:    `{"story":{"id":"exe_ddd44444"},"from":"in_progress","to":"done"}`,
			wantExit: 0,
		},
	},
	"satelle-substrate-pushed-check": {
		{
			// `satelle sync status --story` exits 0: every touched area is in step
			// (or behind), or the story touched no synced substrate.
			name:       "accepts when sync status --story exits zero",
			sid:        "sty_a1b2c3d4",
			pathPrefix: "bin",
			setup:      syncStatusShim(0, "skills in step"),
			wantExit:   0,
			wantStdout: "skills in step",
			wantAlso:   []string{"sync status --story sty_a1b2c3d4"},
		},
		{
			name:       "rejects when sync status --story exits non-zero, carrying its output",
			sid:        "sty_a1b2c3d4",
			pathPrefix: "bin",
			setup:      syncStatusShim(1, "skills unpushed — run satelle sync"),
			wantExit:   1,
			wantStdout: "skills unpushed — run satelle sync",
			wantAlso:   []string{"sty_a1b2c3d4 changed substrate that is not on the hosted copy"},
		},
		{
			name:       "rejects a payload that names no story",
			stdin:      `{"from":"in_progress","to":"done"}`,
			pathPrefix: "bin",
			setup:      syncStatusShim(0, "unused"),
			wantExit:   1,
			wantStdout: "could not read the story id",
		},
	},
	"satelle-route-drift-check": {
		{
			// The common path: no route_drift block in the payload, so there is
			// nothing to judge and the gate must not cost a rejection.
			name:     "accepts a payload with no drift block",
			stdin:    `{"story":{"id":"sty_nodrift1"},"from":"plan","to":"in_progress"}`,
			wantExit: 0,
		},
		{
			name:       "rejects a drifted payload naming both lanes",
			stdin:      `{"story":{"id":"sty_drift001"},"from":"in_progress","to":"done","route_drift":{"item":"sty_drift001","category":"docs","status":"in_progress","walked":["backlog","plan","in_progress"],"derived":["backlog","in_progress","done"],"off_route":["plan"],"status_on_route":true}}`,
			wantExit:   1,
			wantStdout: "route drift",
		},
	},
	"satelle-instruction-change-trigger": {
		{
			// The gate FIRES: a real rewrite of instruction text past the word
			// threshold. `satelle story diff` is shimmed because the recorded and
			// live channels are what name the changed files; the threshold itself
			// is the shipped default (THRESHOLD=5).
			name:       "fires on an instruction rewrite past the word threshold",
			sid:        "sty_1c700001",
			pathPrefix: "bin",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				mustWrite(t, filepath.Join(repo, ".satelle", "skills", "satelle-example.md"),
					"one\n")
				gitCommitAll(t, repo, "baseline")
				// The baseline the shim names must be a REAL commit, or the check
				// takes its "new or untracked instruction file" path instead of
				// measuring the diff. The root commit is the baseline here.
				mustWrite(t, filepath.Join(repo, "bin", "satelle"), `#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = "--include-substrate" ]; then
    printf '{"files":[".satelle/skills/satelle-example.md"],"baseline_sha":"%s"}\n' "$(git rev-list --max-parents=0 HEAD)"
    exit 0
  fi
done
echo '{"files":[".satelle/skills/satelle-example.md"]}'
`)
				if err := os.Chmod(filepath.Join(repo, "bin", "satelle"), 0o755); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, filepath.Join(repo, ".satelle", "skills", "satelle-example.md"),
					"rewrite the whole paragraph so the word count clears the threshold easily\n")
				gitCommitAll(t, repo, "rewrite an instruction skill (sty_1c700001)")
			},
			wantExit:   0,
			wantStdout: "instruction text changed",
		},
		{
			// The gate is SKIPPED: nothing under the instruction paths moved. This
			// is the common case — most slices touch no skill, principle or
			// constitution file, and the trigger must stay quiet on all of them.
			name: "skips a slice that touches no instruction file",
			sid:  "sty_1c700002",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
				mustWrite(t, filepath.Join(repo, "cmd", "foo.go"), "package main\n")
				gitCommitAll(t, repo, "a code-only slice (sty_1c700002)")
			},
			wantExit:   1,
			wantStdout: "no instruction file changed",
		},
	},
	"satelle-docs-only-check": {
		{
			name: "accepts a markdown-only slice",
			sid:  "sty_d0c11111",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n\n## Retry policy\n")
				mustWrite(t, filepath.Join(repo, "docs", "guide.md"), "# Guide\n")
				gitCommitAll(t, repo, "document the retry policy (sty_d0c11111)")
			},
			wantExit:   0,
			wantStdout: "docs-only slice confirmed",
		},
		{
			name: "rejects a non-doc path, naming the offender",
			sid:  "sty_d0c22222",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n\n## Config\n")
				mustWrite(t, filepath.Join(repo, "cmd", "foo.go"), "package main\n")
				gitCommitAll(t, repo, "docs plus code (sty_d0c22222)")
			},
			wantExit:   1,
			wantStdout: "cmd/foo.go",
		},
		{
			// The lane's whole file-type scope is the doc_paths pattern: a
			// non-markdown doc form is out until a repo widens it by overriding
			// this skill — the pattern is configuration, not a Go branch.
			name: "rejects a non-markdown doc form under the shipped default",
			sid:  "sty_d0c33333",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
				mustWrite(t, filepath.Join(repo, "docs", "guide.rst"), "Guide\n=====\n")
				gitCommitAll(t, repo, "rst guide (sty_d0c33333)")
			},
			wantExit:   1,
			wantStdout: "docs/guide.rst",
		},
		{
			// sty_8221c090: story B's commit cites story A in its BODY. Only a
			// SUBJECT match owns a commit, so A's slice stays its own prose.
			name: "body citation of another story does not attribute that commit",
			sid:  "sty_d0c55555",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
				mustWrite(t, filepath.Join(repo, "docs", "guide.md"), "# Guide\n")
				gitCommitAll(t, repo, "document the lane (sty_d0c55555)")
				mustWrite(t, filepath.Join(repo, "cmd", "foo.go"), "package main\n")
				gitCommitAll(t, repo, "code change (sty_d0c66666)\n\nExists because sty_d0c55555 needed it.")
			},
			wantExit:   0,
			wantStdout: "docs-only slice confirmed",
		},
		{
			name: "rejects an empty change set",
			sid:  "sty_d0c44444",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
			},
			wantExit:   1,
			wantStdout: "no change set found",
		},
		{
			// sty_03414f12: prose delivered outside the repo changes no repo
			// path; the recorded external-change document is the evidence.
			name:       "accepts an external-change document with no repo change",
			sid:        "sty_d0c77777",
			pathPrefix: "bin",
			setup: func(t *testing.T, repo string) {
				emptyRepoWithBaseline(t, repo)
				externalDocShim(t, repo, "sty_d0c77777", "external-change", "page 1265532932 v10 — restructured")
			},
			wantExit:   0,
			wantStdout: "external document evidence",
		},
		{
			name:       "rejects an external-change document with an empty body",
			sid:        "sty_d0c88888",
			pathPrefix: "bin",
			setup: func(t *testing.T, repo string) {
				emptyRepoWithBaseline(t, repo)
				externalDocShim(t, repo, "sty_d0c88888", "external-change", "  ")
			},
			wantExit:   1,
			wantStdout: "--type external-change",
		},
		{
			name:       "rejects an attachment of another type",
			sid:        "sty_d0c99999",
			pathPrefix: "bin",
			setup: func(t *testing.T, repo string) {
				emptyRepoWithBaseline(t, repo)
				externalDocShim(t, repo, "sty_d0c99999", "output", "page 1265532932 v10")
			},
			wantExit:   1,
			wantStdout: "--type external-change",
		},
		{
			// Evidence never excuses a non-doc repo path.
			name:       "rejects a non-doc path even with an external-change document",
			sid:        "sty_d0caaaaa",
			pathPrefix: "bin",
			setup: func(t *testing.T, repo string) {
				emptyRepoWithBaseline(t, repo)
				mustWrite(t, filepath.Join(repo, "cmd", "foo.go"), "package main\n")
				gitCommitAll(t, repo, "docs plus code (sty_d0caaaaa)")
				// After the commit, so the shim is not part of the change set.
				externalDocShim(t, repo, "sty_d0caaaaa", "external-change", "page 1265532932 v10")
			},
			wantExit:   1,
			wantStdout: "cmd/foo.go",
		},
	},
	"satelle-agents-layer-only-check": {
		{
			name: "accepts an agents-layer-only slice",
			sid:  "sty_a9e11111",
			setup: func(t *testing.T, repo string) {
				emptyRepoWithBaseline(t, repo)
				mustWrite(t, filepath.Join(repo, ".satelle", "workflows", "agents.local.toml"), "[planner]\ntools = \"Read\"\n")
				mustWrite(t, filepath.Join(repo, ".satelle", "workflows", "agents.toml"), "[reviewer]\ntools = \"Read\"\n")
				gitCommitAll(t, repo, "restate the bindings (sty_a9e11111)")
			},
			wantExit:   0,
			wantStdout: "agents-layer-only slice confirmed",
		},
		{
			name: "rejects other substrate beside the bindings, naming it",
			sid:  "sty_a9e22222",
			setup: func(t *testing.T, repo string) {
				emptyRepoWithBaseline(t, repo)
				mustWrite(t, filepath.Join(repo, ".satelle", "workflows", "agents.local.toml"), "[planner]\ntools = \"Read\"\n")
				mustWrite(t, filepath.Join(repo, ".satelle", "workflows", "step.toml"), "[raised]\n")
				gitCommitAll(t, repo, "bindings plus a step (sty_a9e22222)")
			},
			wantExit:   1,
			wantStdout: ".satelle/workflows/step.toml",
		},
		{
			name: "rejects a nested agents.toml and code",
			sid:  "sty_a9e33333",
			setup: func(t *testing.T, repo string) {
				emptyRepoWithBaseline(t, repo)
				mustWrite(t, filepath.Join(repo, ".satelle", "workflows", "x", "agents.toml"), "[planner]\n")
				mustWrite(t, filepath.Join(repo, "cmd", "foo.go"), "package main\n")
				gitCommitAll(t, repo, "not the agents layer (sty_a9e33333)")
			},
			wantExit:   1,
			wantStdout: ".satelle/workflows/x/agents.toml",
		},
		{
			name: "rejects an empty change set",
			sid:  "sty_a9e44444",
			setup: func(t *testing.T, repo string) {
				emptyRepoWithBaseline(t, repo)
			},
			wantExit:   1,
			wantStdout: "no change set found",
		},
		{
			name:       "rejects a payload that names no story",
			stdin:      `{"from":"in_progress","to":"done"}`,
			wantExit:   1,
			wantStdout: "could not read the story id",
		},
	},
	"satelle-substrate-only-check": {
		{
			name: "accepts managed footprint",
			sid:  "sty_aaa11111",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
				mustWrite(t, filepath.Join(repo, ".gitignore"), "# satelle managed\n.satelle/data/\n")
				mustWrite(t, filepath.Join(repo, ".claude", "settings.json"), "{}\n")
				mustWrite(t, filepath.Join(repo, ".grok", "hooks", "satelle.json"), "{}\n")
				mustWrite(t, filepath.Join(repo, ".satelle", "x.md"), "# substrate\n")
				gitCommitAll(t, repo, "init footprint (sty_aaa11111)")
			},
			wantExit:   0,
			wantStdout: "substrate-only slice confirmed",
		},
		{
			// sty_30d3bd99 AC2: a prune slice is deletions of git-ignored
			// .satelle/ files, so git sees nothing and there is no commit — the
			// recorded channel is the ONLY evidence. The gate must accept it
			// rather than reporting "no change set found".
			name:       "accepts a deletion-only prune slice from the recorded channel",
			sid:        "sty_44be0001",
			pathPrefix: "bin",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
				// The removed seeds are gone from disk and were never tracked.
				mustWrite(t, filepath.Join(repo, "bin", "satelle"), `#!/usr/bin/env bash
for a in "$@"; do
  if [ "$a" = "--recorded" ]; then
    echo '{"files":[".satelle/principles/satelle-agent-goals.md",".satelle/principles/satelle-agent-model.md"]}'
    exit 0
  fi
done
echo '{"files":[]}'
`)
				if err := os.Chmod(filepath.Join(repo, "bin", "satelle"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantExit:   0,
			wantStdout: "substrate-only slice confirmed",
		},
		{
			name: "rejects code slice naming offenders",
			sid:  "sty_bbb22222",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
				mustWrite(t, filepath.Join(repo, "cmd", "foo.go"), "package main\n")
				mustWrite(t, filepath.Join(repo, "Makefile"), "all:\n")
				gitCommitAll(t, repo, "code change (sty_bbb22222)")
			},
			wantExit:   1,
			wantStdout: "cmd/foo.go",
		},
		{
			name: "honors edit_exempt_paths extension",
			sid:  "sty_ccc33333",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				mustWrite(t, filepath.Join(repo, ".satelle", "satelle.toml"),
					"[gate]\nedit_exempt_paths = [\"build/\"]\n")
				gitCommitAll(t, repo, "baseline with exempt config")
				mustWrite(t, filepath.Join(repo, "build", "x.txt"), "artifact\n")
				gitCommitAll(t, repo, "build artifact (sty_ccc33333)")
			},
			wantExit:   0,
			wantStdout: "substrate-only slice confirmed",
		},
		{
			name: "fresh-init default exempts gitignore slice",
			sid:  "sty_ddd44444",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				mustWrite(t, filepath.Join(repo, ".satelle", "satelle.toml"),
					"[gate]\nedit_exempt_paths = [\".satelle/\", \".gitignore\"]\n")
				gitCommitAll(t, repo, "baseline with fresh-init exempt")
				mustWrite(t, filepath.Join(repo, ".gitignore"), "# managed\n.satelle/local.toml\n")
				gitCommitAll(t, repo, "gitignore converge (sty_ddd44444)")
			},
			wantExit:   0,
			wantStdout: "substrate-only slice confirmed",
		},
		{
			// sty_8221c090 — the d7bf17d shape: story B's commit names B in its
			// SUBJECT and cites A in its BODY while touching a product path. A
			// whole-message `git log --grep` handed A that file and rejected A's
			// close. Only a subject match owns a commit, so A still accepts.
			name: "body citation of another story does not attribute that commit",
			sid:  "sty_fff66666",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
				mustWrite(t, filepath.Join(repo, ".satelle", "skills", "a.md"), "# A\n")
				gitCommitAll(t, repo, "substrate change (sty_fff66666)")
				mustWrite(t, filepath.Join(repo, "cmd", "foo.go"), "package main\n")
				gitCommitAll(t, repo, "code change (sty_ggg77777)\n\nExists because sty_fff66666 needed it.")
			},
			wantExit:   0,
			wantStdout: "substrate-only slice confirmed",
		},
		{
			// AC3: empty commit alone is not evidence of a substrate change.
			name: "rejects empty commit as sole evidence",
			sid:  "sty_eee55555",
			setup: func(t *testing.T, repo string) {
				gitInit(t, repo)
				mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
				gitCommitAll(t, repo, "baseline")
				cmd := exec.Command("git", "commit", "--allow-empty", "-q", "-m", "empty (sty_eee55555)")
				cmd.Dir = repo
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("empty commit: %v\n%s", err, out)
				}
			},
			wantExit:   1,
			wantStdout: "no change set found",
		},
	},
}

// TestEveryCheckFenceHasFixtures discovers embedded skills with a ```check fence
// and requires each to have a fixture table covering accept + reject.
func TestEveryCheckFenceHasFixtures(t *testing.T) {
	var missing []string
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind != "skills" {
			continue
		}
		if structure.CheckFence(d.Body) == "" {
			continue
		}
		cases, ok := fenceFixtures[d.Name]
		if !ok || len(cases) == 0 {
			missing = append(missing, d.Name)
			continue
		}
		hasAccept, hasReject := false, false
		for _, c := range cases {
			if c.wantExit == 0 {
				hasAccept = true
			} else {
				hasReject = true
			}
		}
		if !hasAccept || !hasReject {
			t.Errorf("skill %s fixtures must cover at least one accept and one reject", d.Name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("embedded skills with ```check but no fenceFixtures entry: %v", missing)
	}
	// reverse: no fixture for a skill that lost its fence
	for name := range fenceFixtures {
		found := false
		for _, d := range config.EmbeddedDefaults() {
			if d.Kind == "skills" && d.Name == name && structure.CheckFence(d.Body) != "" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("fenceFixtures has %s but no embedded skill carries a ```check fence", name)
		}
	}
}

// TestCheckFenceGoldenTables drives each fixture through the shipped fence.
func TestCheckFenceGoldenTables(t *testing.T) {
	for _, d := range config.EmbeddedDefaults() {
		if d.Kind != "skills" {
			continue
		}
		script := structure.CheckFence(d.Body)
		if script == "" {
			continue
		}
		cases, ok := fenceFixtures[d.Name]
		if !ok {
			continue // discovery test already fails
		}
		t.Run(d.Name, func(t *testing.T) {
			for _, tc := range cases {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					repo := t.TempDir()
					if tc.setup != nil {
						tc.setup(t, repo)
					}
					scriptPath := filepath.Join(t.TempDir(), "check.sh")
					if err := os.WriteFile(scriptPath, []byte(script+"\n"), 0o755); err != nil {
						t.Fatal(err)
					}
					stdin := tc.stdin
					if stdin == "" {
						sid := tc.sid
						if sid == "" {
							sid = "sty_eee55555"
						}
						stdin = `{"story":{"id":"` + sid + `"},"from":"in_progress","to":"done"}`
					}
					out, exit := runFenceScript(t, scriptPath, repo, stdin, tc.pathPrefix)
					if exit != tc.wantExit {
						t.Fatalf("exit=%d want %d\nout:\n%s", exit, tc.wantExit, out)
					}
					if tc.wantStdout != "" && !strings.Contains(out, tc.wantStdout) {
						t.Errorf("stdout missing %q:\n%s", tc.wantStdout, out)
					}
					for _, also := range tc.wantAlso {
						if !strings.Contains(out, also) {
							t.Errorf("stdout missing %q:\n%s", also, out)
						}
					}
				})
			}
		})
	}
}

// emptyRepoWithBaseline is a git repo with one baseline commit and nothing else.
// The shim directory is untracked, so it never joins the change set.
func emptyRepoWithBaseline(t *testing.T, repo string) {
	t.Helper()
	gitInit(t, repo)
	mustWrite(t, filepath.Join(repo, "README.md"), "baseline\n")
	gitCommitAll(t, repo, "baseline")
}

// externalDocShim writes a bin/satelle stub that reports an empty story diff and
// one attached document of docType with the given body, in the JSON shapes
// `satelle story docs --json` and `satelle story doc` print.
func externalDocShim(t *testing.T, repo, sid, docType, body string) {
	t.Helper()
	docs := `[{"story_id":"` + sid + `","name":"evidence","type":"` + docType + `"}]`
	doc := `{"story_id":"` + sid + `","name":"evidence","type":"` + docType +
		`","body":"---\nstory: ` + sid + `\ntype: ` + docType + `\nname: evidence\n---\n\n` + body + `"}`
	script := "#!/usr/bin/env bash\n" +
		"case \"$1 $2\" in\n" +
		"  \"story docs\") cat <<'J'\n" + docs + "\nJ\n    ;;\n" +
		"  \"story doc\") cat <<'J'\n" + doc + "\nJ\n    ;;\n" +
		"  *) echo '{\"files\":[]}' ;;\n" +
		"esac\n"
	shim := filepath.Join(repo, "bin", "satelle")
	mustWrite(t, shim, script)
	if err := os.Chmod(shim, 0o755); err != nil {
		t.Fatal(err)
	}
}

// comparableRow is one comparable in a `satelle story comparables --json` stub:
// id plus its measured output and fresh-input actuals (0 omits the figure, as
// the verb omits an unmeasured one).
type comparableRow struct {
	id            string
	output, fresh int
}

// comparablesShim writes a bin/satelle stub that answers `story comparables`
// with rows in the JSON shape the verb prints; exit1 makes it fail instead.
func comparablesShim(exit1 bool, rows ...comparableRow) func(*testing.T, string) {
	return func(t *testing.T, repo string) {
		t.Helper()
		var items []string
		for _, r := range rows {
			var figs []string
			if r.fresh > 0 {
				figs = append(figs, fmt.Sprintf(`"fresh_input":%d`, r.fresh))
			}
			if r.output > 0 {
				figs = append(figs, fmt.Sprintf(`"output":%d`, r.output))
			}
			items = append(items, fmt.Sprintf(`{"id":"%s","status":"done","category":"fix","shared_tags":1,"actuals":{%s}}`,
				r.id, strings.Join(figs, ",")))
		}
		body := `{"story_id":"x","comparables":[` + strings.Join(items, ",") + `]}`
		script := "#!/usr/bin/env bash\n"
		if exit1 {
			script += "exit 1\n"
		} else {
			script += "if [ \"$1 $2\" = \"story comparables\" ]; then cat <<'J'\n" + body + "\nJ\nfi\n"
		}
		shim := filepath.Join(repo, "bin", "satelle")
		mustWrite(t, shim, script)
		if err := os.Chmod(shim, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// syncStatusShim writes a bin/satelle stub standing in for the mechanism the
// substrate-pushed check delegates to: it echoes its arguments and out, then
// exits with exit when called as `sync status --story <id>`.
func syncStatusShim(exit int, out string) func(*testing.T, string) {
	return func(t *testing.T, repo string) {
		t.Helper()
		script := "#!/usr/bin/env bash\n" +
			"if [ \"$1 $2 $3\" != \"sync status --story\" ]; then echo \"unexpected: $*\"; exit 99; fi\n" +
			"echo \"$*\"\necho '" + out + "'\nexit " + fmt.Sprint(exit) + "\n"
		shim := filepath.Join(repo, "bin", "satelle")
		mustWrite(t, shim, script)
		if err := os.Chmod(shim, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// withEstimateConfig composes setups: write .satelle/satelle.toml then shim.
func withEstimateConfig(toml string, then func(*testing.T, string)) func(*testing.T, string) {
	return func(t *testing.T, repo string) {
		t.Helper()
		mustWrite(t, filepath.Join(repo, ".satelle", "satelle.toml"), toml)
		then(t, repo)
	}
}

func runFenceScript(t *testing.T, scriptPath, repo, stdin, pathPrefix string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", scriptPath)
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(stdin)
	if pathPrefix != "" {
		cmd.Env = append(os.Environ(),
			"PATH="+filepath.Join(repo, pathPrefix)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("run fence: %v\n%s", err, buf.String())
		}
	}
	return buf.String(), exit
}
