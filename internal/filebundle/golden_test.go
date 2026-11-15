package filebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

const goldenBundleSHA256 = "94a271ab1045f676b500035dfc507a07ea797bc90139dac7ac0d36f02b2598dd"

var goldenEntries = []Entry{
	{Path: "remote.toml", Content: "name = \"golden\"\n# café — ✓\n"},
	{Path: "workflows/done.toml", Content: "done = true\n"},
	{Path: "workflows/step.toml", Content: "[a]\nnote = \"x < y > z & w\"\n"},
	{Path: "skills/alpha.md", Content: "# Alpha\n\nline one\r\nline two\n"},
	{Path: "skills/nested/deep.md", Content: "deep <> & ✓\n"},
	{Path: "skills/zeta.md", Content: "# Zeta\n\n### Heading\n\ncafé — ✓ <b>&amp;</b>\n"},
	{Path: "principles/one.md", Content: "# One\n\n日本語 — text\n"},
}

func TestEncodeBytesArePinned(t *testing.T) {
	got, err := Encode(goldenEntries)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(got)
	if hex.EncodeToString(sum[:]) != goldenBundleSHA256 {
		t.Errorf("Encode sha256 = %x, want %s\nbytes: %s", sum, goldenBundleSHA256, got)
	}
	if !strings.HasPrefix(string(got), `{"format":1,"files":[{"path":"remote.toml"`) {
		t.Errorf("the frame changed: %.60s", got)
	}
}
