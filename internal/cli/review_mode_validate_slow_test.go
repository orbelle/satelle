//go:build clislow

package cli

import (
	"testing"
)

func TestSlowValidateAcceptsBothReviewModes(t *testing.T) {
	for name, tc := range map[string]struct{ step, gate string }{
		"no mode":           {"", ""},
		"gate full":         {"", `review_mode = "full"`},
		"gate fail-fast":    {"", `review_mode = "fail-fast"`},
		"step fail_fast":    {`fail_fast = ["gate-b"]`, ""},
		"both fail-fast":    {`fail_fast = ["gate-a", "gate-b"]`, `review_mode = "fail-fast"`},
		"panel gate ff":     {"", "review_mode = \"fail-fast\"\npanel = [\"reviewer\"]"},
		"explicit full too": {`fail_fast = []`, `review_mode = "full"`},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := validateWithRoute(t, tc.step, tc.gate)
			if err != nil {
				t.Fatalf("validate refused a valid review mode: %v\n%s", err, out)
			}
		})
	}
}
