package agentstep

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/agentartifact"
	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/docindex"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/workitem"
	"github.com/bobmcallan/satelle/internal/worktree"
)

// A step's performer in a provider's cloud session (sty_82cffd60,
// interface = "cloud"). Everything is local except the work itself: the
// dispatch launches the session from the story worktree with the step's payload,
// waits for the session's pushed branch to carry the completion trailer, and
// brings that branch into the worktree. The step's exit gates then judge the
// result as they judge any performer's. Which provider, the branch's name and
// how the session is started are the adapter's (internal/agentcli); what the
// final commit body holds is the step skill's, collected only when the binding
// names a collect_doc.

const (
	// cloudPerformerSkill is the embedded, repo-overridable instruction block a
	// cloud performer is told after the step's own rubric.
	cloudPerformerSkill = "satelle-cloud-performer"
	// cloudNonceTrailer is the trailer key whose value is the dispatch nonce; the
	// tip commit carrying it is the only completion marker.
	cloudNonceTrailer = "Satelle-Nonce"
	// cloudDocType is the type a collected commit body is attached as, the one a
	// local performer's output attachment carries.
	cloudDocType = "output"

	// defaultCloudPoll is how often the remote branch is checked.
	defaultCloudPoll = 30 * time.Second
	// defaultCloudDeadline bounds the wait for a cloud binding that sets no
	// timeout. A cloud build is slow and unattended, so it is generous; the
	// binding's timeout= replaces it.
	defaultCloudDeadline = 60 * time.Minute
)

// unpushedRemoteRefusal is the refusal for a child placed remote (step.toml
// remote_agent, sty_dde8b6a4) whose worktree branch the cloud cannot see. The
// dispatch never pushes for the operator; the message names the child, the
// placement and the command that clears it, so the driver's missed push is a
// stop it can act on.
func (g *Engine) unpushedRemoteRefusal(ctx context.Context, id string, cause error) error {
	branch := worktree.CurrentBranch(ctx, g.repoRoot)
	if branch == "" {
		branch = "<branch>"
	}
	remote, err := worktree.UpstreamRemote(ctx, g.repoRoot)
	if err != nil {
		remote = "origin"
	}
	return fmt.Errorf("cloud dispatch refused: %s is placed remote and its branch %s is not pushed (%v) — push it before presenting the step: git push -u %s %s",
		id, branch, cause, remote, branch)
}

// dispatchCloud performs the step in a cloud session and collects the result.
// remotePlaced marks a step the placement rule sent here (remote_agent) rather
// than one whose own agent is a cloud binding.
func (g *Engine) dispatchCloud(ctx context.Context, item workitem.Item, toStatus, wfName, agent string,
	binding config.AgentBinding, composed []string, skill string, remotePlaced bool) (verb.DispatchResult, error) {
	res := verb.DispatchResult{Dispatched: true, Agent: agent, Skill: skill}
	harness := agentcli.HarnessOf(binding.CommandTemplate())

	// A cloud session returns a branch and nothing on stdout, so a skill that
	// needs a structured artifact or an attempt loop cannot be served by it.
	for _, name := range composed {
		body, err := g.skillBody(ctx, name)
		if errors.Is(err, docindex.ErrNotFound) {
			continue
		}
		if err != nil {
			return res, err
		}
		oc, cerr := agentartifact.ParseContract(body)
		if cerr != nil {
			return res, fmt.Errorf("skill %q output contract: %w", name, cerr)
		}
		ap, cerr := agentartifact.ParseAttemptPolicy(body)
		if cerr != nil {
			return res, fmt.Errorf("skill %q attempt policy: %w", name, cerr)
		}
		if oc.Active() || ap.Active() {
			return res, fmt.Errorf("cloud dispatch refused: skill %q declares an output contract/attempt policy that a cloud session cannot return; binding [%s]", name, agent)
		}
	}
	// Deliver the step's skills with the frontmatter dropped, and record what was
	// delivered; a declared skill that does not resolve refuses the dispatch
	// before anything starts (sty_23ceeca2).
	delivered, err := g.composeSkills(ctx, composed, true)
	if err != nil {
		return res, err
	}
	res.Skills = delivered.Manifest
	if rerr := g.undeliveredRefusal(ctx, delivered, wfName, toStatus, agent); rerr != nil {
		return res, rerr
	}
	if missing := delivered.Undelivered(); len(missing) > 0 {
		res.Evidence = &verb.EvidenceReport{Undelivered: missing} // record mode: flagged, not refused
	}
	deadline, err := binding.TimeoutDuration(g.cloudDefaultDeadline)
	if err != nil {
		return res, fmt.Errorf("named agent %q: invalid timeout in .satelle/workflows/agents.toml [%s]: %w", agent, agent, err)
	}
	if binding.CollectDoc != "" && g.attachArtifact == nil {
		return res, fmt.Errorf("cloud dispatch refused: binding [%s] collects document %q but no document attachment writer is configured", agent, binding.CollectDoc)
	}

	nonce, err := newCloudNonce()
	if err != nil {
		return res, err
	}
	branch, err := agentcli.CloudBranch(harness, item.ID, nonce)
	if err != nil {
		return res, err
	}
	if err := agentcli.CloudLaunchAvailable(harness); err != nil {
		return res, err
	}
	// The session is based on the pushed branch; the dispatch never pushes for the
	// operator, so an unpushed worktree is refused before anything starts.
	if _, err := worktree.PushedBranch(ctx, g.repoRoot); err != nil {
		if remotePlaced {
			return res, g.unpushedRemoteRefusal(ctx, item.ID, err)
		}
		return res, fmt.Errorf("cloud dispatch refused: %w", err)
	}
	remote, err := worktree.UpstreamRemote(ctx, g.repoRoot)
	if err != nil {
		return res, fmt.Errorf("cloud dispatch refused: %w", err)
	}

	prompt, sent, err := g.cloudPrompt(ctx, item, toStatus, agent, binding, delivered.Rubric, skill, branch, nonce)
	if err != nil {
		return res, err
	}

	// The log opens once there is a prompt to record; its summary is registered
	// before the launch, so a launch that fails still ends the log with one.
	dlog, _, closeLog := g.openDispatchLog(agent, item.ID, binding)
	defer closeLog()
	if dlog != nil {
		pj, _ := cloudPayloadJSON(sent)
		logInput(dlog, prompt, string(pj), sent, "", nil)
	}
	usageReason := harness + " cloud: usage unavailable"
	outcome := "failed"
	var failure error
	rec := &verb.CloudDispatch{Branch: branch, Doc: binding.CollectDoc}
	docPath := ""
	defer func() {
		if dlog == nil {
			return
		}
		if outcome == "collected" {
			dlog.write(agentcli.Event{Kind: agentcli.EventCompleted})
		} else {
			dlog.write(agentcli.Event{Kind: agentcli.EventFailed, Error: outcome})
		}
		s := dispatchSummary{
			Turns:     agentcli.TurnsUnavailableReason(binding.ResolvedInterface(), binding.CommandTemplate()),
			ToolCalls: agentcli.ToolCallsUnavailableReason(binding.ResolvedInterface(), binding.CommandTemplate()),
			Usage:     usageReason,
			Outcome:   outcome,
			Answer:    cloudAnswer(rec, docPath),
		}
		if failure != nil {
			s.Error = failure.Error()
		}
		dlog.summary(s)
	}()

	g.emitActivity(item.ID, "dispatch:"+toStatus, 1, 1)
	g.emitProgress("dispatching step %s to a %s cloud session (waits up to %s for its branch %s)…", toStatus, harness, deadline, branch)
	session, err := agentcli.LaunchCloud(ctx, harness, g.repoRoot, prompt)
	if err != nil {
		failure = fmt.Errorf("named agent %q failed performing step %q: %w", agent, toStatus, err)
		return res, failure
	}
	rec.SessionID, rec.URL = session.ID, session.URL
	if remotePlaced {
		rec.Placement = placementRemote
	}
	res.Command, res.Cloud = session.URL, rec
	g.emitProgress("cloud session started: %s — waiting up to %s for branch %s…", session.URL, deadline, branch)
	res.UsageNote = verb.UsageNote{
		UsageUnavailableReason: usageReason,
		CacheSplitUnavailable:  true,
	}
	defer func() {
		data := map[string]any{
			"agent": agent, "step": toStatus, "outcome": outcome, "session_id": rec.SessionID, "url": rec.URL,
			"branch": rec.Branch, "commit": rec.Commit, "collect_doc": rec.Doc,
		}
		if rec.Placement != "" {
			data["placement"] = rec.Placement
		}
		g.telemetryEvent(ctx, item.ID, "executor", "cloud_dispatch", data)
	}()
	fail := func(format string, a ...any) (verb.DispatchResult, error) {
		failure = fmt.Errorf("named agent %q failed performing step %q: %s (cloud session %s); the story worktree is unchanged",
			agent, toStatus, fmt.Sprintf(format, a...), session.URL)
		return res, failure
	}

	tip, err := worktree.WaitTrailerBranch(ctx, g.repoRoot, remote, branch, cloudNonceTrailer, nonce, g.cloudPoll, deadline)
	if err != nil {
		if errors.Is(err, worktree.ErrTrailerTimeout) {
			outcome = "timeout"
			return fail("branch %s did not carry the %s trailer within %s", branch, cloudNonceTrailer, deadline)
		}
		return fail("waiting for branch %s: %v", branch, err)
	}
	if binding.CollectDoc != "" && tip.Body == "" {
		return fail("binding collects %q but the tip commit %.8s has no message body", binding.CollectDoc, tip.Commit)
	}
	if err := worktree.CollectBranch(ctx, g.repoRoot, tip.Commit); err != nil {
		outcome = "conflict"
		return fail("%v", err)
	}
	rec.Commit = tip.Commit
	if binding.CollectDoc != "" {
		name, _, err := g.attachArtifact(ctx, item, binding.CollectDoc, cloudDocType, tip.Body)
		if err != nil {
			failure = fmt.Errorf("named agent %q collected %.8s for step %q but could not attach %q: %w (cloud session %s)",
				agent, tip.Commit, toStatus, binding.CollectDoc, err, session.URL)
			return res, failure
		}
		docPath = g.docPath(ctx, item.ID, name)
	}
	outcome = "collected"
	return res, nil
}

// cloudAnswer is what a cloud session returned: where it ran, the branch, the
// commit collected from it, and the path of the document its body was attached
// as.
func cloudAnswer(rec *verb.CloudDispatch, docPath string) string {
	var b strings.Builder
	for _, kv := range [][2]string{{"session", rec.URL}, {"branch", rec.Branch}, {"commit", rec.Commit}, {"doc", docPath}} {
		if kv[1] != "" {
			fmt.Fprintf(&b, "%s: %s\n", kv[0], kv[1])
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// docPath is the persistent file of the item's document name, or name itself
// when the resolver gives no path.
func (g *Engine) docPath(ctx context.Context, itemID, name string) string {
	if g.itemDocs != nil {
		for _, d := range g.itemDocs(ctx, itemID) {
			if d.Name == name && d.Path != "" {
				return d.Path
			}
		}
	}
	return name
}

// cloudPayloadJSON is the payload as the cloud prompt embeds it.
func cloudPayloadJSON(tp transitionPayload) ([]byte, error) {
	return json.MarshalIndent(tp, "", "  ")
}

// cloudPrompt composes what the session is told: the step rubric, then the
// cloud-performer skill, then the mechanism contract — data only. It refuses a
// prompt that would carry the hosted service's address or its session-token
// variable name: a cloud session reaches neither, and must not be told of them.
// It returns the payload the prompt embeds, built once: building it marks the
// messages it carries delivered.
func (g *Engine) cloudPrompt(ctx context.Context, item workitem.Item, toStatus, agent string,
	binding config.AgentBinding, rubric, skill, branch, nonce string) (string, transitionPayload, error) {
	performer, err := g.skillBody(ctx, cloudPerformerSkill)
	if err != nil {
		return "", transitionPayload{}, fmt.Errorf("cloud dispatch needs the %q skill: %w", cloudPerformerSkill, err)
	}
	performer = stripFrontmatter(performer)
	payload, _ := g.performerPayload(ctx, item, toStatus, skill, agent, binding, "")
	pj, err := cloudPayloadJSON(payload)
	if err != nil {
		return "", transitionPayload{}, fmt.Errorf("cloud dispatch payload: %w", err)
	}
	// The harness takes the prompt as one argv token, so it must never start with
	// '-' (a YAML frontmatter fence would be parsed as an option): lead with a
	// plain-text heading and carry no frontmatter block.
	var b strings.Builder
	fmt.Fprintf(&b, "Satelle cloud step %q for story %s\n\n", toStatus, item.ID)
	b.WriteString(strings.TrimSpace(rubric))
	b.WriteString("\n\n---\n\n")
	b.WriteString(strings.TrimSpace(performer))
	b.WriteString("\n\n---\n\n## Mechanism contract\n\n")
	fmt.Fprintf(&b, "branch: %s\nnonce: %s\ntrailer: %s: %s\n\npayload:\n\n```json\n%s\n```\n", branch, nonce, cloudNonceTrailer, nonce, pj)
	prompt := b.String()
	for _, secret := range cloudForbidden() {
		if strings.Contains(prompt, secret) {
			return "", transitionPayload{}, fmt.Errorf("cloud dispatch refused: the composed prompt contains %q — a cloud session must not be told of the hosted service or its session token", secret)
		}
	}
	return prompt, payload, nil
}

// cloudForbidden lists the strings a cloud prompt must never carry.
func cloudForbidden() []string {
	out := []string{hosted.SessionTokenEnv, config.DefaultHostedServer}
	if s := strings.TrimRight(config.ResolveHostedServer(config.Config{}), "/"); s != "" && s != config.DefaultHostedServer {
		out = append(out, s)
	}
	return out
}

// newCloudNonce is the per-dispatch marker the session puts in its final commit.
func newCloudNonce() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cloud dispatch nonce: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
