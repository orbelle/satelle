package agentstep

import (
	"context"

	"github.com/bobmcallan/satelle/internal/store"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Remote placement (sty_dde8b6a4): a parallel epic child's step is performed by
// the step's remote_agent cloud binding; every other child is performed by the
// step's own agent. The container, its schedule and the signed-in signal are
// real (placementtest); the cloud provider and the local agent are fakes.

const (
	placedLocalAgent  = "worker"
	placedRemoteAgent = "cloudy"
)

// placedStep is the step's raw lines: its remote placement declaration.
const placedStep = `remote_agent = "cloudy"` + "\n" + `local_tags = ["lane:trunk"]`

func placedWF(extra string) string {
	return spineWF("", "", "",
		"plan|"+placedLocalAgent+"|cloud-step||||"+extra,
		"in_progress|executor",
		"done")
}

// placedRig is an engine over a cloud fixture whose local agent is a fake runner.
type placedRig struct {
	cloudEngine
	fix   cloudFix
	cloud *fakeCloud
	local *fakeRunner
	db    *store.DB
	epic  workitem.Item
	// limit is the remote binding's timeout. It is long (the session pushes before
	// the wait starts, which then returns at its first poll); a test whose subject
	// is the timeout sets cloudTestShortLimit.
	limit string
}

// ranLocal reports whether the step's own agent was dispatched.
func (r *placedRig) ranLocal() bool { return r.local.got.SystemPrompt != "" }

func (r *placedRig) dispatchItem(item workitem.Item) (string, error) {
	res, err := r.DispatchExecutor(context.Background(), item, "plan")
	return res.Agent, err
}

func (r *placedRig) telemetry(kind string) []telemetryRec {
	var out []telemetryRec
	for _, rec := range *r.tele {
		if rec.kind == kind {
			out = append(out, rec)
		}
	}
	return out
}
