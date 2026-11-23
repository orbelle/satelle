package remotenode

import (
	"maps"
	"strings"
	"testing"
)

func TestParseSecretsFile(t *testing.T) {
	body := "\n   \n# comment\n  # indented comment\nA=plain\nB=\"double quoted\"\nC='single quoted'\nD=has=equals\nE=\nCRLF=x\r\n"
	got, err := parseSecretsFile([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"A": "plain", "B": "double quoted", "C": "single quoted", "D": "has=equals", "E": "", "CRLF": "x"}
	if !maps.Equal(got, want) {
		t.Fatalf("parseSecretsFile = %q, want %q", got, want)
	}
	for _, line := range []string{"not a pair", "export K=v", "K = v", "1K=v", `K="unbalanced`, `K='mixed"`} {
		t.Run(line, func(t *testing.T) {
			_, err := parseSecretsFile([]byte("# first\nOK=1\n" + line + "\n"))
			if err == nil || err.Error() != ".satelle/.env line 3 is not a blank line, a # comment or KEY=VALUE" || strings.Contains(err.Error(), line) {
				t.Fatalf("parseSecretsFile(%q) = %v", line, err)
			}
		})
	}
}
