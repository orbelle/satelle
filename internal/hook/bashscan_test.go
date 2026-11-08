package hook

import (
	"slices"
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
		{cmd: `bash -c 'rm -f q'`, paths: []string{"/w/q"}},
		{cmd: `python3 -c 'print(1)'`, inline: []string{"/w"}},
		{cmd: "python3 script.py"},
		{cmd: "cat <<'EOF' | python3 -\nprint(1)\nEOF", inline: []string{"/w"}},
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

// AC7: a shell call naming satelled's operator-record route is the operator's
// record too, behind wrappers and inside program text; the GET page is not.
func TestScanBashOperatorRoute(t *testing.T) {
	noFile := func(string) bool { return false }
	const post = "curl -X POST http://127.0.0.1:8787/r/x/operator-record/sto_1 -d to=approved"
	for _, c := range []string{
		post,
		"env FOO=1 " + post,
		"timeout 5 " + post,
		"sh -c '" + post + "'",
		`python3 -c 'import urllib.request as u; u.urlopen("http://127.0.0.1:8787/r/x/operator-record/sto_1", b"to=approved")'`,
		"wget --post-data to=approved http://localhost:8787/r/x/operator-record/sto_1",
		"python3 <<'EOF'\nimport urllib.request\nurllib.request.urlopen('http://127.0.0.1:8787/r/x/operator-record/sto_1', b'to=approved')\nEOF",
	} {
		if !ScanBash(c, "/w", "/w", noFile).OperatorRecord {
			t.Errorf("%q: the operator-record route was not seen", c)
		}
	}
	for _, c := range []string{
		"curl http://127.0.0.1:8787/r/x/operator",
		"curl http://127.0.0.1:8787/r/x/fragment/operator",
	} {
		if ScanBash(c, "/w", "/w", noFile).OperatorRecord {
			t.Errorf("%q: read as the operator-record route", c)
		}
	}
	if v := (Gate{Root: "/w"}).Bash(post, "/w"); v.Allow || !strings.Contains(v.Reason, "operator-record route") {
		t.Errorf("gate: %+v", v)
	}
}
