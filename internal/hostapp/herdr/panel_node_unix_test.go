//go:build unix

package herdr

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The panel imports herdr's runtime modules by bare name. These stubs stand in
// for them so the shipped main.js runs under node: elements record their id
// and children, and exec runs the real satelle on PATH (a fake one here).
const panelKit = `export class View {}
export function el() {
  const node = { id: null, kids: [] };
  const add = (c) => node.kids.push(c && c.__node ? c.__node : c);
  const p = new Proxy(function () {}, {
    get(_, k) {
      if (k === "__node") return node;
      if (k === "then") return undefined;
      if (k === "id") return (v) => { node.id = v; return p; };
      if (k === "child") return (c) => { add(c); return p; };
      if (k === "children") return (cs) => { for (const c of cs ?? []) add(c); return p; };
      if (k === "when") return (c, f) => (c ? f(p) : p);
      return () => p;
    },
  });
  return p;
}
export const div = () => el();
`

const panelBase = `import { el } from "./gpui-kit.mjs";
export const v_flex = () => el();
export const h_flex = () => el();
export const InputState = { new: () => ({ on() {}, value: () => "", set_value() {} }) };
export const Input = { new: () => el() };
export const Scrollbar = { vertical: () => el() };
export const TextView = { markdown: () => el() };
`

const panelHerdr = `import { execFile } from "node:child_process";
export const spaces = () => [];
export const panes = () => [];
export const focused = () => null;
export const props = () => ({ folder: process.env.PANEL_FOLDER });
export const checking = () => false;
export const theme = () => new Proxy({}, { get: () => "#000000" });
export const run = () => {};
export const watchFolder = () => new Promise(() => {});
export function exec(cmd, args, opts) {
  return new Promise((resolve) => {
    execFile(cmd, args, { cwd: opts?.cwd }, (err, stdout, stderr) => {
      if (err && err.code === "ENOENT") return resolve({ code: 127, stdout: "", stderr: cmd + ": not found" });
      resolve({ code: err ? (typeof err.code === "number" ? err.code : 1) : 0, stdout, stderr });
    });
  });
}
`

const panelDriver = `import Satelle from "./main.mjs";
import fs from "node:fs";
const store = {};
globalThis.localStorage = { getItem: (k) => store[k] ?? null, setItem: (k, v) => { store[k] = String(v); } };
const pending = new Set(), intervals = [];
const cx = {
  spawn(fn) { const p = Promise.resolve().then(() => fn(cx)); pending.add(p); p.finally(() => pending.delete(p)).catch(() => {}); return p; },
  notify() {}, stop_propagation() {}, write_to_clipboard() {},
  timer: { every(ms) { intervals.push(ms); }, after() {} },
  focus_handle: () => ({ focus() {} }),
};
const settle = async () => { while (pending.size) await Promise.allSettled([...pending]); };
const text = (n) => typeof n === "string" ? n : n && n.kids ? n.kids.map(text).join("") : "";
const find = (n, pred, out = []) => { if (n && n.kids) { if (pred(n)) out.push(n); for (const k of n.kids) find(k, pred, out); } return out; };
const snap = (view) => {
  const root = view.render(cx).__node;
  const footer = find(root, (n) => n.id === "satelle-footer");
  return {
    footer: footer.length === 1 ? text(footer[0]) : null,
    footers: footer.length,
    stories: find(root, (n) => typeof n.id === "string" && n.id.startsWith("story-")).map((n) => ({ id: n.id, text: text(n) })),
    list: find(root, (n) => n.id === "satelle-list").map(text).join(""),
  };
};
const out = { snaps: [], intervals, threw: false, sameView: false };
try {
  const view = new Satelle();
  view.init({}, cx);
  await settle();
  out.snaps.push(snap(view));
  if (process.env.NEXT_VERSION) {
    fs.writeFileSync(process.env.FAKE_VERSION_FILE, process.env.NEXT_VERSION + "\n");
    const before = view;
    view.refresh(cx);
    await settle();
    out.snaps.push(snap(view));
    out.sameView = before === view;
  }
} catch (e) {
  out.threw = true; out.error = String(e && e.stack || e);
}
console.log(JSON.stringify(out));
`

const fakeSatelle = `#!/bin/sh
PATH=/usr/bin:/bin
case "$1" in
version)
  if [ "${FAKE_VERSION_CODE:-0}" != 0 ]; then echo "satelle: version broke" >&2; exit "$FAKE_VERSION_CODE"; fi
  cat "$FAKE_VERSION_FILE"; exit 0 ;;
story) cat "$FAKE_STORIES"; exit 0 ;;
*) exit 1 ;;
esac
`

type panelSnap struct {
	Footer  *string `json:"footer"`
	Footers int     `json:"footers"`
	Stories []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"stories"`
	List string `json:"list"`
}

type panelRun struct {
	Snaps     []panelSnap `json:"snaps"`
	Intervals []int       `json:"intervals"`
	Threw     bool        `json:"threw"`
	Error     string      `json:"error"`
	SameView  bool        `json:"sameView"`
}

type panelCase struct {
	version     string // first contents of the version file
	versionCode string // exit code of satelle version
	stories     string // stdout of satelle story list
	next        string // version file contents before a second refresh, if any
	noSatelle   bool   // PATH holds no satelle
}

// runPanel runs the embedded main.js under node against a fake satelle.
func runPanel(t *testing.T, c panelCase) panelRun {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	bin, empty, folder := filepath.Join(dir, "bin"), filepath.Join(dir, "empty"), filepath.Join(dir, "repo")
	for _, d := range []string{bin, empty, folder} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	main := string(Files()[0].Body)
	for _, mod := range []string{"gpui-kit", "gpui-base", "herdr"} {
		from := `from "` + mod + `"`
		if !strings.Contains(main, from) {
			t.Fatalf("embedded main.js no longer imports %s; the stubs do not cover it", from)
		}
		main = strings.Replace(main, from, `from "./`+mod+`.mjs"`, 1)
	}
	versionFile, storiesFile := filepath.Join(dir, "version.txt"), filepath.Join(dir, "stories.json")
	stories := c.stories
	if stories == "" {
		stories = "[]"
	}
	for name, body := range map[string]string{
		filepath.Join(dir, "main.mjs"):      main,
		filepath.Join(dir, "gpui-kit.mjs"):  panelKit,
		filepath.Join(dir, "gpui-base.mjs"): panelBase,
		filepath.Join(dir, "herdr.mjs"):     panelHerdr,
		filepath.Join(dir, "driver.mjs"):    panelDriver,
		filepath.Join(bin, "satelle"):       fakeSatelle,
		versionFile:                         c.version + "\n",
		storiesFile:                         stories,
	} {
		if err := os.WriteFile(name, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	path := bin
	if c.noSatelle {
		path = empty
	}
	code := c.versionCode
	if code == "" {
		code = "0"
	}
	cmd := exec.Command(node, "driver.mjs")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + path, "HOME=" + dir, "PANEL_FOLDER=" + folder,
		"FAKE_VERSION_FILE=" + versionFile, "FAKE_VERSION_CODE=" + code, "FAKE_STORIES=" + storiesFile, "NEXT_VERSION=" + c.next}
	stdout, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("driver: %v\n%s", err, stderr)
	}
	var r panelRun
	if err := json.Unmarshal(stdout, &r); err != nil {
		t.Fatalf("driver output %q: %v", stdout, err)
	}
	if r.Threw {
		t.Fatalf("the panel threw: %s", r.Error)
	}
	return r
}

func footerOf(t *testing.T, s panelSnap) string {
	t.Helper()
	if s.Footers != 1 || s.Footer == nil {
		t.Fatalf("panel rendered %d satelle-footer elements, want 1", s.Footers)
	}
	return *s.Footer
}

func TestPanelFooterShowsSatelleVersion(t *testing.T) {
	const v = "satelle 9.8.7 (commit abc, built now)"
	r := runPanel(t, panelCase{version: v})
	if got := footerOf(t, r.Snaps[0]); got != v {
		t.Errorf("footer = %q, want %q", got, v)
	}
}

func TestPanelFooterFollowsSatelleUpdate(t *testing.T) {
	const before, after = "satelle 1.0.0 (commit a, built then)", "satelle 1.0.1 (commit b, built now)"
	r := runPanel(t, panelCase{version: before, next: after})
	if len(r.Snaps) != 2 {
		t.Fatalf("driver rendered %d times, want 2", len(r.Snaps))
	}
	if got := footerOf(t, r.Snaps[0]); got != before {
		t.Errorf("footer before update = %q, want %q", got, before)
	}
	if got := footerOf(t, r.Snaps[1]); got != after {
		t.Errorf("footer after refresh = %q, want %q", got, after)
	}
	if !r.SameView {
		t.Error("the second render was not on the same panel instance")
	}
	timed := false
	for _, ms := range r.Intervals {
		timed = timed || ms == 20000
	}
	if !timed {
		t.Errorf("no 20s refresh timer registered; intervals = %v", r.Intervals)
	}
}

func TestPanelFooterUnknownVersionKeepsStories(t *testing.T) {
	const unknown = "satelle version unknown"
	t.Run("version fails", func(t *testing.T) {
		r := runPanel(t, panelCase{versionCode: "1",
			stories: `[{"id":"sto_x","title":"Story X","status":"plan","updated_at":"2026-01-01T00:00:00Z"}]`})
		s := r.Snaps[0]
		if got := footerOf(t, s); got != unknown {
			t.Errorf("footer = %q, want %q", got, unknown)
		}
		if len(s.Stories) != 1 || s.Stories[0].ID != "story-sto_x" || !strings.Contains(s.Stories[0].Text, "Story X") {
			t.Errorf("stories = %+v, want one story-sto_x titled Story X", s.Stories)
		}
	})
	t.Run("satelle not on PATH", func(t *testing.T) {
		r := runPanel(t, panelCase{noSatelle: true})
		s := r.Snaps[0]
		if got := footerOf(t, s); got != unknown {
			t.Errorf("footer = %q, want %q", got, unknown)
		}
		if len(s.Stories) != 0 || !strings.Contains(s.List, "not found") {
			t.Errorf("list = %q (stories %+v), want the exec error shown and no stories", s.List, s.Stories)
		}
	})
}
