package cli

import (
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

func TestPrintRunShowsProgressAndLost(t *testing.T) {
	for _, tc := range []struct {
		run  *satellepb.ItemRun
		want string
	}{
		{&satellepb.ItemRun{State: remotenode.RunRunning, NodeId: "n", Notes: "progress: x"},
			"r on i: running (node n)\nprogress: x\n"},
		{&satellepb.ItemRun{State: remotenode.RunLost, NodeId: "n", Reason: "the node did not answer within 15s; it last answered at 2026-11-24T01:00:00Z"},
			"r on i: lost (node n)\nthe node did not answer within 15s; it last answered at 2026-11-24T01:00:00Z\n"},
	} {
		var out strings.Builder
		printRun(&out, "r", "i", tc.run, 0)
		if out.String() != tc.want {
			t.Errorf("printRun = %q, want %q", out.String(), tc.want)
		}
	}
}
