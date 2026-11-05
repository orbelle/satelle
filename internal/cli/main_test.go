package cli

import (
	"io"
	"testing"
)

// testForgeAPIURLEnv names the fake forge a re-exec'd child's `satelle pr`
// talks to (forgeAPIURL).
const testForgeAPIURLEnv = "SATELLE_TEST_FORGE_API_URL"

// realPushValidator is the validation a push really runs, kept aside while the
// rest of the suite runs with it stood down.
var realPushValidator func(io.Writer, string) int

// useRealPushValidator restores the real push validation for one test.
func useRealPushValidator(t *testing.T) {
	t.Helper()
	prev := pushValidator
	pushValidator = realPushValidator
	t.Cleanup(func() { pushValidator = prev })
}
