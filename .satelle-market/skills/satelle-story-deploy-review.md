---
name: satelle-story-deploy-review
scope: project
type: skill
tags: [solo-dev, reviewer, gate, deploy]
description: Reviewer gate for deploy evidence when a workflow includes a deploy step. Isolated read-only judge of recorded deploy outcomes.
---

# Deploy gate (functional check)

**Functional-check** gate. The check is the embedded ```check script below —
self-contained (references no external script or file). satelle runs it in
the repo root on `integrated → deployed`; exit 0 accepts, non-zero rejects
with the output tail as notes.

Does a **real local deploy**: build the binary, install it, (re)start the
background service via satelle's own `service install` mechanism, then
health-check both surfaces and **leave the service running**. After this
gate passes, the project page is live on the service port. Local-first — the
service is a local systemd user unit, so a real deploy has no production
blast radius.

```check
#!/usr/bin/env bash
set -euo pipefail
BIN="${HOME}/.local/bin/satelle"

# 1. Build + install the deploy artifact onto PATH (no Makefile dependency).
tmp="$(mktemp)"
go build -o "${tmp}" ./cmd/satelle
install -m 0755 "${tmp}" "${BIN}"
rm -f "${tmp}"

# 2. Deploy: (re)install/restart the background service (satelle's own mechanism),
#    or fall back to a detached serve when systemd is unavailable.
if command -v systemctl >/dev/null 2>&1 && "${BIN}" service install --session <session> >/tmp/satelle-deploy.log 2>&1; then
  echo "deploy: systemd service (re)installed."
else
  PORT="$("${BIN}" service status --session <session> 2>/dev/null | awk -F'[: ]+' '/^config:/{for(i=1;i<=NF;i++) if($i=="port") print $(i+1)}')"
  PORT="${PORT:-8787}"
  echo "deploy: no systemd — detached serve on :${PORT}."
  setsid nohup "${BIN}" serve --addr 0.0.0.0 --port "${PORT}" >/tmp/satelle-serve.log 2>&1 & disown || true
fi

# 3. Resolve the live URL and health-check the web UI.
URL="$("${BIN}" service status --session <session> 2>/dev/null | awk '/^url:/{print $2}')"
URL="${URL:-http://localhost:8787}"
ok=""
for _ in $(seq 1 40); do
  if curl -fsS "${URL}/healthz" 2>/dev/null | grep -q ok; then ok=1; break; fi
  sleep 0.5
done
[ -n "${ok}" ] || { echo "deploy: web /healthz never came up at ${URL}" >&2; exit 1; }
page="$(curl -fsS "${URL}/" || true)"
for m in 'data-panel="stories"' 'data-panel="workflow"' 'class="tabs"'; do
  case "${page}" in *"${m}"*) : ;; *) echo "deploy: web UI missing ${m} at ${URL}" >&2; exit 1;; esac
done
echo "deploy: web UI healthy and LIVE at ${URL}."

# 4. Health-check the CLI.
"${BIN}" status --session <session> >/dev/null 2>&1 || { echo "deploy: 'satelle status --session <session>' failed" >&2; exit 1; }
echo "deploy: PASS — service deployed and LIVE at ${URL} (web + CLI healthy)."
```
