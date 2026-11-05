package cli

import (
	"os"
	"testing"

	"github.com/bobmcallan/satelle/internal/gittest"
)

// TestMain isolates every git this package's tests start, including git run
// by the code under test, from the host's configuration.
func TestMain(m *testing.M) { os.Exit(gittest.Main(m)) }
