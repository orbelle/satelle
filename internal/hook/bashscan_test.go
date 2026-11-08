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
}

// notOperatorRecordCases name satelle or record without running the verb.
var notOperatorRecordCases = []string{
	"satelle story attach sto_1 --name record --file f",
	"satelle story get sto_1",
	"git log --grep record",
	"echo record; satelle status",
}

func TestScanBashOperatorRecord(t *testing.T) {
	noFile := func(string) bool { return false }
	for _, c := range operatorRecordCases {
		if !ScanBash(c, "/w", "/w", noFile).OperatorRecord {
			t.Errorf("%q: the operator record verb was not seen", c)
		}
	}
	for _, c := range notOperatorRecordCases {
		if ScanBash(c, "/w", "/w", noFile).OperatorRecord {
			t.Errorf("%q: read as the operator record verb", c)
		}
	}
}
