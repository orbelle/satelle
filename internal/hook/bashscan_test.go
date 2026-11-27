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
	for _, c := range compoundRecordLines {
		add(c)
	}
	return out
}

// compoundRecordLines are record commands behind time and [[: shellCompoundWords
// must not list either, or the record rule would skip their substitution.
var compoundRecordLines = []string{
	`time true && echo "$(satelle story rec'ord')"`,
	`[[ -n x ]] && echo "$(sa'telle' story record)"`,
}

func TestOperatorRecordCompoundWordsUnchanged(t *testing.T) {
	noFile := func(string) bool { return false }
	for _, c := range compoundRecordLines {
		if !ScanBash(c, "/w", "/w", noFile).OperatorRecord {
			t.Errorf("%q: the record rule allows it", c)
		}
	}
}

func TestAssignmentWords(t *testing.T) {
	for _, w := range []string{"A=1", "A+=1", "_x+=", "A=", "A+="} {
		if !isAssignmentWord(w) {
			t.Errorf("%q is not read as an assignment", w)
		}
	}
	for _, w := range []string{"A+", "+=1", "A++=1", "1A=x", "-A=1"} {
		if isAssignmentWord(w) {
			t.Errorf("%q is read as an assignment", w)
		}
	}
	if got := ScanBash("A+=1 rm x", "/w", "/w", func(string) bool { return false }).Paths; !slices.Contains(got, "/w/x") {
		t.Errorf("A+=1 rm x: paths %v, want /w/x", got)
	}
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
