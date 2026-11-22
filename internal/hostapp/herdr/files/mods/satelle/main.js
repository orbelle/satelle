import { View, div } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, Scrollbar, TextView } from "gpui-base";
import { spaces, panes, focused, exec, run, watchFolder, theme, props, checking } from "herdr";

const MODES = ["active", "backlog", "done", "all"];
const MAX_ROWS = 200;

function location() {
  const fixed = props()?.folder;
  const space = spaces().find((s) => s.active), f = focused(), all = panes();
  const pane = f?.pane ? all.find((p) => p.pane === f.pane) : null;
  let cwd = fixed || pane?.cwd || f?.cwd;
  if (!cwd && f?.tab) cwd = all.find((p) => p.tab === f.tab && p.cwd)?.cwd;
  if (!cwd && space) { const tabs = space.tabs.map((tab) => tab.id); cwd = all.find((p) => tabs.includes(p.tab) && p.cwd)?.cwd; }
  return { cwd: cwd || space?.folder || null, on: space?.machine ?? null };
}
const key = () => JSON.stringify(location());
const took = (ms) => !ms ? null : ms < 1000 ? ms + "ms" : ms < 60000 ? Math.round(ms / 1000) + "s" : Math.floor(ms / 60000) + "m " + Math.round((ms % 60000) / 1000) + "s";

function readLedger(events) {
  const runs = {}, performers = {}, visited = new Set();
  for (const e of events) {
    const p = e.payload ?? {};
    if (e.kind === "gate.run") (runs[p.transition + " " + p.gate] ??= []).push(p);
    else if (e.kind === "performer.run") performers[p.state] = p;
    else if (e.kind === "item.transitioned") { visited.add(p.from); visited.add(p.to); }
    else if (e.kind === "item.created" && p.status) visited.add(p.status);
  }
  return { runs, performers, visited };
}

function mainPath(route) {
  const states = route.states ?? [], byName = Object.fromEntries(states.map((s) => [s.name, s]));
  let at = states.find((s) => s.start)?.name;
  const path = [];
  while (at && !path.includes(at)) {
    path.push(at);
    if (byName[at]?.terminal) break;
    const next = (route.transitions ?? []).find((tr) => tr.from.includes(at) && byName[tr.to] && !byName[tr.to].park && !path.includes(tr.to));
    at = next?.to;
  }
  return path;
}

const SAMPLE = {
  id: "sto_sample", title: "A sample story", status: "plan", workflow: "delivery", category: "feature", tags: ["sample"],
  body: "## Feature\n\nA **sample** body.", criteria: ["Given a thing, when it runs, then it works"], assignees: ["someone"], working: ["someone"], updated_at: "2026-01-01T00:00:00Z",
};
const SAMPLE_ROUTE = {
  status: "plan", states: [{ name: "backlog", start: true }, { name: "plan", current: true }, { name: "done", terminal: true }],
  transitions: [{ from: ["backlog"], to: "plan" }, { from: ["plan"], to: "done", gates: [{ gate: "a", result: "accept" }, { gate: "b", result: "error", notes: "why" }, { gate: "c" }] }],
};

const SAMPLE_VERSION = "satelle 0.0.0 (sample)";

const SAMPLE_LEDGER = [
  { kind: "item.transitioned", payload: { from: "backlog", to: "plan" } },
  { kind: "performer.run", payload: { state: "plan", binding: "planner", model: "m", duration_ms: 61000 } },
  { kind: "gate.run", payload: { transition: "plan->done", gate: "a", result: "accept", notes: "fine", duration_ms: 40 } },
  { kind: "gate.run", payload: { transition: "plan->done", gate: "b", binding: "reviewer", model: "m", result: "error", notes: "why", duration_ms: 5000 } },
];

export default class Satelle extends View {
  init(_props, cx) {
    this.watcher = props()?.watch ?? null;
    if (this.watcher) {
      if (checking()) return;
      this.detect(cx);
      cx.timer.every(5000, (cx) => this.detect(cx));
      return;
    }
    this.open = []; this.rest = null; this.openIds = new Set();
    this.mode = localStorage.getItem("mode") ?? "active"; if (!MODES.includes(this.mode)) this.mode = "active";
    this.filter = ""; this.expanded = {}; this.sections = {}; this.routes = {}; this.ledgers = {}; this.step = {}; this.gateOpen = {};
    this.handle = cx.focus_handle();
    this.input = InputState.new({ placeholder: "Filter: id, title, status, tag" });
    this.input.on("change", (_e, cx) => { this.filter = this.input.value(); cx.notify(); });
    this.input.on("focus", (_e, cx) => { this.typing = true; cx.notify(); });
    this.input.on("blur", (_e, cx) => { this.typing = false; cx.notify(); });
    if (checking()) {
      this.context = key(); this.version = SAMPLE_VERSION; this.open = [SAMPLE]; this.openIds = new Set([SAMPLE.id]);
      this.expanded[SAMPLE.id] = true; this.sections[SAMPLE.id + ":body"] = true; this.routes[SAMPLE.id] = SAMPLE_ROUTE; this.ledgers[SAMPLE.id] = readLedger(SAMPLE_LEDGER); this.gateOpen[SAMPLE.id + ":plan>done:b"] = true;
      return;
    }
    if (props()?.story) this.expanded[props().story] = true;
    this.refresh(cx);
    cx.timer.every(20000, (cx) => this.refresh(cx));
  }

  detect(cx) {
    const { cwd, on } = location(), context = key();
    if (context === this.context || this.detecting) return;
    this.detecting = true;
    cx.spawn(async (cx) => {
      let found = false;
      try { found = Boolean(cwd) && (await exec("satelle", ["runtime", "path"], { cwd, on })).code === 0; } catch {}
      this.detecting = false; this.context = context;
      if (found !== this.found) { this.found = found; run(found ? "layout.show" : "layout.hide", { id: this.watcher }); }
      if (key() !== context) this.detect(cx);
    });
  }

  refresh(cx) {
    if (this.loading) { this.again = true; return; }
    const { cwd, on } = location(), context = key();
    this.context = context;
    this.loadVersion(cwd, on, cx);
    if (!cwd) { this.error = "No folder"; this.open = []; this.rest = null; return; }
    this.loading = true;
    const wantAll = this.mode === "done" || this.mode === "all";
    cx.spawn(async (cx) => {
      try {
        const list = async (args) => {
          const r = await exec("satelle", ["story", "list", "--json", ...args], { cwd, on });
          if (r.code !== 0) throw new Error((r.stderr || r.stdout).trim().replace(/^satelle: /, ""));
          return JSON.parse(r.stdout || "[]") ?? [];
        };
        const open = await list(["--open"]);
        const rest = wantAll ? await list([]) : null;
        if (key() !== context) return;
        this.open = open; this.openIds = new Set(open.map((s) => s.id));
        if (rest) this.rest = rest;
        this.cwd = cwd; this.on = on; this.error = null;
        this.watch(cwd, on, cx);
        for (const id of Object.keys(this.expanded)) if (this.expanded[id]) this.loadRoute(id, cx);
      } catch (e) {
        if (key() === context) { this.error = String(e.message ?? e); this.open = []; this.rest = null; this.openIds = new Set(); }
      } finally {
        this.loading = false; cx.notify();
        if (this.again || key() !== context) { this.again = false; this.refresh(cx); }
      }
    });
  }

  loadVersion(cwd, on, cx) {
    cx.spawn(async (cx) => {
      let version = null;
      try {
        const r = await exec("satelle", ["version"], cwd ? { cwd, on } : { on });
        const line = r.code === 0 ? (r.stdout ?? "").trim().split("\n")[0].trim() : "";
        version = line || null;
      } catch {}
      this.version = version; cx.notify();
    });
  }

  watch(cwd, on, cx) {
    if (on || this.watchedFor === cwd) return;
    this.watchedFor = cwd;
    cx.spawn(async (cx) => {
      try {
        const r = await exec("satelle", ["runtime", "path"], { cwd });
        if (r.code !== 0) throw new Error(r.stderr);
        const dir = r.stdout.trim() + "/ledger";
        if (this.watching === dir) return;
        this.watching = dir;
        while (this.watching === dir) {
          await watchFolder(dir);
          if (this.watching === dir) this.refresh(cx);
        }
      } catch { this.watching = null; this.watchedFor = null; }
    });
  }

  loadRoute(id, cx) {
    const cwd = this.cwd, on = this.on;
    if (!cwd) return;
    cx.spawn(async (cx) => {
      try {
        const r = await exec("satelle", ["story", "route", id, "--json"], { cwd, on });
        if (r.code === 0) { this.routes[id] = JSON.parse(r.stdout); cx.notify(); }
        const l = await exec("satelle", ["ledger", "list", id, "--json"], { cwd, on });
        if (l.code === 0) { this.ledgers[id] = readLedger(JSON.parse(l.stdout || "[]") ?? []); cx.notify(); }
      } catch {}
    });
  }

  setMode(mode, cx) {
    this.mode = mode; localStorage.setItem("mode", mode);
    if ((mode === "done" || mode === "all") && !this.rest) this.refresh(cx);
    cx.notify();
  }

  toggle(id, cx) {
    this.expanded[id] = !this.expanded[id];
    if (this.expanded[id]) this.loadRoute(id, cx);
    cx.notify();
  }

  rows() {
    const all = this.mode === "done" || this.mode === "all" ? this.rest ?? this.open : this.open;
    const words = this.filter.toLowerCase().split(/\s+/).filter(Boolean);
    return all.filter((s) => {
      if (this.mode === "active" && s.status === "backlog") return false;
      if (this.mode === "backlog" && s.status !== "backlog") return false;
      if (this.mode === "done" && this.openIds.has(s.id)) return false;
      if (!words.length) return true;
      const text = [s.id, s.title, s.status, s.category && "category:" + s.category, s.workflow && "workflow:" + s.workflow,
        ...(s.assignees ?? []).map((a) => "assignee:" + a), ...(s.tags ?? [])].filter(Boolean).join(" ").toLowerCase();
      return words.every((w) => text.includes(w));
    }).sort((a, b) => (a.updated_at < b.updated_at ? 1 : -1));
  }

  statusColor(s, t) {
    if (s.status === "blocked") return t.blocked;
    if (s.status === "cancelled") return t.dim;
    if (!this.openIds.has(s.id)) return t.done;
    if (s.status === "backlog") return t.muted;
    return (s.working ?? []).length ? t.ansi2 : t.working;
  }

  progress(s, t) {
    const route = this.routes[s.id];
    if (!route) return div().text_color(t.dim).child("Loading progress…");
    const history = this.ledgers[s.id];
    const path = mainPath(route), at = path.indexOf(route.status), live = (s.working ?? []).length > 0;
    const now = live ? t.ansi2 : t.working;
    const names = at < 0 ? [...path, route.status] : path;
    const picked = names.includes(this.step[s.id]) ? this.step[s.id] : route.status;
    const past = (name, i) => name !== route.status && (history ? history.visited.has(name) : at >= 0 && i < at);
    const steps = names.map((name, i) => {
      const current = name === route.status, done = past(name, i), on = name === picked;
      return div().id("step-" + s.id + "-" + name).px(6).py(1).rounded(4).border_1().text_size(11).cursor_pointer()
        .border_color(current ? now : on ? t.foreground : t.border)
        .text_color(current ? now : done ? t.muted : t.dim)
        .when(current, (e) => e.font_semibold())
        .when(on, (e) => e.bg(t.selected))
        .hover((st) => st.bg(t.hover))
        .on_click((_e, cx) => { this.step[s.id] = name; cx.notify(); })
        .child(done ? "✓ " + name : name);
    });
    const exits = (route.transitions ?? []).filter((tr) => tr.from.includes(picked) && (tr.gates ?? []).length);
    const mark = { accept: ["✓", t.ansi2], reject: ["✕", t.ansi1], error: ["!", t.blocked] };
    const performer = history?.performers[picked];
    return v_flex().gap(6)
      .child(h_flex().flex_wrap().gap(4).children(steps))
      .when(Boolean(performer), (e) => e.child(h_flex().flex_wrap().gap(6).text_size(11).text_color(t.dim)
        .child(div().child("performed by"))
        .child(div().text_color(t.muted).child([performer.binding, performer.model, took(performer.duration_ms)].filter(Boolean).join(" · ")))))
      .when(!exits.length, (e) => e.child(div().text_size(11).text_color(t.dim).child("No gates leave " + picked)))
      .children(exits.map((tr) => v_flex().gap(2)
        .child(div().text_size(11).text_color(t.dim).child(picked + " → " + tr.to))
        .children(tr.gates.map((g) => {
          const [glyph, color] = mark[g.result] ?? ["○", t.dim];
          const runs = history?.runs[picked + "->" + tr.to + " " + g.gate] ?? [], last = g.result ? runs[runs.length - 1] : null;
          const k = s.id + ":" + picked + ">" + tr.to + ":" + g.gate, open = Boolean(this.gateOpen[k]);
          const notes = last?.notes ?? g.notes ?? "", failed = Boolean(g.result) && g.result !== "accept";
          const who = last ? [last.binding ?? "check", last.binding && last.model && !last.binding.includes(last.model) ? last.model : null, took(last.duration_ms), runs.length > 1 ? "run " + runs.length : null].filter(Boolean).join(" · ") : "";
          const shown = open ? notes : failed ? (notes.length > 240 ? notes.slice(0, 240) + "…" : notes) : "";
          return v_flex()
            .child(h_flex().id("gate-" + k).gap(6).rounded(4).when(Boolean(notes), (e) => e.cursor_pointer().hover((st) => st.bg(t.hover)))
              .on_click((_e, cx) => { if (notes) { this.gateOpen[k] = !open; cx.notify(); } })
              .child(div().w(12).flex_none().text_color(color).child(glyph))
              .child(div().flex_none().text_color(g.result ? t.foreground : t.muted).child(g.gate))
              .when(failed, (e) => e.child(div().flex_none().text_size(11).text_color(color).child(g.result)))
              .when(Boolean(who), (e) => e.child(div().flex_1().min_w(0).truncate().text_size(11).text_color(t.dim).child(who))))
            .when(Boolean(shown), (e) => e.child(div().pl(18).pb(2).text_size(11).text_color(t.muted).child(shown)));
        }))));
  }

  section(s, name, title, open, t, body) {
    const k = s.id + ":" + name, shown = this.sections[k] ?? open;
    return v_flex().gap(4)
      .child(h_flex().id("sec-" + k).gap(6).cursor_pointer().text_size(11).text_color(t.muted).hover((st) => st.text_color(t.foreground))
        .on_click((_e, cx) => { this.sections[k] = !shown; cx.notify(); })
        .child(div().w(10).child(shown ? "▾" : "▸")).child(div().font_semibold().child(title)))
      .when(shown, (e) => e.child(div().pl(16).child(body())));
  }

  detail(s, t) {
    const criteria = s.criteria ?? [];
    return v_flex().pl(26).pr(10).pb(10).pt(2).gap(10)
      .child(this.section(s, "progress", "Progress", true, t, () => this.progress(s, t)))
      .child(this.section(s, "criteria", "Criteria (" + criteria.length + ")", true, t, () =>
        v_flex().gap(4).children(criteria.length ? criteria.map((c, i) =>
          h_flex().items_start().gap(6).child(div().w(18).flex_none().text_color(t.dim).child(String(i + 1) + "."))
            .child(div().flex_1().min_w(0).child(c))) : [div().text_color(t.dim).child("None")])))
      .child(this.section(s, "body", "Description", false, t, () =>
        s.body ? TextView.markdown("body-" + s.id, s.body).selectable(true) : div().text_color(t.dim).child("None")));
  }

  chips(s, t) {
    const working = s.working ?? [];
    const pick = (el, token) => el.id("chip-" + s.id + "-" + token).cursor_pointer().hover((st) => st.opacity(0.75))
      .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
      .on_click((_e, cx) => {
        cx.stop_propagation();
        const words = this.filter.split(/\s+/).filter(Boolean), had = words.some((w) => w.toLowerCase() === token.toLowerCase());
        this.filter = (had ? words.filter((w) => w.toLowerCase() !== token.toLowerCase()) : [...words, token]).join(" ");
        this.input.set_value(this.filter); cx.notify();
      });
    const kv = (k, v, color, extra) => pick(h_flex().px(5).rounded(3).bg(color + "22").text_color(color), k + ":" + v)
      .child(div().font_semibold().child(k)).child(div().opacity(0.55).child(":")).child(div().child(v))
      .when(Boolean(extra), (e) => e.child(div().opacity(0.55).px(3).child("·")).child(div().font_semibold().child(extra)));
    const out = [];
    if (s.category) out.push(kv("category", s.category, t.ansi3));
    if (s.workflow) out.push(kv("workflow", s.workflow, t.ansi4));
    for (const a of s.assignees ?? []) out.push(kv("assignee", a, t.ansi5, working.includes(a) ? "working" : null));
    for (const tag of s.tags ?? []) {
      const i = tag.indexOf(":");
      out.push(i > 0 ? kv(tag.slice(0, i), tag.slice(i + 1), t.ansi6) : pick(div().px(5).rounded(3).bg(t.raised).text_color(t.muted), tag).child(tag));
    }
    return out;
  }

  row(s, t) {
    const open = Boolean(this.expanded[s.id]), color = this.statusColor(s, t), busy = (s.working ?? []).length > 0;
    return v_flex().flex_none().rounded(7).when(open, (e) => e.bg(t.surface).border_1().border_color(t.border))
      .child(h_flex().id("story-" + s.id).flex_none().px(8).py(6).gap(8).items_start().rounded(7).cursor_pointer()
        .hover((st) => st.bg(t.hover)).on_click((_e, cx) => this.toggle(s.id, cx))
        .child(div().w(10).flex_none().text_color(t.dim).child(open ? "▾" : "▸"))
        .child(v_flex().flex_1().min_w(0).gap(4)
          .child(h_flex().items_start().gap(8)
            .child(div().flex_1().min_w(0).child(s.title))
            .when(busy, (e) => e.child(div().mt(6).size(6).flex_none().rounded(3).bg(t.ansi2).loop("opacity", { from: 0.35, to: 1, period: 1200 })))
            .child(div().px(5).py(1).flex_none().rounded(4).border_1().border_color(color).text_size(10).text_color(color).child(s.status)))
          .child(h_flex().flex_wrap().gap(4).text_size(10).children(this.chips(s, t))))
        .child(div().id("copy-" + s.id).flex_none().px(4).py(1).rounded(4).text_size(10).cursor_pointer()
          .text_color(this.copied === s.id ? t.ansi2 : t.dim).hover((st) => st.bg(t.raised).text_color(t.foreground))
          .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
          .on_click((_e, cx) => {
            cx.stop_propagation(); cx.write_to_clipboard(s.id); this.copied = s.id; cx.notify();
            cx.timer.after(1500, (cx) => { if (this.copied === s.id) { this.copied = null; cx.notify(); } });
          })
          .child(this.copied === s.id ? "copied" : s.id)))
      .when(open, (e) => e.child(this.detail(s, t)));
  }

  render(cx) {
    if (this.watcher) { if (!checking()) this.detect(cx); return div(); }
    if (!checking() && key() !== this.context) this.refresh(cx);
    const t = theme(), rows = this.rows(), shown = rows.slice(0, MAX_ROWS);
    const repo = (this.cwd ?? location().cwd ?? "").split("/").filter(Boolean).pop() ?? "";
    const empty = this.error || (this.loading ? "Loading stories…" : this.filter ? "No stories match" : "No " + (this.mode === "all" ? "" : this.mode + " ") + "stories");
    return v_flex().id("satelle").size_full().min_h(0).rounded(10).overflow_hidden().bg(t.background).border_1().border_color(t.border)
      .text_color(t.foreground).text_size(13).track_focus(this.handle)
      .on_key_down((e, cx) => {
        if (e.key === "escape" && this.typing) { this.typing = false; this.handle.focus(); cx.stop_propagation(); cx.notify(); }
      })
      .child(h_flex().h(42).flex_none().px(14).gap(8).items_center().border_b(1).border_color(t.border)
        .child(div().font_semibold().child("Stories"))
        .child(div().min_w(0).truncate().text_color(t.dim).child(repo))
        .child(div().px(5).py(1).flex_none().rounded(4).border_1().border_color(t.border).text_size(10).text_color(t.muted).child(String(rows.length)))
        .child(div().flex_1())
        .child(div().id("satelle-refresh").px(6).py(2).flex_none().rounded(5).text_size(11).text_color(t.dim).cursor_pointer()
          .hover((st) => st.bg(t.hover).text_color(t.foreground)).on_click((_e, cx) => this.refresh(cx)).child(this.loading ? "…" : "Refresh")))
      .child(v_flex().flex_none().p(8).gap(8).border_b(1).border_color(t.border)
        .child(div().id("satelle-filter").h(30).px(10).flex().items_center().rounded(8).border_1().border_color(this.typing ? t.accent : t.border).cursor_text()
          .on_mouse_down("left", (_e, cx) => cx.stop_propagation())
          .child(Input.new(this.input).h_full().text_size(12).text_color(t.foreground)))
        .child(h_flex().gap(4).children(MODES.map((m) =>
          div().id("mode-" + m).px(8).py(2).rounded(6).text_size(11).cursor_pointer().border_1()
            .border_color(this.mode === m ? t.accent : t.border).text_color(this.mode === m ? t.foreground : t.muted)
            .when(this.mode === m, (e) => e.bg(t.selected)).hover((st) => st.bg(t.hover))
            .on_click((_e, cx) => this.setMode(m, cx)).child(m)))))
      .child(div().flex_1().min_h(0).relative()
        .child(v_flex().id("satelle-list").absolute().inset_0().overflow_y_scroll().p(6).gap(2)
          .children(shown.map((s) => this.row(s, t)))
          .when(!shown.length, (e) => e.child(div().p(8).text_color(this.error ? t.blocked : t.dim).child(empty)))
          .when(rows.length > MAX_ROWS, (e) => e.child(div().p(8).text_size(11).text_color(t.dim).child("Showing the first " + MAX_ROWS + " of " + rows.length + ": filter to narrow"))))
        .child(Scrollbar.vertical("satelle-list").absolute().inset_0()))
      .child(h_flex().id("satelle-footer").h(24).flex_none().px(14).items_center().border_t(1).border_color(t.border).text_size(10).text_color(t.dim)
        .child(div().min_w(0).truncate().child(this.version === undefined ? "satelle version …" : this.version ?? "satelle version unknown")));
  }
}
