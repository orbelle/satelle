//go:build integration

package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/upgrade"
)

func manifestOf(t *testing.T, dir string) []upgrade.Entry {
	t.Helper()
	m, err := upgrade.Manifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func sameEntries(a, b []upgrade.Entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func shaOf(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func ledgerFiles(t *testing.T, runtime string) map[string]string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(runtime, "ledger", "*.jsonl"))
	out := map[string]string{}
	for _, f := range files {
		out[filepath.Base(f)] = shaOf(t, f)
	}
	return out
}

func blobCount(runtime string) int {
	files, _ := filepath.Glob(filepath.Join(runtime, "blobs", "*"))
	return len(files)
}

const fullMap = `[status]
backlog = { workflow = "default", state = "backlog" }
done = { workflow = "default", state = "done" }
"task/active" = { workflow = "standing", state = "open" }
`

// The whole way across on a pre-1.0 fixture: validate names the upgrade,
// archive moves the old .satelle byte for byte and starts v1, the import
// refuses an incomplete map and dry-runs without writing, imports every item
// with its history and documents, verifies, and imports nothing twice.
func TestUpgradeEndToEnd(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	oldFormat(t, s)

	// Validate and every command name the upgrade.
	if r := s.run("validate"); r.code == 0 || !strings.Contains(r.out+r.err, "satelle upgrade --archive") {
		t.Fatalf("validate on a pre-1.0 repo: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if r := s.run("story", "list"); r.code == 0 || !strings.Contains(r.err, "satelle upgrade --archive") {
		t.Fatalf("story list on a pre-1.0 repo: exit %d %s", r.code, r.err)
	}

	// Archive: a dry run moves nothing; the run moves it byte for byte.
	data := filepath.Join(s.dir, ".satelle")
	before := manifestOf(t, data)
	runtime := strings.TrimSpace(s.must("runtime", "path"))
	s.must("upgrade", "--archive", "--dry-run")
	if !sameEntries(before, manifestOf(t, data)) {
		t.Fatal("the dry run changed .satelle")
	}
	if _, err := os.Stat(filepath.Join(runtime, "archive")); !os.IsNotExist(err) {
		t.Fatal("the dry run made an archive")
	}
	out := s.must("upgrade", "--archive")
	dirs, _ := filepath.Glob(filepath.Join(runtime, "archive", "*"))
	if len(dirs) != 1 || !regexp.MustCompile(`^\d{8}T\d{6}Z$`).MatchString(filepath.Base(dirs[0])) {
		t.Fatalf("archive dirs %v", dirs)
	}
	dest := filepath.Join(dirs[0], ".satelle")
	if !sameEntries(before, manifestOf(t, dest)) {
		t.Fatal("the archive differs from the old .satelle")
	}
	if !strings.Contains(out, dest) || !strings.Contains(out, "satelle help upgrade") {
		t.Errorf("archive printed:\n%s", out)
	}
	s.must("validate")

	// A workflow the map targets beyond init's default.
	s.write(".satelle/workflows/standing.toml", "name = \"standing\"\n[states.open]\nstart = true\n[states.closed]\nterminal = true\n[[transition]]\nfrom = [\"open\"]\nto = \"closed\"\n")
	s.must("validate")

	store := t.TempDir()
	oldStore(t, store)
	maps := t.TempDir()
	writeFile(t, filepath.Join(maps, "partial.toml"), "[status]\nbacklog = { workflow = \"default\", state = \"backlog\" }\n", 0o644)
	writeFile(t, filepath.Join(maps, "full.toml"), fullMap, 0o644)

	// An incomplete map refuses before anything is written.
	r := s.run("upgrade", "--from", store, "--map", filepath.Join(maps, "partial.toml"))
	if r.code == 0 || !strings.Contains(r.err, "story done: 1 item(s)") || !strings.Contains(r.err, "execution done: 1 item(s)") || !strings.Contains(r.err, "task active: 1 item(s)") {
		t.Fatalf("partial map: exit %d\n%s%s", r.code, r.out, r.err)
	}
	if len(ledgerFiles(t, runtime)) != 0 || blobCount(runtime) != 0 {
		t.Fatal("a refused import wrote to the ledger")
	}

	// A dry run prints the plan and writes nothing.
	out = s.must("upgrade", "--from", store, "--map", filepath.Join(maps, "full.toml"), "--dry-run")
	for _, want := range []string{"story backlog -> default/backlog  1 item(s)", "task active -> standing/open  1 item(s)", "4 item(s) to import", "4 history row(s), 2 document(s)", "1 history row(s) naming no item"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if len(ledgerFiles(t, runtime)) != 0 || blobCount(runtime) != 0 {
		t.Fatal("the dry run wrote to the ledger")
	}

	// The import.
	if out = s.must("upgrade", "--from", store, "--map", filepath.Join(maps, "full.toml")); !strings.Contains(out, "4 item(s) imported") {
		t.Fatalf("import printed:\n%s", out)
	}
	type got struct {
		ID, Kind, Title, Body, Status, Workflow, Category, Assignee string
		Criteria, Tags                                              []string
		Parent                                                      string `json:"parent"`
		CreatedAt                                                   string `json:"created_at"`
	}
	want := map[string][2]string{"sty_a1000001": {"default", "backlog"}, "sty_a1000002": {"default", "done"}, "tsk_t1000001": {"standing", "open"}, "exe_e1000001": {"default", "done"}}
	for _, row := range oldRows {
		var g got
		s.mustJSON(&g, row.kind, "get", row.id)
		if g.ID != row.id || g.Kind != row.kind || g.Title != row.title || g.Body != row.body || g.Category != row.category ||
			g.Assignee != row.assignee || g.Parent != row.parent || g.Workflow != want[row.id][0] || g.Status != want[row.id][1] {
			t.Errorf("%s = %+v", row.id, g)
		}
		if !strings.HasPrefix(g.CreatedAt, strings.TrimSuffix(row.created, "Z")) {
			t.Errorf("%s created_at %s, want %s", row.id, g.CreatedAt, row.created)
		}
	}
	var one got
	s.mustJSON(&one, "story", "get", "sty_a1000001")
	if strings.Join(one.Criteria, "|") != "first criterion|second\n   continued" || strings.Join(one.Tags, ",") != "area:x,k:v" {
		t.Errorf("story one criteria %q tags %q", one.Criteria, one.Tags)
	}
	mapped := s.ledger("sty_a1000001", upgrade.KindMapped)
	var mp upgrade.Mapped
	if len(mapped) != 1 || json.Unmarshal(mapped[0].Payload, &mp) != nil || mp.OldStatus != "backlog" || mp.State != "backlog" ||
		mp.Old["priority"] != "high" || mp.Old["park_origin"] != "x" || mp.MapSHA256 != shaOf(t, filepath.Join(maps, "full.toml")) {
		t.Errorf("upgrade.mapped = %+v", mp)
	}
	imported := s.ledger("sty_a1000001", upgrade.KindImported)
	if len(imported) != 4 {
		t.Fatalf("%d upgrade.imported events, want 4", len(imported))
	}
	for i, ev := range imported {
		var im upgrade.Imported
		if err := json.Unmarshal(ev.Payload, &im); err != nil {
			t.Fatal(err)
		}
		row := oldEvidence[i]
		if im.ID != row[0] || im.Kind != row[2] || im.Actor != row[3] || im.Body != row[4] {
			t.Errorf("imported %d = %+v, want row %v", i, im, row)
		}
	}
	var last upgrade.Imported
	_ = json.Unmarshal(imported[3].Payload, &last)
	if last.PayloadText != "not json" {
		t.Errorf("a non-JSON payload is carried as %+v", last)
	}

	// Documents: identical bytes, the sidecar's media type, no views.
	var atts []struct {
		Name      string `json:"name"`
		SHA256    string `json:"sha256"`
		MediaType string `json:"media_type"`
	}
	s.mustJSON(&atts, "story", "attachments", "sty_a1000001")
	names := []string{}
	for _, a := range atts {
		names = append(names, a.Name)
		if a.SHA256 != shaOf(t, filepath.Join(store, "stories", "sty_a1000001", a.Name)) {
			t.Errorf("%s sha256 differs from the old file", a.Name)
		}
		if a.Name == "shot.png" && a.MediaType != "image/png" {
			t.Errorf("shot.png media type %s", a.MediaType)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "plan.md,shot.png" {
		t.Errorf("attachments %v", names)
	}
	body := s.must("story", "attachment", "sty_a1000001", "plan.md")
	if body != "# Plan\n\nthe old plan\n" {
		t.Errorf("plan.md body %q", body)
	}
	s.must("ledger", "verify")

	// A second run imports nothing and changes no ledger byte.
	files := ledgerFiles(t, runtime)
	if out = s.must("upgrade", "--from", store, "--map", filepath.Join(maps, "full.toml")); !strings.Contains(out, "0 item(s) imported, 4 already imported") {
		t.Errorf("second run printed:\n%s", out)
	}
	after := ledgerFiles(t, runtime)
	for f, sum := range files {
		if after[f] != sum {
			t.Errorf("the second run changed %s", f)
		}
	}
}

// A repo listed in [upgrade] start_clean archives but refuses the import,
// naming the setting; with the key absent the same import plans.
func TestUpgradeStartClean(t *testing.T) {
	t.Parallel()
	s := newSandbox(t)
	oldFormat(t, s)
	s.must("upgrade", "--archive")
	store := t.TempDir()
	oldStore(t, store)
	m := filepath.Join(t.TempDir(), "map.toml")
	writeFile(t, m, "[status]\nbacklog = { workflow = \"default\", state = \"backlog\" }\ndone = { workflow = \"default\", state = \"done\" }\nactive = { workflow = \"default\", state = \"backlog\" }\n", 0o644)
	s.must("upgrade", "--from", store, "--map", m, "--dry-run")
	writeFile(t, filepath.Join(s.env["SATELLE_HOME"], "config.toml"), "[upgrade]\nstart_clean = [\""+s.dir+"\"]\n", 0o644)
	r := s.run("upgrade", "--from", store, "--map", m, "--dry-run")
	if r.code == 0 || !strings.Contains(r.err, "[upgrade] start_clean in ") || !strings.Contains(r.err, "config.toml") {
		t.Fatalf("start-clean repo: exit %d %s", r.code, r.err)
	}
}
