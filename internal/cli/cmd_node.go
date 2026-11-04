// `satelle node join` is what runs on a machine acting as a remote node: it
// creates the node's data dir and store, fetches the definition it runs from
// satelle.dev with its node token, installs and reindexes its substrate, joins
// the relay session, fetches the declared repos read-only, reports ready and
// serves the controller's jobs. It reads its secrets and ends its run through
// satelle.dev with the same token. A node of a task-kind remote (remote.toml
// [drive] kind = "task") also runs its wake: one execution of the remote's task,
// reported on satelle.dev's status route and followed by the node's end. It is a node mode, not a developer command:
// remotes are defined, published, secreted and launched through satelle-server,
// whose launcher builds this argv.
package cli

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/bobmcallan/satelle/internal/agentcli"
	"github.com/bobmcallan/satelle/internal/agentstep"
	"github.com/bobmcallan/satelle/internal/app"
	"github.com/bobmcallan/satelle/internal/buildinfo"
	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/doctor"
	"github.com/bobmcallan/satelle/internal/drive"
	"github.com/bobmcallan/satelle/internal/hosted"
	"github.com/bobmcallan/satelle/internal/remotechan"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/remotenode"
	"github.com/bobmcallan/satelle/internal/verb"
	"github.com/bobmcallan/satelle/internal/wfdot"
	"github.com/bobmcallan/satelle/internal/wfgovern"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// Seams a test replaces; production leaves them alone.
var (
	// nodeChannelOptions configure the node's end of the channel.
	nodeChannelOptions []remotechan.Option
	// nodeIdentity makes the node's key.
	nodeIdentity = remotechan.NewIdentity
	// nodeServer is the satelle.dev the node asks for its definition and secrets.
	nodeServer = func() string { return config.ResolveHostedServer(config.Config{}) }
	// nodeHTTP is the HTTP client for those calls; nil is a 30 second default.
	nodeHTTP *http.Client
	// nodeFetchEnv is added to git's environment when the node fetches its repos.
	nodeFetchEnv []string
	// nodeDriveOptions is how a task-kind node drives its execution: the engine's
	// performers and the declared rework relay, as `story drive` has them.
	nodeDriveOptions = func(cmd *cobra.Command) (drive.Options, error) {
		eng, a, err := engineForCmd(cmd)
		if err != nil {
			return drive.Options{}, err
		}
		eng.SetProcessSecrets(currentNodeProcessSecrets())
		return driveOptions(cmd, eng, a, cmd.ErrOrStderr()), nil
	}
	// nodeStatusResend, when set, replaces the interval a running task's heartbeat
	// is re-sent at (remotenode.DefaultStatusResendInterval, or a third of the
	// window the server reports).
	nodeStatusResend time.Duration
	// nodeSetupTimeout bounds each [setup] command.
	nodeSetupTimeout = 10 * time.Minute
)

// nodeEndTimeout bounds the one end call a stopping node makes.
const nodeEndTimeout = 10 * time.Second

func init() {
	node := &cobra.Command{
		Use:    "node",
		Short:  "Run this machine as a remote node",
		Hidden: true,
	}
	var root, relay, session, token string
	var tokenStdin bool
	join := &cobra.Command{
		Use:    "join",
		Short:  "Run on a remote node: set up from the definition satelle.dev serves and serve its jobs",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Read before the root is resolved or initialised, and before the
			// end call is deferred, so a bad token leaves the node untouched.
			if tokenStdin {
				t, err := readTokenStdin(cmd.InOrStdin())
				if err != nil {
					return err
				}
				token = t
			}
			return runNodeJoin(cmd, root, relay, session, token)
		},
	}
	// These flags are the launch contract: the server's launcher
	// (satelle-server sty_c9013137) builds `satelle node join --root <dir>
	// --relay <url> --session <id>` and hands over the node token either as
	// `--token <node token>` or, to keep it out of argv, a log and the process
	// list, as `--token-stdin` with the token on the first line of stdin. The
	// session is the node's id on satelle.dev, the key of every route the node
	// calls. The key line on stdout and the node's own log on stderr are the
	// other half of it.
	join.Flags().StringVar(&root, "root", remotenode.NodeRoot, "the node's working root")
	join.Flags().StringVar(&relay, "relay", "", "relay URL")
	join.Flags().StringVar(&session, "session", "", "channel session id, which is the node's id on satelle.dev")
	join.Flags().StringVar(&token, "token", "", "the node's bearer token for satelle.dev and the relay")
	join.Flags().BoolVar(&tokenStdin, "token-stdin", false, "read the node's bearer token from the first line of stdin, keeping it out of argv")
	for _, f := range []string{"relay", "session"} {
		_ = join.MarkFlagRequired(f)
	}
	join.MarkFlagsMutuallyExclusive("token", "token-stdin")
	join.MarkFlagsOneRequired("token", "token-stdin")
	node.AddCommand(join)
	register(node)
}

// readTokenStdin reads the node token from r: the first line, or all of r if it
// ends without a newline. It stops at the newline rather than at EOF, since the
// writer (a server's exec stdin stream) may keep stdin open, and it never closes
// r. Its errors are fixed text, so no token can reach them.
func readTokenStdin(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("join: --token-stdin: cannot read stdin")
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", errors.New("join: --token-stdin: no token on stdin")
	}
	return token, nil
}

// runNodeJoin is the node's side. It runs in the working root: init creates the
// data dir and store, the key is printed for the controller to pin, the node
// fetches and installs its definition, and it then enrolls and serves. However it
// stops, it tells satelle.dev once, which revokes its token.
func runNodeJoin(cmd *cobra.Command, root, relay, session, token string) error {
	client := remotenode.NewNodeClient(nodeServer(), session, token, nodeHTTP)
	// endNode tells satelle.dev the node has ended. It is once-guarded
	// (NodeClient.End), so a job's teardown calls it before releasing its sprite
	// hold and this defer, which covers every other way join stops, is then a no-op.
	endNode := func() {
		ctx, cancel := context.WithTimeout(context.Background(), nodeEndTimeout)
		defer cancel()
		if err := client.End(ctx); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "join: telling satelle.dev the node has ended: %v\n", err)
		}
	}
	defer endNode()

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The node heartbeats on satelle.dev's status route from the moment it joins, so
	// a slow definition install or [setup] command (each may run for
	// nodeSetupTimeout, far past the window after which satelle.dev lists a
	// silent node lost) does not end it. The status says whether a job is in flight.
	// The beat stops before the end call (this defer is registered after it), so no
	// heartbeat follows the end, and before a failed start is reported, so that
	// report is the last status. A task-kind node keeps it until the moment its
	// run's own heartbeat starts (runTaskWake), so a stall anywhere before the run,
	// a secret read, the setup or the repo fetch, is still heard.
	node := remotenode.NewNode()
	beat := remotenode.StartHeartbeat(ctx, client, nodeStatusResend, node.Status, cmd.ErrOrStderr())
	defer beat.Stop()

	root, err := expandNodeRoot(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if err := os.Chdir(root); err != nil {
		return err
	}
	// noWorkspace: the node is not registered in any workspace. Init's output is
	// the node's own log; stdout is reserved for the key line. Init's process-hook
	// step is deferred until the definition names the harnesses its bindings run.
	if err := runInitWith(cmd.ErrOrStderr(), root, true, nil, false); err != nil {
		return fmt.Errorf("join: init %s: %w", root, err)
	}

	id, err := nodeIdentity()
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), remotenode.KeyLinePrefix+base64.StdEncoding.EncodeToString(id.Public()))

	dataDir := filepath.Join(root, config.DefaultDataDir)
	def, version, err := installNodeDefinition(ctx, cmd, client, dataDir)
	if err != nil {
		reportNodeStartFailure(ctx, cmd, client, beat, err)
		return fmt.Errorf("join: %w", err)
	}
	if err := installNodeHooks(cmd.ErrOrStderr(), root, dataDir); err != nil {
		reportNodeStartFailure(ctx, cmd, client, beat, err)
		return fmt.Errorf("join: %w", err)
	}

	// Every key the definition declares (a [secrets] name, a github-app run token
	// and a satelle:<name> team value alike) is read once here by name, before
	// anything of the run starts, and held in the node's process environment for
	// the life of the fire, so every run the node is woken for inherits it (the
	// task drive's performers and gates, and a relay agent job). It is gone from
	// the environment when join returns. The definition was validated as v1 when
	// it was installed (remotedef.Parse), so an old-shape one never gets here.
	secrets, err := loadNodeSecrets(ctx, client, def, cmd.ErrOrStderr())
	if err != nil {
		reportNodeStartFailure(ctx, cmd, client, beat, err)
		return fmt.Errorf("join: %w", err)
	}
	defer setNodeProcessSecrets(secrets, cmd.ErrOrStderr())()
	// The definition itself (key names, never values) rides the same environment,
	// so a command the run starts knows the remote's repo and credential.
	envDef, err := def.EnvValue()
	if err != nil {
		reportNodeStartFailure(ctx, cmd, client, beat, err)
		return fmt.Errorf("join: definition: %w", err)
	}
	defer setNodeEnv(remotedef.EnvDefinition, envDef)()

	// The definition's [setup] runs here, where the node is created and started:
	// on_create, then post_start, after its definition is read and before it
	// joins the relay or works a run. Each join is a fresh node, so both run once.
	for _, phase := range []struct {
		name string
		cmds []string
	}{{remotenode.SetupOnCreate, def.Setup.OnCreate}, {remotenode.SetupPostStart, def.Setup.PostStart}} {
		if err := remotenode.RunSetup(ctx, root, phase.name, phase.cmds, nodeSetupTimeout, cmd.ErrOrStderr()); err != nil {
			reportNodeStartFailure(ctx, cmd, client, beat, err)
			return fmt.Errorf("join: %w", err)
		}
	}

	// A task-kind node is woken by satelle.dev, which leaves no local end on the
	// relay to pair with, so it never joins the relay: everything its run needs is
	// on the node API under its node token.
	if def.RunsTask() {
		report, perr := prepareNode(ctx, root, dataDir, def, version)
		if perr != nil {
			reportNodeStartFailure(ctx, cmd, client, beat, perr)
			return fmt.Errorf("join: %w", perr)
		}
		logNodeReady(cmd.ErrOrStderr(), report)
		// The wake is the node's one job: its swap is made for it and gone after.
		disableSwap, serr := remotenode.EnableSwap(ctx, def.Machine.Swap)
		if serr != nil {
			reportNodeStartFailure(ctx, cmd, client, beat, serr)
			return fmt.Errorf("join: %w", serr)
		}
		defer logSwapOff(cmd.ErrOrStderr(), disableSwap)
		// On a fly-sprite the wake holds the sprite awake until it concludes, and
		// past that until the node has told satelle.dev it ended: a sprite paused
		// between the release and the end call leaves the node silent, listed lost.
		release := spriteHold(ctx, cmd, session)
		defer release()
		err := runTaskWake(ctx, cmd, beat, client, root, session, token, def)
		endAfterJob(beat, endNode, release)
		return err
	}

	// A node a starter asked satelle.dev for to perform a step (mode=performer)
	// publishes its channel key before it dials, once, so the starter pins it from
	// satelle.dev rather than from this node's stdout (sty_7028d996). Every other
	// mode prints the key line above and publishes nothing.
	desc, rerr := client.Run(ctx)
	if rerr != nil {
		return fmt.Errorf("join: %w", rerr)
	}
	if desc.Mode == remotenode.ModePerformer {
		if perr := client.PublishChannelKey(ctx, id.Public()); perr != nil {
			return fmt.Errorf("join: %w", perr)
		}
	}

	sess, err := remotechan.Join(ctx, relay, session, id, token, nodeChannelOptions...)
	if err != nil {
		var refused *remotechan.RelayStatusError
		if errors.As(err, &refused) && refused.Status == http.StatusUnauthorized {
			return &remotenode.TokenRefusedError{Route: "relay upgrade for session " + session}
		}
		return fmt.Errorf("join: %w", err)
	}
	defer sess.Close()

	node.Handle(remotenode.AgentJob, agentJobHandler(dataDir, root, secrets))
	node.Handle(remotenode.StoryReportJob, remotenode.StoryReportHandler(nodeProcess))
	report, perr := prepareNode(ctx, root, dataDir, def, version)
	if perr != nil {
		report = remotenode.Report{Error: perr.Error()}
	}
	// A story node serves one story: that is its job, and its swap lives as long.
	disableSwap, serr := remotenode.EnableSwap(ctx, def.Machine.Swap)
	if serr != nil {
		report = remotenode.Report{Error: serr.Error()}
	} else {
		defer logSwapOff(cmd.ErrOrStderr(), disableSwap)
	}
	// So too its sprite hold: the sprite is held awake for the whole serve, and
	// through the end call after it.
	release := spriteHold(ctx, cmd, session)
	defer release()
	err = node.Serve(ctx, sess, report)
	endAfterJob(beat, endNode, release)
	return err
}

// endAfterJob is a node's teardown once its one job is over, in the order
// satelle.dev must see it: the heartbeat stops (so no status follows the
// job's last one), the node ends, and only then is its sprite hold released.
// Each step is idempotent, so join's deferred backstops are then no-ops.
func endAfterJob(beat *remotenode.Heartbeat, endNode, release func()) {
	beat.Stop()
	endNode()
	release()
}

// spriteHold holds a fly-sprite awake for the node's job and returns what
// releases it (remotenode.SpriteHold): a sprite pauses a node whose only traffic
// is its own outbound calls. Off a sprite it holds nothing.
func spriteHold(ctx context.Context, cmd *cobra.Command, session string) (release func()) {
	return remotenode.SpriteHold{Name: "satelle-" + session, Out: cmd.ErrOrStderr()}.Start(ctx)
}

// logSwapOff turns off and removes the job's swapfile, logging a failure: the
// job is over, so it is the node's log that hears it.
func logSwapOff(log io.Writer, disable func() error) {
	if err := disable(); err != nil {
		fmt.Fprintf(log, "join: removing the job's swap: %v\n", err)
	}
}

// nodeProcessSecrets is what join placed in the node's process environment for
// the fire (key → value), for the task drive's engine to scrub from performer
// output. It is never written anywhere.
var nodeProcessSecrets struct {
	mu  sync.Mutex
	set map[string]string
}

// currentNodeProcessSecrets is the set join placed, nil outside a fire.
func currentNodeProcessSecrets() map[string]string {
	nodeProcessSecrets.mu.Lock()
	defer nodeProcessSecrets.mu.Unlock()
	return nodeProcessSecrets.set
}

// setNodeProcessSecrets places secrets in the process environment and returns
// what takes them out again: each key back to what it was before, or unset.
//
// It also hands them off (remotedef.WriteHandoff) to a satelle a command of the
// run starts: that satelle is a child of the performer's harness, which need not
// pass these variables to its children (a reviewer `satelle pr approve` starts
// would otherwise run without the declared CLAUDE_CODE_OAUTH_TOKEN). The
// hand-off and its path variable (remotedef.EnvSecretsFile) are gone on
// restore. A hand-off that cannot be written is a warning on log, naming no
// value: the environment still carries the secrets.
func setNodeProcessSecrets(secrets map[string]string, log io.Writer) (restore func()) {
	type prior struct {
		value string
		had   bool
	}
	prev := make(map[string]prior, len(secrets))
	for k, v := range secrets {
		old, had := os.LookupEnv(k)
		prev[k] = prior{old, had}
		_ = os.Setenv(k, v)
	}
	nodeProcessSecrets.mu.Lock()
	nodeProcessSecrets.set = secrets
	nodeProcessSecrets.mu.Unlock()
	unhand := func() {}
	if len(secrets) > 0 {
		if path, err := remotedef.WriteHandoff(secrets); err != nil {
			fmt.Fprintf(log, "join: warning: secrets hand-off not written (%v); a satelle the run starts reads only its inherited environment\n", err)
		} else {
			unsetEnv := setNodeEnv(remotedef.EnvSecretsFile, path)
			unhand = func() {
				unsetEnv()
				if err := remotedef.RemoveHandoff(path); err != nil {
					fmt.Fprintf(log, "join: warning: secrets hand-off not removed: %v\n", err)
				}
			}
		}
	}
	return func() {
		unhand()
		nodeProcessSecrets.mu.Lock()
		nodeProcessSecrets.set = nil
		nodeProcessSecrets.mu.Unlock()
		for k, p := range prev {
			if p.had {
				_ = os.Setenv(k, p.value)
			} else {
				_ = os.Unsetenv(k)
			}
		}
	}
}

// setNodeEnv sets key to value in the process environment for the fire and
// returns what puts the prior value (or its absence) back.
func setNodeEnv(key, value string) (restore func()) {
	old, had := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	return func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

// loadNodeSecrets reads every key the definition declares (SecretEntries), once
// each, by name, through the node's ordinary secret read: the single read of a
// node's secrets, whose result join holds in the process environment for the
// fire and hands a relay agent job. satelle.dev serves each from its source: a
// [secrets] name from the secrets backend, a satelle:<name> override from its
// own store, and a github-app override as an installation token limited to the
// definition's [[repo]] entries, answered with when it expires. The node never
// resolves an override itself. Any refusal stops the node, naming the key; the
// owner-run refusal says so plainly, and a revoked node token is returned as is.
// A value already expired is refused. Only key names and their source are
// logged, never a value.
func loadNodeSecrets(ctx context.Context, client *remotenode.NodeClient, def remotedef.Definition, log io.Writer) (map[string]string, error) {
	var out map[string]string
	for _, d := range def.SecretEntries() {
		value, expires, err := client.SecretWithExpiry(ctx, d.Key)
		if err != nil {
			var refusal *remotenode.SecretRefusal
			if d.Source == remotedef.SourceRunToken && errors.As(err, &refusal) && refusal.Err.Code == remotenode.SecretRefusedGitHubAppOwnerRun {
				return nil, fmt.Errorf("secret %s: a local owner run cannot use a github-app credential (%s): %s",
					d.Key, remotenode.SecretRefusedGitHubAppOwnerRun, refusal.Err.Reason)
			}
			return nil, err
		}
		if !expires.IsZero() && !expires.After(time.Now()) {
			return nil, fmt.Errorf("secret %s: the value satelle.dev answered with expired at %s", d.Key, expires.Format(time.RFC3339))
		}
		if out == nil {
			out = map[string]string{}
		}
		out[d.Key] = value
		fmt.Fprintf(log, "join: secret %s read (%s)\n", d.Key, secretSource(d))
	}
	return out, nil
}

// secretSource names where a declared key comes from, for the join log: the
// secrets backend, satelle's store under its name, or the GitHub a github-app
// token is minted for.
func secretSource(e remotedef.SecretEntry) string {
	switch e.Source {
	case remotedef.SourceRunToken:
		return remotedef.OverrideGitHubApp + " " + remotedef.DefaultGitHubHost
	case remotedef.SourceSatelle:
		return "satelle:" + e.Name
	}
	return string(e.Source)
}

// reportNodeStartFailure tells satelle.dev why the node could not start, on the
// status of the run it was started for. It reads the run only now, on the failure
// path, so the success path reads it exactly where it did. Both calls outlive a
// cancelled ctx, as a SIGTERM that stopped the setup is still reported. A run
// that cannot be read is logged; the failure is returned by the caller regardless.
// It stops beat first, so the concluded status is the last one satelle.dev hears
// and no running re-send follows it.
func reportNodeStartFailure(ctx context.Context, cmd *cobra.Command, client *remotenode.NodeClient, beat *remotenode.Heartbeat, cause error) {
	beat.Stop()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeEndTimeout)
	defer cancel()
	desc, err := client.Run(rctx)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "join: reading the run to report the failed start: %v\n", err)
		return
	}
	remotenode.ReportStartFailure(ctx, client, desc.StoryID, cause.Error(), cmd.ErrOrStderr())
}

// logNodeReady writes what a task node would have reported ready to its own log:
// with no relay end to read a ready report, the log is where it is seen.
func logNodeReady(log io.Writer, report remotenode.Report) {
	repos := make([]string, len(report.Repos))
	for i, r := range report.Repos {
		repos[i] = r.Name + "@" + r.Commit
	}
	fmt.Fprintf(log, "join: ready: definition=%s allow=%s repos=%s bindings=%s workflows=%s\n", report.DefinitionVersion,
		strings.Join(report.Allow, ","), strings.Join(repos, ","), strings.Join(report.Bindings, ","),
		strings.Join(report.Workflows, ","))
}

// runTaskWake runs a task-kind remote's single wake against the node API. The
// node's join heartbeat (beat) runs until the line before RunTask, whose first act
// is its queued status, so satelle.dev hears the node throughout and no join
// heartbeat follows the run's first (Stop waits for one in flight). A wake that
// cannot be prepared is reported as a failed start; a run that cannot be made or
// driven is returned, having already reported itself concluded as rejected. A
// node at a pull-only root (refusePullOnly) runs its task and pushes no workstate.
//
// The node syncs its work state as a local satelle does, to the project its run
// descriptor names, over Sync and Chain with the node token as the credential
// (hosted.NodeTokenStore). It has no bound checkout, so it sends no location.
func runTaskWake(ctx context.Context, cmd *cobra.Command, beat *remotenode.Heartbeat, client *remotenode.NodeClient,
	root, session, token string, def remotedef.Definition) error {
	opts, desc, err := prepareTaskWake(ctx, cmd, client)
	if err != nil {
		reportNodeStartFailure(ctx, cmd, client, beat, err)
		return err
	}
	push := true
	if err := refusePullOnly(root); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "workstate push skipped: %v\n", err)
		push = false
	}
	tr := remotenode.TaskRun{
		Def: def, Desc: desc, NodeID: session, Client: client, Drive: opts, ResendEvery: nodeStatusResend,
	}
	// A node that pushes reads the hosted chains it writes to first, so its rows
	// build on the hosted head; one that pushes nothing has no head to build on.
	if push {
		server := nodeServer()
		hc := hosted.NewClient(server, hosted.NodeTokenStore{Server: server, Token: token}, nodeHTTP)
		tr.Publish, tr.Fetch = nodePublish(cmd, hc, server, desc.Project), nodeChainFetch(cmd, hc, server, desc.Project)
	}
	beat.Stop()
	res, rerr := remotenode.RunTask(ctx, tr)
	if rerr != nil {
		return fmt.Errorf("join: the task run %s: %w", res.Execution, rerr)
	}
	return nil
}

// errNodeNoProject: the run descriptor names no project, so the node has nowhere
// to sync its work state to.
var errNodeNoProject = errors.New("the run descriptor names no project")

// nodeChainFetch is a task run's pre-write fetch (remotenode.TaskRun.Fetch): for
// each id it reads the hosted chain through the chain read a local satelle uses
// (ChainConn.ReadStory) and applies it to the node's store through the same
// applier a trimmed story's fetch uses (applyHostedChain), so the rows the run
// then appends take the hosted head as their base (sty_0d621eaf). A refusal that
// stops a push (stopsPush) stops the run before any further read or write.
//
// A story the node's store does not hold, as a fresh node holds none, is brought
// in by the scoped pass `satelle sync workstate pull <id>` makes
// (runScopedWorkstateSnapshot): its hosted row, then its hosted chain, the row
// rebuilt from the chain's fold, so story verbs find it at its chain's head
// status (sty_a291a081). One the hosted project does not hold either is
// remotenode.ErrSubjectAbsent.
func nodeChainFetch(cmd *cobra.Command, hc *hosted.Client, server, project string) func(context.Context, ...string) error {
	return func(ctx context.Context, ids ...string) error {
		if project == "" {
			return errNodeNoProject
		}
		a, err := appFrom(cmd)
		if err != nil {
			return err
		}
		var conn *hosted.ChainConn
		defer func() {
			if conn != nil {
				_ = conn.Close()
			}
		}()
		var errs []error
		for _, id := range ids {
			if id == "" {
				continue
			}
			_, gerr := a.Store.Stories.Get(ctx, id)
			if errors.Is(gerr, workitem.ErrNotFound) {
				if perr := nodePullStory(ctx, cmd, a, hc, server, project, id); perr != nil {
					if stopsPush(perr) || errors.Is(perr, remotenode.ErrSubjectAbsent) {
						return perr
					}
					errs = append(errs, perr)
				}
				continue
			}
			if gerr != nil {
				errs = append(errs, gerr)
				continue
			}
			if conn == nil {
				if conn, err = hc.OpenChain(ctx); err != nil {
					return err
				}
			}
			events, _, ferr := conn.ReadStory(ctx, project, id, "")
			if stopsPush(ferr) {
				return fmt.Errorf("%w: %w", remotenode.ErrRunRefused, ferr)
			}
			if ferr == nil {
				_, ferr = applyHostedChain(ctx, a, id, events)
			}
			if ferr != nil {
				errs = append(errs, ferr)
			}
		}
		return errors.Join(errs...)
	}
}

// nodePullStory is the scoped pull of one story the node's store does not hold.
// A refusal that stops a push is returned as remotenode.ErrRunRefused, a story
// the server lacks as remotenode.ErrSubjectAbsent.
func nodePullStory(ctx context.Context, cmd *cobra.Command, a *app.App, hc *hosted.Client, server, project, id string) error {
	_, err := runScopedWorkstateSnapshot(ctx, a, cmd.ErrOrStderr(), hc, server, project,
		map[string]bool{"stories": true, "ledger": true}, false, snapshotModePull,
		scopeResolution{IDs: []string{id}, Set: scopeSet{id: true}})
	var absent scopeAbsentError
	switch {
	case stopsPush(err):
		return fmt.Errorf("%w: %w", remotenode.ErrRunRefused, err)
	case errors.As(err, &absent):
		return fmt.Errorf("%w: %s in project %s: %w", remotenode.ErrSubjectAbsent, id, project, err)
	}
	return err
}

// nodePublish is a task run's publish (remotenode.TaskRun.Publish): the push a
// local `satelle sync workstate push` scoped to the execution and its subject
// makes, through the same collector (collectWorkstateRows), the same story
// appends (planStoryAppends, publishStoryEvents) and the same Sync/Apply, with
// the node token as the credential. A refusal that stops a push (stopsPush) is
// returned before anything else is sent.
func nodePublish(cmd *cobra.Command, hc *hosted.Client, server, project string) func(context.Context, workitem.Item, string) (remotenode.PublishReport, error) {
	return func(ctx context.Context, exec workitem.Item, subject string) (remotenode.PublishReport, error) {
		rep := remotenode.PublishReport{Project: project}
		if project == "" {
			return rep, errNodeNoProject
		}
		a, err := appFrom(cmd)
		if err != nil {
			return rep, err
		}
		scope := scopeSet{exec.ID: true}
		if subject != "" {
			scope[subject] = true
		}
		batch, _, _, _, err := collectWorkstateRows(ctx, a, workstateOptIn(config.PersonalScope), hosted.WorkstateCursor{}, scope)
		if err != nil {
			return rep, err
		}
		cursor, err := hosted.LoadWorkstateCursor(server, project, a.RepoRoot)
		if err != nil {
			return rep, fmt.Errorf("load workstate cursor: %w", err)
		}
		plan, err := planStoryAppends(ctx, a, scope, cursor.StoryHeads)
		if err != nil {
			return rep, err
		}
		var appendErr error
		rep.Stories, appendErr = publishStoryEvents(ctx, cmd.ErrOrStderr(), a, hc, server, project, plan, nil)
		if stopsPush(appendErr) {
			return rep, appendErr
		}
		res, err := hc.Apply(ctx, project, hosted.WorkstateIngest{Items: batch.Items, Ledger: withoutStoryEntries(batch.Ledger), Outputs: batch.Outputs})
		if err != nil {
			return rep, fmt.Errorf("apply workstate: %w", err)
		}
		rep.Items, rep.Ledger, rep.Outputs = res.Items, res.Ledger, res.Outputs
		return rep, appendErr
	}
}

// prepareTaskWake reads the wake's subject and builds how its execution is driven.
// Either failing means the node cannot do its one job, so it is reported as a
// failed start.
func prepareTaskWake(ctx context.Context, cmd *cobra.Command, client *remotenode.NodeClient) (drive.Options, remotenode.RunDescriptor, error) {
	desc, err := client.Run(ctx)
	if err != nil {
		return drive.Options{}, desc, fmt.Errorf("join: %w", err)
	}
	opts, err := nodeDriveOptions(cmd)
	if err != nil {
		return drive.Options{}, desc, fmt.Errorf("join: driving the task: %w", err)
	}
	return opts, desc, nil
}

// checkNodeTask refuses a task-kind definition whose task the node does not have:
// the bundle's tasks/tsk_<name>.md, ingested when the substrate was reindexed.
func checkNodeTask(ctx context.Context, def remotedef.Definition) error {
	raw, err := verb.Dispatch(ctx, "task-get", []byte(`{"id":`+strconv.Quote(def.TaskID())+`}`))
	var task workitem.Item
	if err == nil {
		err = json.Unmarshal(raw, &task)
	}
	if err != nil || task.Kind != workitem.KindTask || task.Archived {
		return fmt.Errorf("remote definition names task %q (%s), which the node does not have", def.Drive.Task, def.TaskID())
	}
	return nil
}

// installNodeDefinition fetches the node's definition with its node token,
// replaces the node's substrate with the definition's own and reindexes it through
// the same pass `satelle reindex` runs. It returns what the node reads of the
// definition's remote.toml and the version the definition was fetched at. Nothing
// is installed unless the whole bundle checks out.
func installNodeDefinition(ctx context.Context, cmd *cobra.Command, client *remotenode.NodeClient, dataDir string) (remotedef.Definition, string, error) {
	bundle, version, err := client.Definition(ctx)
	if err != nil {
		return remotedef.Definition{}, "", err
	}
	def, err := remotenode.InstallDefinition(dataDir, bundle)
	if err != nil {
		return remotedef.Definition{}, "", fmt.Errorf("definition: %w", err)
	}
	// The app opens over the installed definition, so the gates and dispatches it
	// wires bind the remote's own agents layer, not init's scaffold. The bindings
	// are still judged later by resolving them (prepareNode).
	if err := openAppForCmd(cmd); err != nil {
		return remotedef.Definition{}, "", err
	}
	a, err := appFrom(cmd)
	if err != nil {
		return remotedef.Definition{}, "", err
	}
	if err := reindexSubstrate(cmd, a, false); err != nil {
		return remotedef.Definition{}, "", fmt.Errorf("reindex: %w", err)
	}
	return def, version, nil
}

func expandNodeRoot(root string) (string, error) {
	if root == remotenode.NodeRoot || strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(root, "~/")), nil
	}
	return filepath.Abs(root)
}

// prepareNode readies the node from the definition it installed, in order: a
// task-kind definition's task is found in the store, the repos are fetched and
// every binding is resolved. Anything that fails is the node's error,
// which the controller reports.
func prepareNode(ctx context.Context, root, dataDir string, def remotedef.Definition, version string) (remotenode.Report, error) {
	if def.RunsTask() {
		if err := checkNodeTask(ctx, def); err != nil {
			return remotenode.Report{}, err
		}
	}
	defName := fmt.Sprintf("%s at version %s", remotenode.DefinitionFile, version)
	repos, err := remotenode.FetchRepos(ctx, filepath.Join(root, "repos"), def, defName, def.Repos, nodeFetchEnv)
	if err != nil {
		return remotenode.Report{}, err
	}
	bindings, err := resolveNodeBindings(dataDir)
	if err != nil {
		return remotenode.Report{}, err
	}
	categories, err := doneCategories(dataDir)
	if err != nil {
		return remotenode.Report{}, err
	}
	process, procRoot, err := nodeProcess()
	if err != nil {
		return remotenode.Report{}, err
	}
	return remotenode.Report{
		Version: buildinfo.Resolve().Version, DefinitionVersion: version, Workflows: categories,
		Allow: def.Allow(), Repos: repos, Bindings: bindings,
		Now: time.Now().UnixNano(), Hashes: config.ProcessHashes(process, procRoot),
	}, nil
}

// nodeProcess is the node's process config and the root it is anchored at: the
// node's working root, which join has made the working directory.
func nodeProcess() (config.Config, string, error) {
	process, _, processRoot, _, err := config.LoadInvokingProcess()
	if err != nil {
		return config.Config{}, "", fmt.Errorf("the node's process config: %w", err)
	}
	return process, processRoot, nil
}

// resolveNodeBindings resolves every binding the remote's agents.toml defines
// through agentstep.LookupRunner, the resolution a dispatch uses, and names every
// one that does not resolve.
func resolveNodeBindings(dataDir string) ([]string, error) {
	all, err := nodeBindings(dataDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)

	var resolved []string
	var failed []error
	for _, n := range names {
		b := all[n]
		iface := b.ResolvedInterface()
		if iface == config.InterfaceCloud {
			continue // dispatched by the platform, not run by this node
		}
		if _, err := agentstep.LookupRunner(iface, b.CommandTemplate()); err != nil {
			failed = append(failed, fmt.Errorf("binding %q: %w", n, err))
			continue
		}
		resolved = append(resolved, n)
	}
	if len(failed) > 0 {
		return nil, errors.Join(failed...)
	}
	return resolved, nil
}

// nodeBindings is every binding the remote's agents.toml defines, by name: the
// executor and reviewer seats when set, and each named agent.
func nodeBindings(dataDir string) (map[string]config.AgentBinding, error) {
	cfg, err := config.LoadAgents(dataDir)
	if err != nil {
		return nil, fmt.Errorf("agents.toml: %w", err)
	}
	all := map[string]config.AgentBinding{}
	if cfg.Executor.Command != "" {
		all["executor"] = cfg.Executor
	}
	if cfg.Reviewer.Command != "" {
		all["reviewer"] = cfg.Reviewer
	}
	for n, b := range cfg.Agents {
		all[n] = b
	}
	return all, nil
}

// nodeHarnesses names, sorted and de-duplicated, the harnesses whose process
// hooks init can install (claude, grok) that the remote's node-run bindings
// run, each resolved from its binding command by agentcli.HarnessOf. A cloud
// binding is dispatched by the platform, not run on the node, so it names none.
func nodeHarnesses(dataDir string) ([]string, error) {
	all, err := nodeBindings(dataDir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, b := range all {
		if b.ResolvedInterface() == config.InterfaceCloud {
			continue
		}
		h := agentcli.HarnessOf(b.CommandTemplate())
		if (h == agentcli.HarnessClaude || h == agentcli.HarnessGrok) && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out, nil
}

// installNodeHooks installs satelle's process hooks on the node for the
// harnesses its definition's bindings run, through init's own hook step with
// those harnesses forced, as `satelle init --harness` does locally. With none,
// init's step reports that no hooks were installed, as join did before.
func installNodeHooks(out io.Writer, root, dataDir string) error {
	hs, err := nodeHarnesses(dataDir)
	if err != nil {
		return fmt.Errorf("process hooks: %w", err)
	}
	if err := ensureProcessHooks(out, root, hs); err != nil {
		return fmt.Errorf("process hooks: %w", err)
	}
	if len(hs) > 0 {
		fmt.Fprintf(out, "join: process hooks installed: %s\n", strings.Join(hs, ","))
	}
	return nil
}

// doneCategories lists the workflow categories of the remote's effective
// workflow: the bundle's workflows/done.toml merged by name over the embedded
// default, or the embedded default alone when the bundle carries no workflow
// tree. It resolves through doctor.GoverningWorkflows and
// wfgovern.RouteSourceOf, the route resolution the rest of satelle uses, so a
// bundle need not carry a done.toml for its node to report what it runs.
func doneCategories(dataDir string) ([]string, error) {
	rs := wfgovern.RouteSourceOf(doctor.GoverningWorkflows(dataDir))
	if rs.Unreadable.Path != "" {
		return nil, fmt.Errorf("the remote's workflow %s is unreadable: %s", rs.Unreadable.Path, rs.Unreadable.Reason)
	}
	if rs.Done == "" {
		return nil, errors.New("the remote's workflow route declares no done.toml")
	}
	lists, err := wfdot.ParseDone(rs.Done)
	if err != nil {
		return nil, fmt.Errorf("the remote's done.toml: %w", err)
	}
	out := make([]string, len(lists))
	for i, l := range lists {
		out[i] = l.Category
	}
	return out, nil
}

// agentJobHandler answers an agent job by running the remote's agent binding on
// the message: the executor if it names a command, else the first agent binding
// by name that does. The binding's env is not applied here. The agent gets the
// secrets the definition declares, as join read them once (loadNodeSecrets), in
// its process environment; the handler makes no node-API call. A secret
// satelle.dev refuses, or a revoked node token, stops the node at join, so no job
// runs without its secrets.
func agentJobHandler(dataDir, root string, secrets map[string]string) remotenode.Handler {
	return func(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
		var req remotenode.AgentRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		cfg, err := config.LoadAgents(dataDir)
		if err != nil {
			return nil, err
		}
		b, name, ok := talkBinding(cfg, req.Binding)
		if !ok {
			if req.Binding != "" {
				return nil, fmt.Errorf("remote defines no runnable binding %q", req.Binding)
			}
			return nil, errors.New("the remote defines no agent binding that runs a command")
		}
		runner, err := agentstep.LookupRunner(b.ResolvedInterface(), b.CommandTemplate())
		if err != nil {
			return nil, fmt.Errorf("binding %q: %w", name, err)
		}
		system := "You are the agent on a remote node. Answer the developer's message."
		if req.SystemPrompt != "" {
			system = req.SystemPrompt
		}
		raw, err := runner.Run(ctx, agentcli.Request{
			SystemPrompt: system,
			Payload:      req.Text, AllowedTools: b.Tools, Model: b.Model, Dir: root,
			Env: secrets,
		})
		if err != nil {
			return nil, fmt.Errorf("binding %q: %w", name, err)
		}
		text, _ := agentcli.UnwrapUsage(raw)
		return json.Marshal(remotenode.AgentReply{Text: strings.TrimSpace(string(text))})
	}
}

// talkBinding picks the binding that answers a message. When want names a
// binding (a performed step's allocated binding, sty_7028d996) that binding
// answers, if the node's agents.toml defines it and it runs; otherwise there is
// none, and the caller reports it. With no want it is the executor if it runs,
// else the first runnable binding by name.
func talkBinding(cfg config.AgentsConfig, want string) (config.AgentBinding, string, bool) {
	runs := func(b config.AgentBinding) bool {
		return b.CommandTemplate() != "" && !config.IsInLoopCommand(b.CommandTemplate()) &&
			b.ResolvedInterface() != config.InterfaceCloud
	}
	if want != "" {
		b, found := cfg.Agents[want]
		if want == "executor" {
			b, found = cfg.Executor, true
		}
		if found && runs(b) {
			return b, want, true
		}
		return config.AgentBinding{}, "", false
	}
	if runs(cfg.Executor) {
		return cfg.Executor, "executor", true
	}
	names := make([]string, 0, len(cfg.Agents))
	for n := range cfg.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if runs(cfg.Agents[n]) {
			return cfg.Agents[n], n, true
		}
	}
	return config.AgentBinding{}, "", false
}
