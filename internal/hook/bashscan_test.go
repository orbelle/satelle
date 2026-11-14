package hook

import (
	"slices"
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
		{cmd: `bash -c 'rm -f q'`, paths: []string{"/w/q"}},
		{cmd: `python3 -c 'print(1)'`, inline: []string{"/w"}},
		{cmd: "python3 script.py"},
		{cmd: "cat <<'EOF' | python3 -\nprint(1)\nEOF", inline: []string{"/w"}},
		// chmod: the leading mode operand is a mode, not a path, in every form.
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
		// chmod: every file operand stays a target, mode-shaped or not, once
		// the mode slot has closed; a non-mode leading positional fails closed.
		{cmd: "chmod notamode f", paths: []string{"/w/notamode", "/w/f"}},
		{cmd: "chmod /tmp/scratch +x", paths: []string{"/tmp/scratch", "/w/+x"}},
		{cmd: "chmod notamode 755", paths: []string{"/w/notamode", "/w/755"}},
		{cmd: "chmod a=r f +x", paths: []string{"/w/f", "/w/+x"}},
		{cmd: "chmod -w +x", paths: []string{"/w/+x"}},
		{cmd: "chmod --reference=ref +x", paths: []string{"/w/+x"}},
	} {
		got := ScanBash(c.cmd, "/w", "", noFile)
		if !slices.Equal(got.Paths, c.paths) || !slices.Equal(got.Inline, c.inline) {
			t.Errorf("%q: paths %v inline %v, want %v %v", c.cmd, got.Paths, got.Inline, c.paths, c.inline)
		}
	}
}

// operatorRecordCases are agent shell calls that run satelle's operator
// record verb, by every wrapper and text form the scan covers.
var operatorRecordCases = []string{
	"satelle story record sto_1 --file f",
	// sto_2a41b9e4: the root flags, separate and glued.
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
	// Quoted, subshell, substitution and background word forms.
	`satelle story "record" sto_1`,
	`"satelle" story record sto_1`,
	"(satelle story record sto_1)",
	"echo $(satelle story record sto_1)",
	"echo ok & satelle story record sto_1",
}

// operatorRecordCodeCases name the verb only in text that a shell or
// interpreter runs; the word rule alone does not see them.
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
	// A group or loop body piped into a shell.
	"{ echo a; echo 'satelle story record sto_1'; } | bash",
	"while true; do echo 'satelle story record sto_1'; done | sh",
}

// operatorRecordDataCases name the verb only as data to a command that does
// not run it: a commit message, echo, printf, a grep pattern, a file argument,
// satelle's own prose flags and a heredoc to cat.
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

// notOperatorRecordCases name satelle or record without running the verb.
var notOperatorRecordCases = []string{
	"satelle story attach sto_1 --name record --file f",
	"satelle story attach sto_1 --session record --file f",
	"satelle story get sto_1",
	"git log --grep record",
	"echo record; satelle status",
}

func TestScanBashOperatorRecord(t *testing.T) {
	noFile := func(string) bool { return false }
	for _, c := range append(slices.Clone(operatorRecordCases), operatorRecordCodeCases...) {
		if !ScanBash(c, "/w", "/w", noFile).OperatorRecord {
			t.Errorf("%q: the operator record verb was not seen", c)
		}
	}
	for _, c := range append(slices.Clone(operatorRecordDataCases), notOperatorRecordCases...) {
		if ScanBash(c, "/w", "/w", noFile).OperatorRecord {
			t.Errorf("%q: read as the operator record verb", c)
		}
	}
}

// Every data case names the verb on one line, so a scan of the whole command
// text (or of every quoted token) would refuse it: the data cases stand only
// while data is exempt.
func TestOperatorRecordDataCasesNameTheVerb(t *testing.T) {
	for _, c := range operatorRecordDataCases {
		if !operatorRecordText.MatchString(c) {
			t.Errorf("%q does not name the verb", c)
		}
	}
}

// No code case is caught by the word rule on any segment, so each refusal rests
// on the scan of text a shell or interpreter runs.
func TestOperatorRecordCodeCasesNeedTheTextScan(t *testing.T) {
	for _, c := range operatorRecordCodeCases {
		for _, ps := range splitSegments(tokenizeBash(c)) {
			if operatorRecordWords(ps.Toks) {
				t.Errorf("%q: the word rule fires on %v", c, ps.Toks)
			}
		}
	}
}
