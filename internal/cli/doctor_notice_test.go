package cli

import (
	"testing"
	"time"
)

func TestDoctorNotice(t *testing.T) {
	rep := doctorReport{Issues: []doctorIssue{
		{Check: "a", Problem: "p1", Fix: "f1"},
		{Check: "b", Problem: "p2"},
		{Check: "a", Problem: "p3", Fix: "f3"},
		{Check: "c", Problem: "p4"},
		{Check: "c", Problem: "p5", Fix: "f5"},
	}}
	want := "satelle doctor: 5 issue(s); run `satelle doctor` for each\n" +
		"  a (2) — fix: f1\n" +
		"  b (1) — fix: " + noticeFallbackFix + "\n" +
		"  c (2) — fix: f5\n"
	if got := doctorNotice(rep); got != want {
		t.Errorf("notice:\n%q\nwant\n%q", got, want)
	}
	if got := doctorNotice(doctorReport{}); got != "" {
		t.Errorf("no issues gave a notice: %q", got)
	}
	if got := doctorNotice(doctorReport{Unchecked: []string{"z"}}); got != "" {
		t.Errorf("unchecked checks alone gave a notice: %q", got)
	}
}

func TestDoctorBlockGolden(t *testing.T) {
	rep := doctorReport{
		Issues: []doctorIssue{
			{Check: "a", Problem: "p1", Fix: "f1"},
			{Check: "b", Problem: "p2", Fix: "f2"},
			{Check: "a", Problem: "p3", Fix: "f3"},
		},
		Unchecked: []string{"z", "y"},
	}
	want := "satelle doctor: 3 issue(s) (read-only; `satelle doctor` lists each with its fix)\n" +
		"  a (2): p1 — fix: f1\n" +
		"  b (1): p2 — fix: f2\n" +
		"  not checked within 2s at session start: y, z; `satelle doctor` runs them\n"
	if got := doctorBlock(rep, 2*time.Second); got != want {
		t.Errorf("block:\n%q\nwant\n%q", got, want)
	}
}
