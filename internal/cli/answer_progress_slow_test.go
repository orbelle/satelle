//go:build clislow

package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSlowValidateRefusesAMalformedAnswerProgressTable(t *testing.T) {
	cases := []struct {
		name, propose, table string
		want                 string // empty: validate passes
	}{
		{"enabled on a propose step", "propose = true\n", "[ready.answer_progress]\nenabled = true\nmin_progress = 2\n", ""},
		{"absent", "propose = true\n", "", ""},
		{"min_progress 0", "propose = true\n", "[ready.answer_progress]\nenabled = true\nmin_progress = 0\n", "min_progress must be >= 1"},
		{"missing enabled", "propose = true\n", "[ready.answer_progress]\nmin_progress = 1\n", "needs enabled"},
		{"non-propose step", "", "[ready.answer_progress]\nenabled = true\n", "answer_progress requires propose = true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := tempRepo(t)
			t.Chdir(repo)
			writeRoute(t, filepath.Join(repo, ".satelle", "workflows"), scheduleRouteDone, answerProgressRouteStep(tc.propose, tc.table))
			out, err := runRootIn(t, "", "validate")
			if tc.want == "" {
				if strings.Contains(out, "answer_progress") {
					t.Fatalf("a well-formed table must not be named in a failure:\n%s", out)
				}
				return
			}
			if err == nil || !strings.Contains(out, "FAIL") || !strings.Contains(out, tc.want) {
				t.Fatalf("validate must FAIL naming %q (err=%v):\n%s", tc.want, err, out)
			}
		})
	}
}
