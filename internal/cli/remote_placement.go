package cli

import (
	"context"
	"time"

	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/verb"
)

// remotePerformerReady bounds how long a started performer node has to report
// ready once its channel is paired.
const remotePerformerReady = 5 * time.Minute

// hostedPlacement is the real RemoteChannel and TeamRunner (sty_7028d996): a
// binding's declared placement reaches satelle.dev through the signed-in hosted
// client. satelle.dev starts every node; this side only asks, then pairs.
type hostedPlacement struct{ client *hosted.Client }

func (p hostedPlacement) FireRemote(ctx context.Context, remoteID, storyID, trigger string) (hosted.FireResult, error) {
	return p.client.FireRemote(ctx, remoteID, storyID, trigger)
}

func (p hostedPlacement) TeamRunStatus(ctx context.Context, remoteID, storyID string) (hosted.RunStanding, error) {
	return p.client.TeamRunStatus(ctx, remoteID, storyID)
}

// Open asks satelle.dev to start a performer node of the binding's remote, pairs
// with it on the session the server minted (admitting only the key the node
// published) and waits for its ready report. Until satelle-server ships the
// nodes route the first call is refused with an error naming that dependency.
func (p hostedPlacement) Open(ctx context.Context, bindingName string, binding config.AgentBinding, storyID string) (agentstep.RemoteRun, error) {
	nodeID, relayURL, err := p.client.StartPerformerNode(ctx, binding.Remote, storyID, bindingName, binding.RemotePath)
	if err != nil {
		return nil, err
	}
	sess, err := remotechan.AttachServerSession(ctx, p.client, relayURL, nodeID, func(ctx context.Context) ([]byte, error) {
		return p.client.GetRemoteNode(ctx, nodeID)
	})
	if err != nil {
		return nil, err
	}
	return remotenode.Attach(ctx, sess, remotePerformerReady)
}

// wirePlacement gives the dispatch engine the means to honour a binding's
// placement. A session that is not signed in wires nothing, so a remote or team
// placed step refuses naming its binding rather than falling back to local.
func wirePlacement(rev *agentstep.Engine) {
	if !verb.SignedIn() {
		return
	}
	p := hostedPlacement{client: hosted.NewClient(resolveServer(""), hosted.DefaultStore(), nil)}
	rev.SetRemoteChannel(p)
	rev.SetTeamRunner(p)
}
