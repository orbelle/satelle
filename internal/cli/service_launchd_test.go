package cli

import (
	"strings"
	"testing"
)

func TestLaunchdPlists(t *testing.T) {
	d := renderDaemonPlist("/x/satelled", "127.0.0.1", 8787)
	for _, want := range []string{"<key>RunAtLoad</key>\n\t<true/>", "<key>KeepAlive</key>\n\t<true/>", "<string>dev.satelle.satelled</string>"} {
		if !strings.Contains(d, want) {
			t.Errorf("satelled plist lacks %q:\n%s", want, d)
		}
	}
	if !inOrder(d, "<key>ProgramArguments</key>", "<string>/x/satelled</string>", "<string>--addr</string>", "<string>127.0.0.1</string>", "<string>--port</string>", "<string>8787</string>", "</array>") {
		t.Errorf("satelled ProgramArguments out of order:\n%s", d)
	}
	s := renderSyncPlist("/x/satelle")
	if !inOrder(s, "<key>ProgramArguments</key>", "<string>/x/satelle</string>", "<string>ledger</string>", "<string>autosync</string>", "</array>") {
		t.Errorf("sync ProgramArguments out of order:\n%s", s)
	}
	if !strings.Contains(s, "<key>StartInterval</key>\n\t<integer>30</integer>") || !strings.Contains(s, "<string>dev.satelle.sync</string>") {
		t.Errorf("sync plist:\n%s", s)
	}
	for _, p := range []string{d, s} {
		if !strings.HasPrefix(p, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\"") {
			t.Errorf("no plist header:\n%s", p)
		}
	}
	if got := plistProgram(d); got != "/x/satelled" {
		t.Errorf("plistProgram(satelled) = %q", got)
	}
	if got := plistProgram(s); got != "/x/satelle" {
		t.Errorf("plistProgram(sync) = %q", got)
	}
	odd := "/Users/a b/R&D/satelled"
	if got := plistProgram(renderDaemonPlist(odd, "0.0.0.0", 1)); got != odd {
		t.Errorf("round trip of %q = %q", odd, got)
	}
	for _, bad := range []string{"", "<plist><dict><key>ProgramArguments</key>", "not xml", "<plist><dict><key>Label</key><string>x</string></dict></plist>"} {
		if got := plistProgram(bad); got != "" {
			t.Errorf("plistProgram(%q) = %q, want empty", bad, got)
		}
	}
}
