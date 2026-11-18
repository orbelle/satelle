(function () {
  "use strict";
  var PANELS = ["stories", "activity", "tasks", "queues", "workflow"];
  function isItemPanel(panel) {
    var t = panel.dataset.topic;
    return t === "stories" || t === "activity" || t === "tasks" || t === "queues" || t.indexOf("wf-") === 0;
  }
  function wfPanelTopics() {
    return [].slice.call(document.querySelectorAll(".tab[data-wf-panel]")).map(function (t) { return t.dataset.panel; });
  }
  var FILTER_KEYS = { status: 1, workflow: 1, assignee: 1, category: 1, tags: 1, tag: 1, kind: 1, task: 1, engaged: 1 };
  var TAG_FACETS = {};
  (document.body.dataset.tagFacets || "").split(",").filter(Boolean).forEach(function (k) { TAG_FACETS[k] = 1; });
  var ORDER_FIELDS = { updated: 1, created: 1, status: 1, title: 1, id: 1, order: 1 };
  var DEFAULT_ORDER = "updated";

  var pendingExpand = null;
  try { pendingExpand = new URLSearchParams(location.search).get("expand"); } catch (e) {}

  function topicForKind(kind) {
    if (kind === "queue") return "queues";
    return (kind === "task" || kind === "execution") ? "tasks" : "stories";
  }

  function debounce(fn, ms) {
    var t = null;
    return function () {
      var args = arguments, self = this;
      if (t) clearTimeout(t);
      t = setTimeout(function () { t = null; fn.apply(self, args); }, ms);
    };
  }

  function showTab(name) {
    if (PANELS.indexOf(name) === -1) name = "stories";
    document.querySelectorAll(".tab").forEach(function (t) {
      t.setAttribute("aria-selected", t.dataset.panel === name ? "true" : "false");
    });
    document.querySelectorAll(".panel").forEach(function (p) {
      p.classList.toggle("active", p.dataset.topic === name);
    });
  }
  function initTabs() {
    wfPanelTopics().forEach(function (tp) { if (PANELS.indexOf(tp) === -1) PANELS.push(tp); });
    window.addEventListener("hashchange", function () {
      showTab((location.hash || "#stories").slice(1));
    });
    showTab((location.hash || "#stories").slice(1));
  }

  function syncFilterToURL(panel) {
    var input = panel.querySelector(".filterbar input");
    if (!input || !panel.dataset.topic) return;
    var params = new URLSearchParams(location.search);
    var v = input.value.trim();
    if (v) params.set(panel.dataset.topic, v); else params.delete(panel.dataset.topic);
    var qs = params.toString();
    var base = location.href.split("?")[0].split("#")[0];
    history.replaceState(null, "", base + (qs ? "?" + qs : "") + location.hash);
  }

  function parseQuery(q) {
    var filters = [], free = [], order = "";
    (q || "").trim().split(/\s+/).forEach(function (part) {
      if (!part) return;
      var i = part.indexOf(":");
      var key = i > 0 ? part.slice(0, i).toLowerCase() : "";
      if (key === "order") { order = part.slice(i + 1).toLowerCase(); return; }
      if (i > 0 && (FILTER_KEYS[key] || TAG_FACETS[key])) {
        var k = key === "tag" ? "tags" : key;
        filters.push({ key: k, vals: part.slice(i + 1).toLowerCase().split(",").filter(Boolean) });
        return;
      }
      free.push(part.toLowerCase());
    });
    return { filters: filters, order: order, free: free };
  }

  function rowMatches(row, parsed) {
    var hasStatus = false;
    for (var k = 0; k < parsed.filters.length; k++) {
      var t = parsed.filters[k];
      if (t.key === "status") {
        hasStatus = true;
        if (t.vals.indexOf("all") === -1 && t.vals.indexOf(row.dataset.status || "") === -1) return false;
      } else if (TAG_FACETS[t.key]) {
        var facet = (row.getAttribute("data-tag-" + t.key) || "").toLowerCase();
        if (t.vals.indexOf("all") === -1 && t.vals.indexOf(facet) === -1) return false;
      } else if (t.key === "tags" || t.key === "assignee" || t.key === "engaged") {
        var list = (row.dataset[t.key] || "").toLowerCase().split(",");
        if (t.key === "tags") list = list.concat((row.dataset.lineage || "").toLowerCase().split(","));
        var any = t.key === "assignee" && t.vals.indexOf("all") !== -1;
        if (!any && !t.vals.some(function (v) { return list.indexOf(v) !== -1; })) return false;
      } else {
        var val = (row.dataset[t.key] || "").toLowerCase();
        if (t.vals.indexOf("all") === -1 && t.vals.indexOf(val) === -1) return false;
      }
    }
    if (!hasStatus && row.dataset.terminal === "1") return false;
    var search = (row.dataset.search || "").toLowerCase();
    return parsed.free.every(function (term) { return search.indexOf(term) !== -1; });
  }

  function sortKey(row, field) {
    if (field === "title") return row.dataset.title || "";
    if (field === "id") return row.dataset.expandUrl || "";
    if (field === "order") {
      var tags = (row.dataset.tags || "").split(",");
      for (var i = 0; i < tags.length; i++) {
        if (tags[i].indexOf("order:") === 0) {
          var n = parseInt(tags[i].slice(6), 10);
          if (!isNaN(n)) return ("000000" + n).slice(-6);
        }
      }
      return "999999";
    }
    return row.dataset[field] || "";
  }

  function applyOrder(panel, order) {
    if (!order || !ORDER_FIELDS[order]) return;
    var holder = panel.querySelector("[data-rows]");
    if (!holder || holder.tagName !== "TBODY") return;
    var rows = [].slice.call(holder.querySelectorAll("tr.row"));
    var desc = order === "updated" || order === "created";
    rows.sort(function (a, b) {
      var av = sortKey(a, order), bv = sortKey(b, order);
      if (av < bv) return desc ? 1 : -1;
      if (av > bv) return desc ? -1 : 1;
      var ai = sortKey(a, "id"), bi = sortKey(b, "id");
      return ai < bi ? -1 : ai > bi ? 1 : 0;
    });
    rows.forEach(function (r) { holder.appendChild(r); });
  }

  function renderChips(panel, parsed, input) {
    var box = panel.querySelector(".chips");
    if (!box) return;
    box.innerHTML = "";
    function chip(label, isDefault, onRemove) {
      var c = document.createElement("span");
      c.className = "fchip" + (isDefault ? " is-default" : "");
      c.appendChild(document.createTextNode(label));
      var b = document.createElement("button");
      b.type = "button"; b.textContent = "×"; b.setAttribute("aria-label", "remove " + label);
      b.addEventListener("click", onRemove);
      c.appendChild(b);
      box.appendChild(c);
    }
    parsed.filters.forEach(function (t) {
      chip(t.key + ":" + t.vals.join(","), false, function () {
        input.value = rebuild(parsed, t, null, false); applyFilter(panel);
      });
    });
    if (parsed.order) {
      chip("order:" + parsed.order, false, function () {
        input.value = rebuild(parsed, null, null, true); applyFilter(panel);
      });
    }
    parsed.free.forEach(function (term) {
      chip(term, false, function () {
        input.value = rebuild(parsed, null, term, false); applyFilter(panel);
      });
    });
    var hasStatus = parsed.filters.some(function (t) { return t.key === "status"; });
    if (!hasStatus && isItemPanel(panel)) {
      chip("status:active", true, function () {
        input.value = (input.value.trim() + " status:all").trim(); applyFilter(panel);
      });
    }
    if (!parsed.order && isItemPanel(panel)) {
      chip("order:updated", true, function () {
        input.value = (input.value.trim() + " order:none").trim(); applyFilter(panel);
      });
    }
    if (input && input.value.trim() !== "") {
      var clr = document.createElement("button");
      clr.type = "button";
      clr.className = "fchip-clear";
      clr.textContent = "clear all";
      clr.setAttribute("aria-label", "clear all filters");
      clr.addEventListener("click", function () { input.value = ""; applyFilter(panel); });
      box.appendChild(clr);
    }
  }

  function rebuild(parsed, dropFilter, dropFree, dropOrder) {
    var parts = [];
    parsed.filters.forEach(function (t) { if (t !== dropFilter) parts.push(t.key + ":" + t.vals.join(",")); });
    if (parsed.order && !dropOrder) parts.push("order:" + parsed.order);
    parsed.free.forEach(function (f) { if (f !== dropFree) parts.push(f); });
    return parts.join(" ");
  }

  function applyFilter(panel) {
    var input = panel.querySelector(".filterbar input");
    var parsed = parseQuery(input ? input.value : "");
    collapseAll(panel);
    var total = 0, shown = 0;
    panel.querySelectorAll("[data-rows] .row, [data-rows] .doc").forEach(function (row) {
      var match = rowMatches(row, parsed);
      row.style.display = match ? "" : "none";
      total++;
      if (match) shown++;
    });
    applyOrder(panel, parsed.order || DEFAULT_ORDER);
    if (input) renderChips(panel, parsed, input);
    var count = panel.querySelector(".filter-count");
    if (count) count.textContent = shown + " / " + total;
    syncFilterToURL(panel);
  }

  function addFilterToken(panel, token) {
    var input = panel.querySelector(".filterbar input");
    if (!input || !token) return;
    var parts = input.value.trim().split(/\s+/).filter(Boolean);
    if (parts.indexOf(token) === -1) {
      input.value = (input.value.trim() + " " + token).trim();
    }
    applyFilter(panel);
  }

  function setFilterToken(panel, key, token) {
    var input = panel.querySelector(".filterbar input");
    if (!input) return;
    var parts = input.value.trim().split(/\s+/).filter(function (p) {
      return p && p.toLowerCase().indexOf(key + ":") !== 0;
    });
    parts.push(token);
    input.value = parts.join(" ");
    applyFilter(panel);
  }

  function initEngagedFilter() {
    document.addEventListener("click", function (e) {
      var b = e.target.closest("[data-filter-token]");
      if (!b) return;
      var panel = document.querySelector('.panel[data-topic="stories"]');
      if (!panel) return;
      e.preventDefault();
      var token = b.dataset.filterToken;
      if (location.hash !== "#stories") location.hash = "#stories";
      setFilterToken(panel, token.slice(0, token.indexOf(":")), token);
    });
  }

  function initFilters() {
    var params = new URLSearchParams(location.search);
    document.querySelectorAll(".panel").forEach(function (panel) {
      var input = panel.querySelector(".filterbar input");
      if (input) {
        var fromURL = panel.dataset.topic ? params.get(panel.dataset.topic) : null;
        if (fromURL !== null) input.value = fromURL;
        input.addEventListener("input", function () { applyFilter(panel); });
      }
      applyFilter(panel);
    });
  }

  function collapseAll(panel) {
    panel.querySelectorAll("tr.row[aria-expanded='true']").forEach(collapseRow);
  }
  function collapseRow(row) {
    row.setAttribute("aria-expanded", "false");
    var next = row.nextElementSibling;
    if (next && next.classList.contains("expansion")) next.remove();
  }
  function expandRow(row) {
    if (row.getAttribute("aria-expanded") === "true") return;
    row.setAttribute("aria-expanded", "true");
    var exp = document.createElement("tr");
    exp.className = "expansion";
    var td = document.createElement("td");
    td.colSpan = row.children.length;
    td.innerHTML = '<div class="expbody loading">loading…</div>';
    exp.appendChild(td);
    row.parentNode.insertBefore(exp, row.nextSibling);
    fetch(row.dataset.expandUrl, { headers: { "X-Requested-With": "fetch" } })
      .then(function (r) { return r.text(); })
      .then(function (html) { td.innerHTML = html; applyTimelineFields(); })
      .catch(function () { td.innerHTML = '<div class="expbody">failed to load</div>'; });
  }

  function toggleRow(row) {
    if (row.getAttribute("aria-expanded") === "true") collapseRow(row); else expandRow(row);
  }
  function copyId(el) {
    var id = el.dataset.id || el.textContent;
    function feedback() {
      el.classList.add("copied");
      el.textContent = "copied ✓";
      setTimeout(function () { el.textContent = id; el.classList.remove("copied"); }, 1000);
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(id).then(feedback, feedback);
    } else {
      feedback();
    }
  }

  function initExpand() {
    document.querySelectorAll(".panel").forEach(function (panel) {
      panel.addEventListener("click", function (e) {
        var chip = e.target.closest(".tagchip[data-filter]");
        if (chip) { e.preventDefault(); e.stopPropagation(); addFilterToken(panel, chip.dataset.filter); return; }
        var idEl = e.target.closest(".id-copy");
        if (idEl) { e.preventDefault(); e.stopPropagation(); copyId(idEl); return; }
        if (e.target.closest("a")) return;
        var row = e.target.closest("tr.row[data-expand-url]");
        if (row) toggleRow(row);
      });
      panel.addEventListener("keydown", function (e) {
        if (e.key !== "Enter" && e.key !== " ") return;
        if (e.target.closest(".tagchip[data-filter]")) return;
        var idEl = e.target.closest(".id-copy");
        if (idEl) { e.preventDefault(); copyId(idEl); return; }
        var row = e.target.closest("tr.row[data-expand-url]");
        if (row) { e.preventDefault(); toggleRow(row); }
      });
    });
  }

  function refetchPanel(topic) {
    var panel = document.querySelector('.panel[data-topic="' + topic + '"]');
    if (!panel) return;
    var holder = panel.querySelector("[data-rows]");
    if (!holder) return;
    var openUrls = [].slice.call(panel.querySelectorAll('tr.row[aria-expanded="true"]'))
      .map(function (r) { return r.dataset.expandUrl; });
    fetch("fragment/" + topic)
      .then(function (r) { return r.text(); })
      .then(function (html) {
        holder.innerHTML = html;
        applyFilter(panel);
        openUrls.forEach(function (url) {
          var row = holder.querySelector('tr.row[data-expand-url="' + url + '"]');
          if (row && row.style.display !== "none") expandRow(row);
        });
        if (topic.indexOf("wf-") === 0) {
          refreshPanelTab(topic);
        } else {
          var tab = document.querySelector('.tab[data-panel="' + topic + '"] .n');
          if (tab) tab.textContent = panel.querySelectorAll("[data-rows] .row").length;
        }
        if (topic === "stories") {
          refreshBacklogBadge();
          refreshEngagementBadge();
        }
      })
      .catch(function () {});
  }

  function refreshPanelTab(topic) {
    fetch("fragment/tab/" + topic)
      .then(function (r) { return r.text(); })
      .then(function (html) {
        var cur = document.querySelector('.tab[data-panel="' + topic + '"]');
        if (!cur) return;
        var tmp = document.createElement("div");
        tmp.innerHTML = html.trim();
        var next = tmp.querySelector(".tab");
        if (!next) return;
        cur.className = next.className;
        var n = cur.querySelector(".n"), nn = next.querySelector(".n");
        if (n && nn) n.textContent = nn.textContent;
      })
      .catch(function () {});
  }

  function refreshBacklogBadge() {
    fetch("fragment/backlog")
      .then(function (r) { return r.text(); })
      .then(function (html) {
        var tabEl = document.querySelector('.tab[data-panel="stories"]');
        if (!tabEl) return;
        var tmp = document.createElement("div");
        tmp.innerHTML = html.trim();
        var next = tmp.querySelector(".n-backlog");
        var cur = tabEl.querySelector(".n-backlog");
        if (next && cur) {
          cur.parentNode.replaceChild(next, cur);
        } else if (next) {
          tabEl.appendChild(next);
        } else if (cur) {
          cur.remove();
        }
      })
      .catch(function () {});
  }

  function refreshEngagementBadge() {
    fetch("fragment/engagement")
      .then(function (r) { return r.text(); })
      .then(function (html) {
        var tmp = document.createElement("div");
        tmp.innerHTML = html.trim();
        var cl = document.querySelector(".tabs .tab-cluster");
        ["n-engaged", "n-elsewhere"].forEach(function (cls) {
          var next = tmp.querySelector("." + cls);
          var cur = document.querySelector(".tabs ." + cls);
          if (next && cur && cur.parentNode) {
            cur.parentNode.replaceChild(next, cur);
          } else if (next && !cur && cl) {
            var after = cls === "n-engaged" ? cl.querySelector(".n-elsewhere") : null;
            if (after) {
              cl.insertBefore(next, after);
            } else {
              cl.appendChild(next);
            }
          } else if (!next && cur) {
            cur.remove();
          }
        });
      })
      .catch(function () {});
  }

  var LIVE_TOPICS = ["stories", "activity", "tasks", "queues"];
  var DERIVED_TOPICS = { stories: ["activity", "queues"], tasks: ["activity", "queues"] };

  function projectRowSlugs(root) {
    return [].slice.call(root.querySelectorAll("tr.row[data-slug]")).map(function (r) {
      return r.dataset.slug;
    });
  }
  function copyCellCounts(fromRow, toRow) {
    ["n-stories", "n-tasks", "n-workflows", "updated-cell"].forEach(function (cls) {
      var src = fromRow.querySelector("." + cls);
      var dst = toRow.querySelector("." + cls);
      if (src && dst) dst.innerHTML = src.innerHTML;
    });
  }

  function relPhrase(ms) {
    if (ms < 0) ms = 0;
    var mins = Math.floor(ms / 60000);
    if (mins < 1) return "just now";
    if (mins < 60) return mins === 1 ? "1 min ago" : mins + " mins ago";
    var hrs = Math.floor(mins / 60);
    if (hrs < 24) return hrs === 1 ? "1 hr ago" : hrs + " hrs ago";
    var days = Math.floor(hrs / 24);
    return days === 1 ? "1 day ago" : days + " days ago";
  }
  function renderRelTimes(root) {
    var now = Date.now();
    [].slice.call((root || document).querySelectorAll("time.rel-time[datetime]")).forEach(function (el) {
      var t = Date.parse(el.getAttribute("datetime"));
      if (!isNaN(t)) el.textContent = relPhrase(now - t);
    });
  }
  function applyProjectsLive(html) {
    var live = document.getElementById("projects-live");
    if (!live) return;
    var tmp = document.createElement("div");
    tmp.innerHTML = html;
    var oldSlugs = projectRowSlugs(live);
    var newSlugs = projectRowSlugs(tmp);
    var same = oldSlugs.length === newSlugs.length &&
      oldSlugs.length > 0 &&
      oldSlugs.every(function (s, i) { return s === newSlugs[i]; });
    if (same) {
      newSlugs.forEach(function (slug) {
        var src = tmp.querySelector('tr.row[data-slug="' + slug + '"]');
        var dst = live.querySelector('tr.row[data-slug="' + slug + '"]');
        if (src && dst) copyCellCounts(src, dst);
      });
    } else {
      live.innerHTML = html;
    }
    renderRelTimes(live);
    var nEl = document.querySelector(".n-partitions");
    if (nEl) nEl.textContent = String(newSlugs.length);
  }
  function refetchProjects() {
    fetch("fragment/projects")
      .then(function (r) { return r.text(); })
      .then(applyProjectsLive)
      .catch(function () {});
  }
  function applyLogsLive(html) {
    var live = document.getElementById("logs-live");
    if (live) live.innerHTML = html;
  }

  function initLive() {
    if (!window.EventSource) return;
    var dot = document.querySelector(".brand-mark");
    var isProjects = document.body.getAttribute("data-page") === "projects";
    var isLogs = document.body.getAttribute("data-page") === "logs";
    var detailEl = document.getElementById("detail-live");
    var detailKind = detailEl ? detailEl.dataset.kind : null;
    var detailId = detailEl ? detailEl.dataset.id : null;
    var detailTopic = detailEl ? topicForKind(detailKind) : null;

    wfPanelTopics().forEach(function (tp) {
      if (LIVE_TOPICS.indexOf(tp) === -1) LIVE_TOPICS.push(tp);
      if (DERIVED_TOPICS.stories.indexOf(tp) === -1) DERIVED_TOPICS.stories.push(tp);
    });
    var refetch = {};
    LIVE_TOPICS.forEach(function (tp) { refetch[tp] = debounce(function () { refetchPanel(tp); }, 250); });
    var refreshProjects = isProjects ? debounce(refetchProjects, 250) : null;
    var refreshLogs = isLogs ? debounce(function () {
      fetch("/fragment/logs" + location.search)
        .then(function (r) { return r.text(); })
        .then(applyLogsLive)
        .catch(function () {});
    }, 250) : null;
    var refreshDetail = detailEl ? debounce(function () {
      fetch("fragment/" + detailKind + "/" + detailId)
        .then(function (r) { return r.text(); })
        .then(function (html) {
          detailEl.innerHTML = html;
          applyTimelineFields();
          openHashDoc();
        })
        .catch(function () {});
    }, 250) : null;

    var live = window.__satelleLive = { open: false, opens: 0, leader: false, fallback: false, reconciles: 0, triggers: 0, stale: false };

    document.addEventListener("visibilitychange", function () {
      if (document.hidden || !live.stale) return;
      live.stale = false;
      reconcile();
    });

    function reconcile() {
      if (document.hidden) { live.stale = true; return; }
      live.reconciles++;
      if (isProjects) { if (refreshProjects) refreshProjects(); return; }
      if (isLogs) { if (refreshLogs) refreshLogs(); return; }
      LIVE_TOPICS.forEach(function (tp) { refetchPanel(tp); });
      if (refreshDetail) refreshDetail();
    }

    function handleTrigger(ev) {
      live.triggers++;
      if (document.hidden) { live.stale = true; return; }
      if (refetch[ev.data]) refetch[ev.data]();
      (DERIVED_TOPICS[ev.data] || []).forEach(function (tp) { if (refetch[tp]) refetch[tp](); });
      if (refreshDetail && (ev.data === detailTopic || (DERIVED_TOPICS[ev.data] || []).indexOf(detailTopic) !== -1)) refreshDetail();
      if (ev.data === "projects" && refreshProjects) refreshProjects();
      if (ev.data === "logs" && refreshLogs) refreshLogs();
    }

    var LOCK = "satelle-live";
    var bc = (navigator.locks && window.BroadcastChannel) ? new BroadcastChannel(LOCK) : null;
    live.fallback = !bc;

    var src = null;
    var state = null;
    var firstOpen = true;

    function post(msg) { if (bc && live.leader) bc.postMessage(msg); }
    function setState(s) {
      state = s;
      if (dot) dot.classList.toggle("sse-down", s === "down");
      post({ t: s });
    }

    function openStream() {
      if (src) return;
      var esURL = (location.pathname.indexOf("/r/") === 0) ? "/events" : "events";
      src = new EventSource(esURL);
      live.open = true;
      live.opens++;
      src.addEventListener("open", function () {
        setState("up");
        if (firstOpen) { firstOpen = false; return; }
        reconcile();
        post({ t: "reconcile" });
      });
      src.addEventListener("trigger", function (ev) {
        handleTrigger(ev);
        post({ t: "trigger", data: ev.data });
      });
      src.onerror = function () { setState("down"); };
    }

    function closeStream() {
      if (!src) return;
      src.close();
      src = null;
      live.open = false;
      firstOpen = false;
    }

    live.drop = function () { closeStream(); setState("down"); };
    live.reopen = function () { if (live.leader || live.fallback) openStream(); };

    if (!bc) {
      window.addEventListener("pagehide", closeStream);
      window.addEventListener("pageshow", function (e) { if (e.persisted) openStream(); });
      openStream();
      return;
    }

    var release = null;
    var waiting = null;

    function lead() {
      waiting = null;
      live.leader = true;
      openStream();
      return new Promise(function (resolve) { release = resolve; });
    }

    function claim() {
      navigator.locks.request(LOCK, { ifAvailable: true }, function (lock) {
        if (lock) return lead();
        firstOpen = false;
        bc.postMessage({ t: "hello" });
        waiting = new AbortController();
        navigator.locks.request(LOCK, { signal: waiting.signal }, lead).catch(function () {});
      }).catch(function () {});
    }

    bc.onmessage = function (e) {
      var m = e.data || {};
      if (live.leader) {
        if (m.t === "hello" && state) bc.postMessage({ t: state });
        return;
      }
      if (m.t === "trigger") handleTrigger(m);
      else if (m.t === "up" || m.t === "down") { if (dot) dot.classList.toggle("sse-down", m.t === "down"); }
      else if (m.t === "reconcile") reconcile();
    };

    window.addEventListener("pagehide", function () {
      if (live.leader) {
        closeStream();
        setState("down");
        live.leader = false;
        if (release) { release(); release = null; }
      }
      if (waiting) { waiting.abort(); waiting = null; }
    });
    window.addEventListener("pageshow", function (e) { if (e.persisted) claim(); });

    claim();
  }

  function currentTheme() {
    return document.documentElement.getAttribute("data-theme") === "dark" ? "dark" : "light";
  }
  function applyTheme(theme, persist) {
    if (theme === "dark") document.documentElement.setAttribute("data-theme", "dark");
    else document.documentElement.removeAttribute("data-theme");
    try { localStorage.setItem("satelle-theme", theme); } catch (e) {}
    var btn = document.getElementById("theme-toggle");
    if (btn) btn.textContent = theme === "dark" ? "☀" : "☾";
    if (persist) {
      try { fetch("theme", { method: "POST", headers: { "Content-Type": "application/x-www-form-urlencoded" }, body: "theme=" + theme }).catch(function () {}); } catch (e) {}
    }
  }
  function initTheme() {
    applyTheme(currentTheme());
    var btn = document.getElementById("theme-toggle");
    if (btn) btn.addEventListener("click", function () {
      applyTheme(currentTheme() === "dark" ? "light" : "dark", true);
    });
  }


  function openHashDoc() {
    var hash = (location.hash || "").replace(/^#/, "");
    if (!hash || hash.indexOf("doc-") !== 0) return;
    var el = document.getElementById(hash);
    if (!el) return;
    if (el.classList.contains("doc-item")) {
      el.open = true;
      el.scrollIntoView({ block: "start" });
      return;
    }
    if (el.classList.contains("route-doc") || el.id === "doc-route") {
      el.scrollIntoView({ block: "start" });
    }
  }

  function consumeExpandParam() {
    if (!pendingExpand) return;
    var id = pendingExpand;
    pendingExpand = null;
    showTab((location.hash || "#stories").slice(1));
    var row = document.querySelector('tr.row[data-expand-url$="' + id + '"]');
    if (!row) return;
    expandRow(row);
    try { row.scrollIntoView({ block: "nearest" }); } catch (e) {}
  }

  function initProjectSwitcher() {
    document.addEventListener("click", function (e) {
      document.querySelectorAll("details.proj-switch[open]").forEach(function (d) {
        if (!d.contains(e.target)) d.removeAttribute("open");
      });
    });
    document.addEventListener("keydown", function (e) {
      if (e.key !== "Escape") return;
      document.querySelectorAll("details.proj-switch[open]").forEach(function (d) {
        d.removeAttribute("open");
        var s = d.querySelector("summary");
        if (s) s.focus();
      });
    });
  }

  var TLFIELDS = ["walltime", "tokens", "model", "outcome"];
  function tlFieldsState() {
    var s = {};
    try { s = JSON.parse(localStorage.getItem("satelle-tlfields") || "{}"); } catch (e) {}
    var out = {};
    TLFIELDS.forEach(function (f) { out[f] = s[f] !== false; });
    return out;
  }
  function applyTimelineFields() {
    var st = tlFieldsState();
    document.querySelectorAll("ol.timeline").forEach(function (tl) {
      TLFIELDS.forEach(function (f) { tl.classList.toggle("hide-" + f, !st[f]); });
    });
    document.querySelectorAll("input[data-tlfield]").forEach(function (cb) {
      cb.checked = st[cb.getAttribute("data-tlfield")] !== false;
    });
  }
  function initTimelineFields() {
    document.addEventListener("change", function (e) {
      var cb = e.target;
      if (!cb || !cb.getAttribute || !cb.getAttribute("data-tlfield")) return;
      var st = tlFieldsState();
      st[cb.getAttribute("data-tlfield")] = cb.checked;
      try { localStorage.setItem("satelle-tlfields", JSON.stringify(st)); } catch (e2) {}
      applyTimelineFields();
    });
    applyTimelineFields();
  }

  document.addEventListener("DOMContentLoaded", function () {
    initTheme();
    initTabs();
    initExpand();
    initEngagedFilter();
    initFilters();
    consumeExpandParam();
    openHashDoc();
    window.addEventListener("hashchange", openHashDoc);
    initLive();
    initProjectSwitcher();
    initTimelineFields();
    initRelTimes();
  });

  function initRelTimes() {
    renderRelTimes(document);
    setInterval(function () { renderRelTimes(document); }, 30000);
  }
})();
