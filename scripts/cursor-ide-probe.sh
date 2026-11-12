#!/usr/bin/env bash
# cursor-ide-probe.sh — prove satelle's cursor hooks in the Cursor IDE's own
# agent (sto_bf491f0d). The IDE has no headless mode, so a person runs it:
#
#   scripts/cursor-ide-probe.sh setup [dir]    # build, make two scratch repos, print the prompts
#   ... open each repo in the Cursor IDE and give its agent the two prompts ...
#   scripts/cursor-ide-probe.sh collect [dir]  # write <dir>/cursor-ide-run.md from what was recorded
#   scripts/cursor-ide-probe.sh clean [dir]    # release the scratch seats and remove <dir>
#
# The hooks in the scratch repos call `record`, which passes each hook call to
# the satelle binary built from this tree and writes what went in and came out:
#
#   scripts/cursor-ide-probe.sh record <capture dir> <label> [command...]
#
# Run setup, collect and clean from your own terminal, not from an agent
# session: the scratch repos are git working trees outside the temp roots,
# which a seated agent session is fenced from writing. Linux only: the
# ancestor check reads /proc.
#
# Environment: CURSOR_IDE_DIR (default /usr/share/cursor) is the installed IDE.
set -u

here=$(cd "$(dirname "$0")" && pwd)
self="$here/cursor-ide-probe.sh"
tree=$(cd "$here/.." && pwd)
ide_dir=${CURSOR_IDE_DIR:-/usr/share/cursor}
default_dir="${XDG_CACHE_HOME:-$HOME/.cache}/satelle-cursor-ide-probe"
repos="wired claude-only"
# The events satelle installs nothing on through which a shell command or an
# edit can be announced; the wired repo observes each.
observed="beforeShellExecution afterShellExecution afterFileEdit postToolUse postToolUseFailure"
# The environment names each capture records, set or unset.
env_names="SATELLE_SESSION SATELLE_PROJECT_DIR SATELLE_HOME CLAUDE_PROJECT_DIR CURSOR_PROJECT_DIR CURSOR_VERSION CURSOR_AGENT CURSOR_INVOKED_AS CLAUDECODE PATH"

die() { echo "cursor-ide-probe: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is not installed"; }

# sat runs the scratch binary from inside a repo, never in an inherited
# satelle session or project, nor as the agent harness the terminal may be in.
sat() {
	(cd "$1" && shift && env -u SATELLE_SESSION -u SATELLE_PROJECT_DIR -u CLAUDECODE -u CURSOR_AGENT \
		-u CURSOR_INVOKED_AS -u PI_CODING_AGENT -u GROK_AGENT "$bin" "$@")
}

# observer_answer is what an observer prints: what this machine's own
# user-level entry on that event answers.
observer_answer() {
	case $1 in
	observe@beforeShellExecution) printf '%s' '{"permission":"allow"}' ;;
	*) printf '%s' '{}' ;;
	esac
}

# ancestors prints the executable of each ancestor of pid, nearest first.
ancestors() {
	local pid=$1 n=0
	while [ "$pid" -gt 1 ] 2>/dev/null && [ $n -lt 64 ]; do
		readlink "/proc/$pid/exe" 2>/dev/null || echo "not read"
		local stat
		stat=$(cat "/proc/$pid/stat" 2>/dev/null) || break
		stat=${stat##*) }
		set -- $stat
		pid=$2
		n=$((n + 1))
	done
}

# ide_runtime prints the executable $ide_dir/cursor execs when that launcher
# is a shell script (`exec /usr/lib/${name}/electron ...`), else nothing.
ide_runtime() {
	local l=$ide_dir/cursor name path
	[ -f "$l" ] && head -c 2 "$l" | grep -q '#!' || return 0
	name=$(sed -n 's/^name=//p' "$l" | head -1)
	path=$(grep -o 'exec [^ ]*' "$l" | tail -1 | cut -d' ' -f2)
	path=${path//\$\{name\}/$name}
	case $path in /*) printf '%s' "$path" ;; esac
}

cmd_record() {
	[ $# -ge 2 ] || die "usage: record <capture dir> <label> [command...]"
	local cap=$1 label=$2
	shift 2
	need jq
	mkdir -p "$cap"
	local payload out code
	payload=$(cat)
	if [ $# -gt 0 ]; then
		out=$(printf '%s' "$payload" | "$@")
		code=$?
	else
		out=$(observer_answer "$label")
		code=0
	fi
	local envjson='{}' name
	for name in $env_names; do
		if [ -n "${!name+x}" ]; then
			envjson=$(jq -c --arg k "$name" --arg v "${!name}" '. + {($k): $v}' <<<"$envjson")
		else
			envjson=$(jq -c --arg k "$name" '. + {($k): null}' <<<"$envjson")
		fi
	done
	local anc bare=false
	if [ -d /proc/self ]; then anc=$(ancestors "$PPID"); else anc="not read"; fi
	command -v satelle >/dev/null 2>&1 && bare=true
	jq -n --arg label "$label" --arg payload "$payload" --arg stdout "$out" --argjson exit "$code" \
		--arg cwd "$PWD" --arg anc "$anc" --argjson env "$envjson" --argjson bare "$bare" \
		'{label: $label, payload: ($payload | fromjson? // $payload), stdout: $stdout, exit: $exit,
		  cwd: $cwd, ancestors: ($anc | split("\n") | map(select(. != ""))), env: $env, bare_satelle: $bare}' \
		>"$cap/$(date +%s%N)-$$.json"
	printf '%s\n' "$out"
	exit "$code"
}

# rewrite points every installed `satelle hook <verb> ...` command of a hooks
# file at record, labelled <verb>@<event>, wrapping the scratch binary. The
# command still holds "satelle hook ", so satelle reads the file as before.
rewrite() {
	local file=$1 cap=$2 tmp
	tmp=$(mktemp)
	jq --arg pre "$self record $cap" --arg bin "$bin" '
		.hooks |= with_entries(.key as $ev | .value |= walk(
			if type == "object" and ((.command? | type) == "string") and (.command | startswith("satelle hook ")) then
				(.command | ltrimstr("satelle hook ")) as $rest
				| .command = ($pre + " " + ($rest | split(" ")[0]) + "@" + $ev + " " + $bin + " hook " + $rest)
			else . end))' "$file" >"$tmp" && mv "$tmp" "$file"
}

cmd_setup() {
	local dir=${1:-$default_dir}
	need jq
	need git
	need go
	[ -e "$dir" ] && die "$dir exists; run clean first"
	mkdir -p "$dir/bin" "$dir/captures"
	dir=$(cd "$dir" && pwd -P)
	bin="$dir/bin/satelle"
	(cd "$tree" && go build -o "$bin" ./cmd/satelle) || die "go build failed"
	local r repo id
	for r in $repos; do
		repo="$dir/$r"
		mkdir -p "$repo"
		git -C "$repo" init -q && git -C "$repo" commit -q --allow-empty -m init || die "git init in $repo failed"
		sat "$repo" init >/dev/null || die "satelle init in $repo failed"
		printf '[agents.hands]\ncommand = "session"\n' >"$repo/.satelle/agents.toml"
		printf 'Work.\n' >"$repo/.satelle/skills/work.md"
		cat >"$repo/.satelle/workflows/default.toml" <<'EOF'
name = "default"
[states.todo]
start = true
[states.doing]
performer = { agent = "hands", skill = "work" }
[states.finished]
terminal = true
[[transition]]
from = ["todo"]
to = "doing"
[[transition]]
from = ["doing"]
to = "finished"
EOF
		printf '[workflows]\ndefault = "default"\n' >"$repo/.satelle/satelle.toml"
		cat >"$repo/.satelle/doctor.toml" <<'EOF'
# Every open probe story is a doctor issue, so the session-start context
# always holds a satelle doctor block.
[[items]]
name = "probe"
kind = "story"
tag = "probe"
fix = "satelle story get {id}"
EOF
		sat "$repo" validate >/dev/null || die "satelle validate in $repo failed"
		id=$(sat "$repo" story create --title "cursor IDE probe" --tag probe --json | jq -r .id) || die "story create in $repo failed"
		printf '%s\n' "$id" >"$dir/$r.id"
	done
	sat "$dir/wired" agents install cursor >/dev/null || die "agents install cursor failed"
	sat "$dir/claude-only" agents install claude >/dev/null || die "agents install claude failed"
	rewrite "$dir/wired/.cursor/hooks.json" "$dir/captures/wired"
	rewrite "$dir/claude-only/.claude/settings.json" "$dir/captures/claude-only"
	local ev tmp
	for ev in $observed; do
		tmp=$(mktemp)
		jq --arg ev "$ev" --arg cmd "$self record $dir/captures/wired observe@$ev" \
			'.hooks[$ev] = ((.hooks[$ev] // []) + [{command: $cmd}])' "$dir/wired/.cursor/hooks.json" >"$tmp" &&
			mv "$tmp" "$dir/wired/.cursor/hooks.json"
	done
	: >"$dir/replies.md"
	local w c
	w=$(cat "$dir/wired.id")
	c=$(cat "$dir/claude-only.id")
	cat <<EOF
Scratch repos are ready in $dir.

For each repo, in order: quit every Cursor window (a running Cursor adopts a
new window into its own environment), then launch:

  env -u SATELLE_SESSION -u SATELLE_PROJECT_DIR $ide_dir/cursor $dir/wired

Open the agent pane (default agent mode, new chat) and give these two prompts.

Prompt 1:
  Read no files. Quote verbatim these lines of your context: the line starting
  \`satelle session:\`, the line starting \`seats\`, the line starting
  \`satelle doctor:\` and the line holding \`open:probe\`. Then, with your file
  write tool (not the shell), create \`unseated.txt\` containing \`x\`. Then, with
  your shell, run \`echo x > unseated-shell.txt\`. Report each outcome and any
  refusal reason verbatim.

Prompt 2 (same chat):
  With your shell run \`$bin story set $w --status doing\`, then
  \`$bin story seat $w --session <the id you quoted>\`. Then, with your file
  write tool (not the shell), create \`seated.txt\` containing \`ok\`. Then run
  \`$bin status\`. Report each command's output verbatim.

Then quit Cursor, launch it on $dir/claude-only the same way, and give the
same two prompts with the story id $c in place of $w.

Paste every reply into $dir/replies.md, under a heading naming the repo and
the prompt, then run: $self collect $dir
EOF
}

cmd_collect() {
	local dir=${1:-$default_dir}
	need jq
	[ -d "$dir/captures" ] || die "$dir/captures does not exist; run setup and the IDE prompts first"
	dir=$(cd "$dir" && pwd -P)
	bin="$dir/bin/satelle"
	local all
	all=$(find "$dir/captures" -name '*.json' -type f | sort)
	[ -n "$all" ] || die "no captures in $dir/captures: the IDE ran no recorded hook"
	# Refusals: the evidence must be the IDE's own session and repo. A
	# packaged IDE whose launcher is a script (Arch's cursor-bin execs
	# /usr/lib/electron<N>/electron) shows that runtime as the ancestor, not a
	# binary under $ide_dir; it counts only when the hook also saw
	# CURSOR_VERSION equal to the installed IDE's version.
	local version runtime
	version=$(jq -r '.version // "unknown"' "$ide_dir/resources/app/product.json" 2>/dev/null || echo unknown)
	runtime=$(ide_runtime)
	local f bad=""
	for f in $all; do
		if jq -e '.env.SATELLE_SESSION != null or .env.SATELLE_PROJECT_DIR != null' "$f" >/dev/null; then
			bad="$bad\n  $f: SATELLE_SESSION or SATELLE_PROJECT_DIR was set in the hook's environment"
		fi
		if jq -e '.ancestors | any(test("cursor-agent"))' "$f" >/dev/null; then
			bad="$bad\n  $f: a cursor-agent process is among the hook's ancestors"
		elif ! jq -e --arg ide "$ide_dir/" --arg rt "$runtime" --arg ver "$version" '
			(.ancestors | any(startswith($ide)))
			or ($rt != "" and (.ancestors | any(. == $rt)) and .env.CURSOR_VERSION == $ver)' "$f" >/dev/null; then
			bad="$bad\n  $f: no process of the Cursor IDE ($ide_dir) is among the hook's ancestors"
		fi
	done
	if [ -n "$bad" ]; then
		printf 'cursor-ide-probe: collect refused:%b\n' "$bad" >&2
		echo "Launch the IDE with: env -u SATELLE_SESSION -u SATELLE_PROJECT_DIR $ide_dir/cursor <repo>" >&2
		exit 1
	fi
	local out="$dir/cursor-ide-run.md"
	{
		echo "# Cursor IDE run (sto_bf491f0d)"
		echo
		echo "- Cursor IDE version (product.json): $version"
		echo "- cursor_version in the payloads: $(jq -r '.payload.cursor_version? // empty' $all | sort -u | paste -sd, -)"
		echo "- collected: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
		local r capdir id caps
		for r in $repos; do
			capdir="$dir/captures/$r"
			id=$(cat "$dir/$r.id" 2>/dev/null || echo "")
			echo
			echo "## $r"
			echo
			echo "Story: ${id:-none}"
			echo
			echo "### Files"
			echo
			for f in unseated.txt unseated-shell.txt; do
				if [ -e "$dir/$r/$f" ]; then echo "- $f: exists"; else echo "- $f: does not exist"; fi
			done
			if [ -e "$dir/$r/seated.txt" ]; then
				echo "- seated.txt: $(tr -d '\n' <"$dir/$r/seated.txt")"
			else
				echo "- seated.txt: does not exist"
			fi
			caps=$(find "$capdir" -name '*.json' -type f 2>/dev/null | sort)
			echo
			echo "### Shell commands"
			echo
			echo "| command | events | gate answer |"
			echo "|---|---|---|"
			if [ -n "$caps" ]; then
				jq -s -r '
					map(. as $c | ($c.payload | if type == "object" then (.tool_input.command? // .tool_input.cmd? // .command? // empty) else empty end) as $cmd
						| select($cmd != null and $cmd != "") | {cmd: $cmd, label: $c.label, stdout: $c.stdout})
					| group_by(.cmd)[]
					| (map(.label) | unique | join(", ")) as $events
					| (map(select(.label | startswith("gate@"))) ) as $gates
					| "| `\(.[0].cmd | gsub("\\|"; "\\\\|"))` | \($events) | \(if ($gates | length) == 0 then "NO GATE CAPTURE" else ($gates | map(if .stdout == "" then "(empty: allow)" else .stdout end) | unique | join(" / ")) end) |"
				' $caps
			fi
			echo
			echo "### Ledger"
			echo
			echo '```'
			if [ -n "$id" ] && [ -x "$bin" ]; then
				sat "$dir/$r" ledger list "$id" 2>&1
				echo "--- item.transitioned"
				sat "$dir/$r" ledger list "$id" --kind item.transitioned 2>&1
			else
				echo "not read"
			fi
			echo '```'
			if [ "$r" = wired ] && [ -x "$bin" ]; then
				echo
				echo "### satelle doctor"
				echo
				echo '```'
				sat "$dir/$r" doctor 2>&1
				echo '```'
			fi
			echo
			echo "### Captures"
			for f in $caps; do
				echo
				echo "#### $(jq -r .label "$f")"
				echo
				echo '```json'
				jq '.payload |= (if type == "object" then del(.user_email) | (if (.prompt? | type) == "string" then .prompt |= (split("\n")[0]) else . end) else . end)' "$f"
				echo '```'
			done
		done
		echo
		echo "## This machine's own hooks"
		echo
		local uf
		for uf in "$HOME/.cursor/hooks.json" "$HOME/.claude/settings.json"; do
			if [ -f "$uf" ]; then
				echo "- $uf: events $(jq -r '.hooks // {} | keys | join(", ")' "$uf"); satelle command: $(grep -q 'satelle hook ' "$uf" && echo yes || echo no)"
				jq -r '.hooks // {} | to_entries[] | "  - \(.key): " + ([.value[] | (.command? // (.hooks? // [] | map(.command) | join("; ")) // "")] | join("; ") | if test("permission.:.allow") then "answers allow" elif test("continue.:true") then "answers continue" else "answers {}" end)' "$uf" 2>/dev/null
			else
				echo "- $uf: absent"
			fi
		done
		echo
		echo "## Replies"
		echo
		cat "$dir/replies.md" 2>/dev/null
		echo
		echo "## Differences"
		echo
		echo "| id | difference | capture | fix (file, test) or help sentence |"
		echo "|---|---|---|---|"
	} | sed "s#$HOME#/work#g" >"$out"
	echo "wrote $out"
}

cmd_clean() {
	local dir=${1:-$default_dir}
	need jq
	[ -d "$dir" ] || die "$dir does not exist"
	dir=$(cd "$dir" && pwd -P)
	bin="$dir/bin/satelle"
	local r id s
	for r in $repos; do
		id=$(cat "$dir/$r.id" 2>/dev/null) || continue
		for s in $(jq -r '.payload.session_id? // .payload.conversation_id? // empty' "$dir/captures/$r"/*.json 2>/dev/null | sort -u); do
			sat "$dir/$r" story release "$id" --session "$s" >/dev/null 2>&1 || true
		done
	done
	rm -rf "$dir"
	echo "removed $dir"
}

case ${1:-} in
setup) shift; cmd_setup "$@" ;;
record) shift; cmd_record "$@" ;;
collect) shift; cmd_collect "$@" ;;
clean) shift; cmd_clean "$@" ;;
*) die "usage: $0 setup|collect|clean [dir], or record <capture dir> <label> [command...]" ;;
esac
