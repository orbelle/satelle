//go:build !windows

package kernel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

const redactCheck = "---\ntype: check\n---\n```sh\nprintf '%s' \"$PRE$SATELLE_TEST_SECRET$POST\"\nexit 1\n```\n"

func redactKernel(t *testing.T, value string) *Kernel {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, ".satelle")
	write(t, filepath.Join(root, "satelle.toml"), "[workflows]\ndefault = \"chk\"\n")
	write(t, filepath.Join(root, "skills", "chk.md"), redactCheck)
	write(t, filepath.Join(root, "workflows", "chk.toml"), `
[states.a]
start = true
[states.b]
terminal = true
[gates.chk]
skill = "chk"
[[transition]]
from = ["a"]
to = "b"
groups = [{ gates = ["chk"] }]
`)
	k, err := Open(Options{Root: root, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { k.Close() })
	k.Redact = func(s string) string { return strings.ReplaceAll(s, value, "[secret]") }
	return k
}

func hexValue(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func recordedGateNotes(t *testing.T, k *Kernel, id string) string {
	t.Helper()
	events, err := k.Ledger.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, e := range events {
		if e.Kind != "gate.run" {
			continue
		}
		var p struct {
			Notes string `json:"notes"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		notes = append(notes, p.Notes)
	}
	if len(notes) == 0 {
		t.Fatalf("%s has no gate.run", id)
	}
	return strings.Join(notes, "\n")
}

func windowIn(s, v string) int {
	for i := 0; i+16 <= len(v); i++ {
		if strings.Contains(s, v[i:i+16]) {
			return i
		}
	}
	return -1
}

func TestDriveStepsStopReasonCarriesNoSecret(t *testing.T) {
	var lines []string
	for range 6 {
		lines = append(lines, hexValue(t, 32))
	}
	v := strings.Join(lines, "\n")
	k := redactKernel(t, v)
	t.Setenv("SATELLE_TEST_SECRET", v)
	t.Setenv("PRE", "")
	t.Setenv("POST", "\nthe check failed")
	it := newItem(t, k)
	res, err := k.DriveSteps(context.Background(), it.ID, "node", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdict != VerdictReject || !strings.Contains(res.Notes, "chk: [secret]") {
		t.Fatalf("DriveSteps = %+v", res)
	}
	for where, s := range map[string]string{"the gate.run notes": recordedGateNotes(t, k, it.ID), "the stop reason": res.Notes} {
		if !strings.Contains(s, "[secret]") {
			t.Errorf("%s holds no [secret]: %s", where, s)
		}
		for _, l := range lines {
			if strings.Contains(s, l) {
				t.Errorf("%s holds a line of the value", where)
			}
		}
	}
}

func TestNoSliceSeesASecretPastBound(t *testing.T) {
	const total, size, cut = 6000, 300, 2048
	v := hexValue(t, size/2)
	k := redactKernel(t, v)
	t.Setenv("SATELLE_TEST_SECRET", v)
	var offsets []int
	for o := cut - 18; o <= cut+18; o++ {
		offsets = append(offsets, o)
	}
	for d := -16; d <= 16; d++ {
		offsets = append(offsets, total-cut-size/2+d)
	}
	for _, o := range offsets {
		t.Setenv("PRE", strings.Repeat("a", o))
		t.Setenv("POST", strings.Repeat("b", total-o-size))
		it := newItem(t, k)
		res, err := k.DriveSteps(context.Background(), it.ID, "node", 0)
		if err != nil {
			t.Fatal(err)
		}
		notes := recordedGateNotes(t, k, it.ID)
		if !strings.Contains(notes, "bytes elided") {
			t.Fatalf("offset %d: the notes were not bounded", o)
		}
		if i := windowIn(notes, v); i >= 0 {
			t.Fatalf("offset %d: the recorded notes hold a 16-byte window of the value at %d", o, i)
		}
		if i := windowIn(res.Notes, v); i >= 0 {
			t.Fatalf("offset %d: the stop reason holds a 16-byte window of the value at %d", o, i)
		}
	}
}
