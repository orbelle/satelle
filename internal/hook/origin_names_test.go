package hook

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/integration"
	"github.com/bobmcallan/satelle/internal/ledger"
)

func TestOriginNamesNoVendor(t *testing.T) {
	var names []string
	for _, h := range integration.Hooks() {
		names = append(names, h.Name())
	}
	names = append(names, integration.Dispatches()...)
	names = append(names, "anthropic", "openai", "google", "xai", "gemini", "gpt", "opus", "sonnet", "haiku")
	files := []string{"../ledger/origin.go", "../kernel/origin.go", "../config/config.go"}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if n == "" {
				continue
			}
			if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(n) + `\b`).Match(b) {
				t.Errorf("%s names %q", f, n)
			}
		}
	}
	if want := []string{"session", "performer", "judge", "sync", "node"}; !slices.Equal(ledger.Roles(), want) {
		t.Fatalf("Roles() = %v, want %v", ledger.Roles(), want)
	}
	for _, r := range ledger.Roles() {
		for _, n := range names {
			if strings.EqualFold(r, n) {
				t.Errorf("role %q is an integration name", r)
			}
		}
	}
}
