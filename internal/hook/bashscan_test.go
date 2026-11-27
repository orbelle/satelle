package hook

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestScanBashTargets(t *testing.T) {
	noFile := func(string) bool { return false }
	for _, c := range []struct {
		cmd    string
		paths  []string
		inline []string
	}{
		{cmd: "ls -la; cat a | grep b"},
		{cmd: "echo x > a.txt", paths: []string{"/w/a.txt"}},
		{cmd: "echo x 2>&1 | tee -a log", paths: []string{"/w/log"}},
		{cmd: `git commit -m "rm -rf /etc > /x"`},
		{cmd: "cd sub && touch f", paths: []string{"/w/sub/f"}},
		{cmd: "cp a b c /dst/", paths: []string{"/dst"}},
		{cmd: "mv -t /dst a b", paths: []string{"/dst"}},
		{cmd: "sed -i 's/a/b/' x y", paths: []string{"/w/x", "/w/y"}},
		{cmd: "sed 's/a/b/' x"},
		{cmd: "dd if=/dev/zero of=/w/img", paths: []string{"/w/img"}},
		{cmd: "git -C /other status"},
		{cmd: "git -C /other add .", paths: []string{"/other"}},
		{cmd: "git -C '/other' log --oneline -1"},
		{cmd: `git -C "/other" log --oneline -1`},
		{cmd: "git -C '/other' status"},
		{cmd: `git -C "/other" status`},
		{cmd: "git -C '/other' diff"},
		{cmd: `git -C "/other" diff`},
		{cmd: "git -C '/my repo' status"},
		{cmd: "git -C '/other' add .", paths: []string{"/other"}},
		{cmd: `git -C "/other" commit -m x`, paths: []string{"/other"}},
		{cmd: "git -C '/my repo' add .", paths: []string{"/my repo"}},
		{cmd: "git -C '/other' diff --output=x", paths: []string{"/other"}},
		{cmd: "make -C 'sub' all", paths: []string{"/w/sub"}},
		{cmd: `tar -C "/dst" -xf a.tar`, paths: []string{"/dst"}},
		{cmd: `bash -c 'rm -f q'`, paths: []string{"/w/q"}},
		{cmd: `python3 -c 'print(1)'`, inline: []string{"/w"}},
		{cmd: "python3 script.py"},
		{cmd: "cat <<'EOF' | python3 -\nprint(1)\nEOF", inline: []string{"/w"}},
		{cmd: "chmod +x /tmp/x.sh", paths: []string{"/tmp/x.sh"}},
		{cmd: "chmod u+w f", paths: []string{"/w/f"}},
		{cmd: "chmod a=r f g", paths: []string{"/w/f", "/w/g"}},
		{cmd: "chmod 755 f", paths: []string{"/w/f"}},
		{cmd: "chmod 0644 f", paths: []string{"/w/f"}},
		{cmd: "chmod u=rwx,g=rx,o= f", paths: []string{"/w/f"}},
		{cmd: "chmod -R go-w dir", paths: []string{"/w/dir"}},
		{cmd: "chmod -w f", paths: []string{"/w/f"}},
		{cmd: "chmod -t f", paths: []string{"/w/f"}},
		{cmd: "chmod --reference=ref f", paths: []string{"/w/f"}},
		{cmd: "chmod -- +x f", paths: []string{"/w/f"}},
		{cmd: `chmod "+x" f`, paths: []string{"/w/f"}},
		{cmd: "chmod +x"},
		{cmd: "chmod notamode f", paths: []string{"/w/notamode", "/w/f"}},
		{cmd: "chmod /tmp/scratch +x", paths: []string{"/tmp/scratch", "/w/+x"}},
		{cmd: "chmod notamode 755", paths: []string{"/w/notamode", "/w/755"}},
		{cmd: "chmod a=r f +x", paths: []string{"/w/f", "/w/+x"}},
		{cmd: "chmod -w +x", paths: []string{"/w/+x"}},
		{cmd: "chmod --reference=ref +x", paths: []string{"/w/+x"}},
		{cmd: "echo x \\\n  > a.txt", paths: []string{"/w/a.txt"}},
		{cmd: "cd sub \\\n  && touch f", paths: []string{"/w/sub/f"}},
		{cmd: "bash -c 'X+=1 command true; rm /o/f'", paths: []string{"/o/f"}},
		{cmd: "bash -c 'X+=1 >/dev/null true; echo x > /o/f'", paths: []string{"/dev/null", "/o/f"}},
	} {
		got := ScanBash(c.cmd, "/w", "", noFile)
		if !slices.Equal(got.Paths, c.paths) || !slices.Equal(got.Inline, c.inline) {
			t.Errorf("%q: paths %v inline %v, want %v %v", c.cmd, got.Paths, got.Inline, c.paths, c.inline)
		}
	}
}

var operatorRecordCases = []string{
	"satelle story record sto_1 --file f",
	"satelle -C /w story record sto_1 --file f",
	"satelle --session s story record sto_1",
	"satelle story -C. record sto_1",
	"satelle story -C=/w record sto_1",
	"satelle story --session=s record sto_1",
	"sh -c 'satelle task record t_1 --file f'",
	"FOO=1 /usr/local/bin/satelle story record sto_1 --file f",
	"env -u SATELLE_SESSION satelle story record sto_1 --file f",
	"command satelle story record sto_1",
	"timeout 30 satelle story record sto_1",
	"xargs satelle story record < ids",
	"satelle --config c.toml story record sto_1",
	`script -qc "satelle story record sto_1 --file f" /dev/null`,
	`python3 -c "import subprocess; subprocess.run(['satelle','story','record','sto_1'])"`,
	"bash <<'EOF'\nsatelle story record sto_1 --file f\nEOF",
	"echo ok && nohup satelle story record sto_1 --file f",
	`satelle story "record" sto_1`,
	`"satelle" story record sto_1`,
	"(satelle story record sto_1)",
	"echo $(satelle story record sto_1)",
	"echo ok & satelle story record sto_1",
}

var operatorRecordCodeCases = []string{
	"zsh -c 'satelle story record sto_1'",
	`node -e "require('child_process').execSync('satelle story record sto_1')"`,
	"echo 'satelle story record sto_1' | bash",
	`echo 'satelle story record sto_1' | "bash"`,
	"printf 'satelle story record sto_1' | xargs -I{} sh -c '{}'",
	`echo "satelle story record sto_1" | grep satelle | sh`,
	"cat <<'EOF' | bash\nsatelle story record sto_1\nEOF",
	"bash <<< 'satelle story record sto_1'",
	"cat <<< x\nsatelle story record sto_1",
	"cat <<EOF\n$(satelle story record sto_1)\nEOF",
	`git commit -m "$(satelle story record sto_1)"`,
	`echo "$(satelle story record sto_1)"`,
	"GIT_EDITOR='satelle story record sto_1' git commit",
	"git -c core.editor='satelle story record sto_1' commit",
	"{ echo a; echo 'satelle story record sto_1'; } | bash",
	"while true; do echo 'satelle story record sto_1'; done | sh",
}

var operatorRecordDataCases = []string{
	`git commit -m "fix(hook): the satelle story record guard reads prose"`,
	"git commit -m \"$(cat <<'EOF'\nfix: satelle story record guard\nEOF\n)\"",
	`echo "run satelle story record sto_1 from your terminal"`,
	"echo satelle story record",
	`printf '%s\n' "satelle story record"`,
	`grep -rn "satelle story record" internal/`,
	`git log --oneline | grep "satelle story record"`,
	`cat "docs/satelle story record notes.md"`,
	`satelle story raise --title "Guard" --body "the operator runs satelle story record"`,
	"cat <<'EOF'\nsatelle story record sto_1\nEOF",
}

var notOperatorRecordCases = []string{
	"satelle story attach sto_1 --name record --file f",
	"satelle story attach sto_1 --session record --file f",
	"satelle story get sto_1",
	"git log --grep record",
	"echo record; satelle status",
}

var operatorRecordStoryTextCases = []string{
	"satelle story set sto_1 --title 'A run record folder (0444) left by a killed run' --criteria 'Given a satelle run, When its record folder (0444) is left by a killed run, Then the next run removes it' --replace --session s",
	`satelle story create --title "The guard lets a story set whose text holds parentheses and the word record through" --criteria "Given a satelle story set (for example 'A run record folder (0444) left by a killed run'), When it runs, Then the gate allows it" --json --session s`,
	"satelle story create \\\n  --title \"The guard lets a story set whose text holds parentheses and the word record through\" \\\n  --criteria \"Given a satelle story set (for example 'A run record folder (0444) left by a killed run'), When it runs, Then the gate allows it\" \\\n  --json --session s",
	`satelle story set sto_1 --criteria 'Given a satelle run, When the run'\''s record folder (0444) is left, Then it is removed' --replace --session s`,
	`satelle story set sto_1 --criteria 'Given a satelle story set, When its text holds the word '\''record'\'' (in parentheses), Then it is allowed' --replace --session s`,
	"satelle story set sto_1 --body - --session s <<'EOF'\nThe operator runs satelle story record (the operator verb) in their own terminal.\nEOF",
	"satelle story create --title 'Guard (parentheses)' \\\n  --body - --json --session s <<'EOF'\nThe operator runs satelle story record (the operator verb) in their own terminal.\nEOF",
	"cat <<'EOF' | satelle story set sto_1 --body - --session s\nThe operator runs satelle story record (the operator verb) in their own terminal.\nEOF",
	`satelle story set sto_1 --criteria 'Given (0444)'"record"`,
	`satelle story set sto_1 --title 'a (b) record'"x"`,
	`satelle story set sto_1 --body "the record (x)" --session s`,
}

var operatorRecordEscapedCases = []string{
	"satelle story \\\nrecord sto_1 --file f",
	"echo ok && satelle story \\\n  record sto_1",
	"echo ok; satelle story record sto_1",
	`satelle story rec\ord sto_1`,
	"satelle story re'cord' sto_1",
	"(echo 'satelle story record sto_1') | bash",
	`satelle story set sto_1 --title "note (x) $(satelle story record sto_1)"`,
	`echo "a"$(satelle story record sto_1)`,
	`echo 'x'$(satelle story record sto_1)`,
	"echo \"a\"`satelle story record sto_1`",
	`echo $(satelle story record)"x"`,
	`echo $(satelle story 'record')x`,
	`echo $(satelle story 'record')$(echo ok)`,
	`echo ok&satelle story record sto_1`,
	`satelle story record&`,
	`satelle story record& echo done`,
	`satelle story "record">out sto_1`,
	`satelle story 'record'<ids`,
	`satelle story re"cord" sto_1`,
	`satelle story "rec"ord sto_1`,
	`x=1 satelle story record sto_1`,
	`satelle story record|cat`,
	`satelle story record;echo`,
	`satelle story record>out`,
	`satelle story record 2>&1`,
	`satelle story "record"|cat`,
	`satelle story "record";x`,
	`satelle story 'record'&&x`,
	`echo $(satelle story "record")`,
	`(satelle story record)`,
	`{ satelle story record; }`,
	`satelle --session s story record sto_1`,
	"echo `satelle story 'record'`x",
	`echo "a"$(satelle story record)`,
	`satelle story $'record' sto_1`,
	`satelle story "record"<<<x`,
	`satelle story record&>/dev/null`,
	`nohup satelle story record`,
	`satelle story 're'"cord"`,
	`satelle story record"$(true)" sto_1`,
	`satelle story record"$x" sto_1`,
	`satelle story record"${x:-}" sto_1`,
	`satelle story "record""$(true)" sto_1`,
	"satelle story record\"`true`\" sto_1",
	`satelle story "$(true)"record sto_1`,
	`satelle"$(true)" story record sto_1`,
	`satelle story $"record" sto_1`,
	"echo x \\\r\nsatelle story record sto_1",
	`satelle story "record"$x sto_1`,
	`satelle story $x"record" sto_1`,
	`satelle story 'record'${x:-} sto_1`,
	`"satelle"$x story record sto_1`,
	`satelle story record$x sto_1`,
	`satelle story ${x:-}record sto_1`,
	`satelle story recor? sto_1`,
	`satelle story {record,} sto_1`,
	`{satelle,story,record} sto_1`,
	`satelle story $'re\x63ord' sto_1`,
	"satelle story rec\\\nord sto_1",
}

func TestTokenizeBashBackslash(t *testing.T) {
	continued := operatorRecordStoryTextCases[2]
	if segs := splitSegments(tokenizeBash(continued)); len(segs) != 1 {
		t.Errorf("%q: %d segments, want 1", continued, len(segs))
	}
	for _, c := range operatorRecordStoryTextCases[3:5] {
		for _, tok := range tokenizeBash(c) {
			if tok.Kind == "word" && (strings.Contains(tok.Value, "(") || tok.Value == operatorRecordVerb) {
				t.Errorf("%q: word token %q", c, tok.Value)
			}
		}
	}
}

func TestScanBashOperatorRecord(t *testing.T) {
	noFile := func(string) bool { return false }
	for _, c := range append(append(slices.Clone(operatorRecordCases), operatorRecordCodeCases...), operatorRecordEscapedCases...) {
		if !ScanBash(c, "/w", "/w", noFile).OperatorRecord {
			t.Errorf("%q: the operator record verb was not seen", c)
		}
	}
	for _, c := range append(append(slices.Clone(operatorRecordDataCases), notOperatorRecordCases...), operatorRecordStoryTextCases...) {
		if ScanBash(c, "/w", "/w", noFile).OperatorRecord {
			t.Errorf("%q: read as the operator record verb", c)
		}
	}
}

func TestOperatorRecordDataCasesNameTheVerb(t *testing.T) {
	for _, c := range operatorRecordDataCases {
		if !operatorRecordText.MatchString(c) {
			t.Errorf("%q does not name the verb", c)
		}
	}
}

func TestOperatorRecordCodeCasesNeedTheTextScan(t *testing.T) {
	for _, c := range operatorRecordCodeCases {
		for _, ps := range splitSegments(tokenizeBash(c)) {
			if operatorRecordWords(ps.Toks) {
				t.Errorf("%q: the word rule fires on %v", c, ps.Toks)
			}
		}
	}
}

func TestResolvePathForms(t *testing.T) {
	rows := []struct{ cwd, p, want string }{
		{"/w", "a.txt", "/w/a.txt"},
		{"/w", "/tmp/x.sh", "/tmp/x.sh"},
		{"/w", "../x", "/x"},
		{"/w/sub", "./a/../b", "/w/sub/b"},
		{"/w", "", ""},
	}
	if runtime.GOOS == "windows" {
		rows = append(rows,
			struct{ cwd, p, want string }{`C:\r`, "sub/f", "C:/r/sub/f"},
			struct{ cwd, p, want string }{`C:\r`, `C:\o\f`, "C:/o/f"},
			struct{ cwd, p, want string }{`C:\r`, "C:/o/f", "C:/o/f"},
			struct{ cwd, p, want string }{`C:\r`, "/tmp/x.sh", "/tmp/x.sh"},
		)
	}
	for _, r := range rows {
		if got := resolvePath(r.cwd, r.p); got != r.want {
			t.Errorf("resolvePath(%q, %q) = %q, want %q", r.cwd, r.p, got, r.want)
		}
	}
}

func operatorRecordOracleCorpus() []string {
	affix := []string{"", `"$(true)"`, `"$x"`, "''", `""`, "\\", "&", ";", "|", ">o", "<i", ")", "`", "$", "'x'", `"x"`, "x", "2>&1", "&&y", `"${x:-}"`, "$x", "${x:-}"}
	quoted := func(v string) []string {
		return []string{v, `"` + v + `"`, "'" + v + "'", v[:2] + `"` + v[2:] + `"`, "$'" + v + "'", `$"` + v + `"`, "\\" + v,
			v[:len(v)-1] + "?", v[:3] + "*", "[" + v[:1] + "]" + v[1:], "{" + v + ",}", v[:3] + "{" + v[3:] + ",x}", "{x,}" + v,
			v[:3] + "\\\n" + v[3:], "$'" + v[:2] + "\\x" + strconv.FormatInt(int64(v[2]), 16) + v[3:] + "'", "$'" + v[:2] + "\\" + strconv.FormatInt(int64(v[2]), 8) + v[3:] + "'"}
	}
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, v := range quoted("record") {
		for _, a := range affix {
			for _, b := range affix {
				add("satelle story " + a + v + b + " sto_1")
			}
		}
	}
	for _, n := range quoted("satelle") {
		for _, a := range affix {
			add("echo ok " + a + n + " story record sto_1")
			add(n + a + " story record")
		}
	}
	for _, a := range affix {
		add("echo ok" + a + "satelle story record sto_1")
		add(`echo "a"` + a + "$(satelle story record)")
	}
	for _, nl := range []string{"\n", "\r\n", "\\\n", "\\\r\n", " \\\r\n", "; \\\n"} {
		add("echo x" + nl + "satelle story record sto_1")
		add("satelle story" + nl + "record sto_1")
	}
	add("{satelle,story,record} sto_1")
	add("satelle {story,record} sto_1")
	add("satelle story {record,x}")
	return out
}

func TestOperatorRecordRefusesWhatBashWouldRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash oracle runs on unix")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	dir := t.TempDir()
	for _, f := range []string{"i", "ids", "record", "satelle"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	corpus := operatorRecordOracleCorpus()
	script := `while IFS= read -r -d '' c; do
  r=$( (PATH="$EMPTY"; satelle() { [ "$2" = record ] && printf HIT; }; x=; eval "$c" ) 2>/dev/null </dev/null )
  case $r in *HIT*) printf '%s\0' "$c";; esac
done`
	cmd := exec.Command(bash, "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "EMPTY="+empty)
	cmd.Stdin = strings.NewReader(strings.Join(corpus, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bash oracle: %v", err)
	}
	hits := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if len(hits) < 100 {
		t.Fatalf("bash ran the operator verb for only %d of %d commands; the oracle is not working", len(hits), len(corpus))
	}
	t.Logf("bash runs the operator verb for %d of %d commands", len(hits), len(corpus))
	noFile := func(string) bool { return false }
	for _, c := range hits {
		if !ScanBash(c, "/w", "/w", noFile).OperatorRecord {
			t.Errorf("%q: bash runs satelle story record, the scan allows it", c)
		}
	}
}

func TestScanBashGitAfterCd(t *testing.T) {
	noFile := func(string) bool { return false }
	for _, c := range []struct {
		cmd   string
		paths []string
	}{
		{"cd /o && git merge x", []string{"/o"}},
		{"cd /o; cd sub && git merge x", []string{"/o", "/w/sub", "/o/sub"}},
		{"cd /o && git -C a merge x", []string{"/o/a", "/w/a"}},
		{"git -C/o merge x", []string{"/o"}},
		{"cd /o && git status", nil},
	} {
		got := ScanBash(c.cmd, "/w", "", noFile)
		if !slices.Equal(got.Paths, c.paths) || got.Unreadable != "" {
			t.Errorf("%q: paths %v unreadable %q, want %v and a plain line", c.cmd, got.Paths, got.Unreadable, c.paths)
		}
	}
}

func TestGitAfterCdMatchesBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash oracle runs on unix")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, o, bin := filepath.Join(dir, "B"), filepath.Join(dir, "O"), filepath.Join(dir, "bin")
	for _, d := range []string{filepath.Join(b, "sub"), filepath.Join(o, "sub"), bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake := `#!/bin/sh
while [ $# -gt 0 ]; do
  case $1 in
    -C) cd "$2" || exit 1; shift 2;;
    -C*) cd "${1#-C}" || exit 1; shift;;
    -*) shift;;
    *) break;;
  esac
done
printf '%s\t%s\n' "$(pwd -P)" "$1" >> "$GITLOG"
`
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := []string{
		"cd O && git merge x", "cd O; cd sub && git merge x", "cd nope; git merge x", "cd O || cd sub && git merge x",
		"cd O | cd sub; git merge x", "cd O && git -C sub merge x", "cd O && git -Csub merge x",
		"cd sub && cd ../.. && git merge x", "'cd' O && 'git' merge x", "cd O && ls && git merge x",
	}
	unread := []string{
		"pushd O && git merge x", "builtin cd O && git merge x", "X=1 cd O && git merge x", "X+=1 cd O && git merge x",
		"cd O && echo hi && git merge x", "printf -v d %s O; cd O && git merge x", "cd O && git\\\n merge x",
		"cd O && git >/dev/null merge x", `cd O && git -C "$(pwd)" merge x`,
	}
	noFile := func(string) bool { return false }
	check := func(line string, wantPlain bool) {
		line = strings.ReplaceAll(line, " O", " "+o)
		log := filepath.Join(dir, "log")
		_ = os.Remove(log)
		cmd := exec.Command(bash, "-c", line)
		cmd.Dir = b
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GITLOG="+log)
		_ = cmd.Run()
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatalf("%q: bash ran no git: %v", line, err)
		}
		got := ScanBash(line, b, b, noFile)
		if wantPlain != (got.Unreadable == "") {
			t.Errorf("%q: unreadable %q, want plain %v", line, got.Unreadable, wantPlain)
		}
		for _, row := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			ran, sub, _ := strings.Cut(row, "\t")
			if ran == b || gitReadOnlySubcommands[sub] {
				continue
			}
			if got.Unreadable == "" && !slices.Contains(got.Paths, filepath.ToSlash(ran)) {
				t.Errorf("%q: bash runs git %s in %s, the scan reads %v", line, sub, ran, got.Paths)
			}
		}
	}
	for _, l := range plain {
		check(l, true)
	}
	for _, l := range unread {
		check(l, false)
	}
}

func TestReadPlainLine(t *testing.T) {
	for _, c := range []struct {
		line  string
		words []string
	}{
		{`git commit -m "it's (x)"`, []string{"git", "commit", "-m", "it's (x)"}},
		{"cd 'a b'", []string{"cd", "a b"}},
		{"git diff HEAD~1;", []string{"git", "diff", "HEAD~1"}},
		{`ls "C:\bin\x"`, []string{"ls", `C:\bin\x`}},
		{"/bin/echo hi", []string{"/bin/echo", "hi"}},
	} {
		segs, why := readPlainLine(c.line)
		if why != "" || len(segs) != 1 {
			t.Errorf("%q: %d segments, unread %q, want one plain segment", c.line, len(segs), why)
			continue
		}
		var got []string
		for _, w := range segs[0].Words {
			got = append(got, w.Value)
		}
		if !slices.Equal(got, c.words) {
			t.Errorf("%q: words %q, want %q", c.line, got, c.words)
		}
	}
	for _, c := range []struct{ line, want string }{
		{"git log # x", "#"},
		{"git log $x", "$"},
		{"git log `x`", "`"},
		{`git log a\ b`, `\`},
		{"git log \\\n -1", "a line continuation"},
		{"git log\ngit diff", "a newline"},
		{"git log\r", "a carriage return"},
		{"git log & ls", "a lone &"},
		{"git log |& cat", "|&"},
		{"(git log)", "("},
		{"{ git log; }", "{"},
		{"ls *.go", "*"},
		{"ls [ab]", "["},
		{"cat < f", "a redirection <"},
		{"git log > f", "a redirection >"},
		{"git log 2>&1", "a redirection >"},
		{"ls ~/x", "~"},
		{"git commit -m x!", "!"},
		{"ls caf\xc3\xa9", "0xc3"},
		{`git log "$x"`, "$ inside double quotes"},
		{`git log "a\$"`, "a backslash escape inside double quotes"},
		{"git log 'x", "an unterminated single quote"},
		{`git log "x`, "an unterminated double quote"},
		{"git log;; ls", "an empty command"},
		{"git log &&", "a trailing &&"},
		{"X=1 cd a", "an assignment"},
		{"X+=1 cd a", "an assignment"},
		{"echo hi", "the builtin or keyword echo"},
		{"printf -v x y", "the builtin or keyword printf"},
		{"true", "the builtin or keyword true"},
		{"pwd", "the builtin or keyword pwd"},
		{"pushd a", "the builtin or keyword pushd"},
		{"then git x", "the builtin or keyword then"},
		{"cd", "a cd whose target"},
		{"cd -", "a cd whose target"},
		{"cd -P x", "a cd whose target"},
		{"cd a b", "a cd whose target"},
	} {
		if _, why := readPlainLine(c.line); !strings.Contains(why, c.want) || why == "" {
			t.Errorf("%q: unread %q, want it naming %q", c.line, why, c.want)
		}
	}
}

func TestNamesGit(t *testing.T) {
	for _, c := range []string{
		`git commit -m "$(cat f)"`, "x=$(git rev-parse HEAD); git reset --hard", "'git' merge x 2>&1",
		"/usr/bin/git -C /o merge x >l", "git status 2>&1", `g\it merge x`, `"g"it merge x`, "git\\\n merge x",
		"git.exe -C x merge y", "ls ~/git && make",
	} {
		if !namesGit(c) {
			t.Errorf("%q: namesGit false, want true", c)
		}
	}
	for _, c := range []string{"cat .gitignore > x", "echo $HOME", "digit=1; echo $digit", "git-lfs pull >l", "GIT_DIR=x make"} {
		if namesGit(c) {
			t.Errorf("%q: namesGit true, want false", c)
		}
	}
}

func TestAssignmentPrefix(t *testing.T) {
	for _, w := range []string{"X=1", "X+=1", "X="} {
		if !isAssignmentPrefix(w) {
			t.Errorf("isAssignmentPrefix(%q) false, want true", w)
		}
	}
	for _, w := range []string{"1X=y", "-x=y", "=x", "X+1"} {
		if isAssignmentPrefix(w) {
			t.Errorf("isAssignmentPrefix(%q) true, want false", w)
		}
	}
	if isAssignmentWord("X+=1") {
		t.Error(`isAssignmentWord("X+=1") is true; the legacy scans must keep reading it as a command word`)
	}
}

func TestBashBuiltinWordsCoverBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash oracle runs on unix")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	out, err := exec.Command(bash, "-c", "compgen -b; compgen -k").Output()
	if err != nil {
		t.Fatalf("compgen: %v", err)
	}
	words := strings.Fields(string(out))
	if len(words) < 50 {
		t.Fatalf("compgen printed %d words; the oracle is not working", len(words))
	}
	for _, w := range words {
		if !bashBuiltinWords[w] {
			t.Errorf("bash builtin or keyword %q is not in bashBuiltinWords", w)
		}
	}
}
