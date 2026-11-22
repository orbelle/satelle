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

const panelKit = `export class View {}
export function el() {
  const node = { id: null, kids: [] };
  const add = (c) => node.kids.push(c && c.__node ? c.__node : c);
  const p = new Proxy(function () {}, {
    get(_, k) {
      if (k === "__node") return node;
      if (k === "then") return undefined;
      if (k === "id") return (v) => { node.id = v; return p; };
      if (k === "bg") return () => { node.bg = true; return p; };
      if (k === "child") return (c) => { add(c); return p; };
      if (k === "children") return (cs) => { for (const c of cs ?? []) add(c); return p; };
      if (k === "when") return (c, f) => (c ? f(p) : p);
      if (k === "on_click") return (f) => { node.click = f; return p; };
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
export const InputState = {
  new: () => {
    let value = "";
    const on = {};
    return {
      on(ev, fn) { on[ev] = fn; },
      value: () => value,
      set_value(v) { value = v; },
      __type(v, cx) { value = v; on.change?.({}, cx); },
    };
  },
};
export const Input = { new: () => el() };
export const Scrollbar = { vertical: () => el() };
export const TextView = { markdown: () => el() };
`

const panelHerdr = `import { execFile } from "node:child_process";
export const spaces = () => globalThis.__spaces ?? [];
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
const storeFile = process.env.STORE_FILE;
const store = storeFile && fs.existsSync(storeFile) ? JSON.parse(fs.readFileSync(storeFile, "utf8")) : JSON.parse(process.env.STORE_SEED || "{}");
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
    chip: find(root, (n) => typeof n.id === "string" && n.id.startsWith("mode-") && n.bg).map((n) => n.id).join(","),
    filter: view.input.value(),
    clear: find(root, (n) => n.id === "satelle-filter-clear").map(text),
    store: { ...store },
  };
};
const enter = async (view, id) => {
  globalThis.__spaces = ["s1", "s2", "s3"].map((s) => ({ id: s, active: s === id, tabs: [] }));
  view.render(cx);
  await settle();
};
const scenarios = {
  async spaces(view) {
    view.input.__type("epic:remotes", cx);
    out.snaps.push(snap(view));
    await enter(view, "s2");
    out.snaps.push(snap(view));
    view.setMode("done", cx);
    await settle();
    await enter(view, "s1");
    out.snaps.push(snap(view));
    view.setMode("backlog", cx);
    await enter(view, "s2");
    out.snaps.push(snap(view));
    await enter(view, "s1");
    out.snaps.push(snap(view));
    await enter(view, "s3");
    out.snaps.push(snap(view));
  },
  async clear(view) {
    view.setMode("backlog", cx);
    out.snaps.push(snap(view));
    view.input.__type("epic:remotes", cx);
    out.snaps.push(snap(view));
    const btn = find(view.render(cx).__node, (n) => n.id === "satelle-filter-clear");
    if (btn.length !== 1 || !btn[0].click) throw new Error("no satelle-filter-clear button to click");
    btn[0].click({}, cx);
    out.snaps.push(snap(view));
  },
  async "restart-set"(view) {
    view.setMode("backlog", cx);
    view.input.__type("epic:remotes", cx);
    out.snaps.push(snap(view));
  },
};
const out = { snaps: [], intervals, threw: false, sameView: false };
try {
  if (process.env.SCENARIO) globalThis.__spaces = [{ id: "s1", active: true, tabs: [] }, { id: "s2", active: false, tabs: [] }];
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
  await scenarios[process.env.SCENARIO]?.(view);
  if (storeFile) fs.writeFileSync(storeFile, JSON.stringify(store));
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
	List   string            `json:"list"`
	Chip   string            `json:"chip"`
	Filter string            `json:"filter"`
	Clear  []string          `json:"clear"`
	Store  map[string]string `json:"store"`
}

type panelRun struct {
	Snaps     []panelSnap `json:"snaps"`
	Intervals []int       `json:"intervals"`
	Threw     bool        `json:"threw"`
	Error     string      `json:"error"`
	SameView  bool        `json:"sameView"`
}

type panelCase struct {
	version     string
	versionCode string
	stories     string
	next        string
	noSatelle   bool
	scenario    string
	storeFile   string
	seed        string
}

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
		"FAKE_VERSION_FILE=" + versionFile, "FAKE_VERSION_CODE=" + code, "FAKE_STORIES=" + storiesFile, "NEXT_VERSION=" + c.next,
		"SCENARIO=" + c.scenario, "STORE_FILE=" + c.storeFile, "STORE_SEED=" + c.seed}
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

const spaceStories = `[{"id":"sto_a","title":"Story A","status":"plan","tags":["epic:remotes"],"updated_at":"2026-01-02T00:00:00Z"},` +
	`{"id":"sto_b","title":"Story B","status":"plan","updated_at":"2026-01-01T00:00:00Z"}]`

func storyIDs(s panelSnap) string {
	ids := make([]string, 0, len(s.Stories))
	for _, st := range s.Stories {
		ids = append(ids, strings.TrimPrefix(st.ID, "story-"))
	}
	return strings.Join(ids, ",")
}

func wantSpace(t *testing.T, label string, s panelSnap, chip, filter, stories string) {
	t.Helper()
	if s.Chip != chip {
		t.Errorf("%s: selected chip = %q, want %q", label, s.Chip, chip)
	}
	if s.Filter != filter {
		t.Errorf("%s: filter box = %q, want %q", label, s.Filter, filter)
	}
	if got := storyIDs(s); got != stories {
		t.Errorf("%s: stories = %q, want %q", label, got, stories)
	}
}

func runSpaces(t *testing.T, seed string) panelRun {
	t.Helper()
	r := runPanel(t, panelCase{scenario: "spaces", stories: spaceStories, seed: seed})
	if len(r.Snaps) != 7 {
		t.Fatalf("driver rendered %d times, want 7", len(r.Snaps))
	}
	return r
}

func TestPanelChipFollowsSpace(t *testing.T) {
	r := runSpaces(t, "")
	wantSpace(t, "s2 after s1 set backlog", r.Snaps[4], "mode-done", "", "")
	wantSpace(t, "back to s1", r.Snaps[5], "mode-backlog", "epic:remotes", "")
	store := r.Snaps[6].Store
	if store["mode:s1"] != "backlog" || store["mode:s2"] != "done" {
		t.Errorf("store = %v, want mode:s1=backlog and mode:s2=done", store)
	}
	if _, ok := store["mode"]; ok {
		t.Errorf("store = %v, has a global mode key", store)
	}
}

func TestPanelFilterFollowsSpace(t *testing.T) {
	r := runSpaces(t, "")
	wantSpace(t, "s1 before typing", r.Snaps[0], "mode-active", "", "sto_a,sto_b")
	wantSpace(t, "s1 typed epic:remotes", r.Snaps[1], "mode-active", "epic:remotes", "sto_a")
	wantSpace(t, "switch to s2", r.Snaps[2], "mode-active", "", "sto_a,sto_b")
	wantSpace(t, "back to s1", r.Snaps[3], "mode-active", "epic:remotes", "sto_a")
	store := r.Snaps[6].Store
	if store["filter:s1"] != "epic:remotes" || store["filter:s2"] != "" {
		t.Errorf("store = %v, want filter:s1=epic:remotes and no filter text for s2", store)
	}
}

func TestPanelUntouchedSpaceDefaults(t *testing.T) {
	t.Run("never chosen", func(t *testing.T) {
		r := runSpaces(t, "")
		wantSpace(t, "fresh s3", r.Snaps[6], "mode-active", "", "sto_a,sto_b")
	})
	t.Run("legacy global mode ignored", func(t *testing.T) {
		r := runSpaces(t, `{"mode":"backlog"}`)
		wantSpace(t, "fresh s1", r.Snaps[0], "mode-active", "", "sto_a,sto_b")
		wantSpace(t, "fresh s3", r.Snaps[6], "mode-active", "", "sto_a,sto_b")
	})
}

func TestPanelSpaceStateSurvivesRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "store.json")
	runPanel(t, panelCase{scenario: "restart-set", storeFile: file, stories: spaceStories})
	r := runPanel(t, panelCase{scenario: "restart-read", storeFile: file, stories: spaceStories})
	wantSpace(t, "s1 after restart", r.Snaps[0], "mode-backlog", "epic:remotes", "")
	if r.Snaps[0].Store["mode:s1"] != "backlog" || r.Snaps[0].Store["filter:s1"] != "epic:remotes" {
		t.Errorf("persisted store = %v", r.Snaps[0].Store)
	}
}

const backlogStories = `[{"id":"sto_c","title":"Story C","status":"backlog","tags":["epic:remotes"],"updated_at":"2026-01-03T00:00:00Z"},` +
	`{"id":"sto_d","title":"Story D","status":"backlog","updated_at":"2026-01-02T00:00:00Z"},` +
	`{"id":"sto_e","title":"Story E","status":"plan","tags":["epic:remotes"],"updated_at":"2026-01-01T00:00:00Z"}]`

func runClear(t *testing.T) panelRun {
	t.Helper()
	r := runPanel(t, panelCase{scenario: "clear", stories: backlogStories})
	if len(r.Snaps) != 4 {
		t.Fatalf("driver rendered %d times, want 4", len(r.Snaps))
	}
	return r
}

func TestPanelClearAllShowsWithFilterText(t *testing.T) {
	r := runClear(t)
	if got := r.Snaps[1].Clear; len(got) != 0 {
		t.Errorf("empty filter: clear buttons = %q, want none", got)
	}
	if got := r.Snaps[2].Clear; len(got) != 1 || got[0] != "clear all" {
		t.Errorf("filter with text: clear buttons = %q, want [\"clear all\"]", got)
	}
	if got := r.Snaps[3].Clear; len(got) != 0 {
		t.Errorf("after clear all: clear buttons = %q, want none", got)
	}
}

func TestPanelClearAllEmptiesFilterKeepsChip(t *testing.T) {
	r := runClear(t)
	wantSpace(t, "typed epic:remotes on backlog", r.Snaps[2], "mode-backlog", "epic:remotes", "sto_c")
	wantSpace(t, "after clear all", r.Snaps[3], "mode-backlog", "", "sto_c,sto_d")
	store := r.Snaps[3].Store
	if store["filter:s1"] != "" || store["mode:s1"] != "backlog" {
		t.Errorf("store = %v, want filter:s1 empty and mode:s1=backlog", store)
	}
}
