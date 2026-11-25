package remotenode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/gitfacts"
	"github.com/bobmcallan/satelle/internal/item"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/ledger"
	"github.com/bobmcallan/satelle/internal/remote/provider"
	"github.com/bobmcallan/satelle/internal/remotedef"
	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

const LocalNodeDir = "/node"

const RemotesDir = kernel.RemotesDir

const (
	localDefinition = "definition.bundle"
	localSubstrate  = "substrate.bundle"
	localRunFile    = "run.json"
	localOutcome    = "outcome.json"
)

// NodeLayout is the node layout this binary writes into a node's in/ and
// implements when it runs as the node; run.json names it.
const NodeLayout = 1

// legacyNodeLayout is the layout taken for a run.json that names none, as
// bundles built before run.json named a layout do.
const legacyNodeLayout = 1

const (
	RunRunning   = "running"
	RunConcluded = "concluded"
	RunStopped   = "stopped"
	RunLost      = "lost"
)

func RunFinished(state string) bool { return state == RunConcluded || state == RunStopped }

func validRemoteID(id string) error {
	if err := filebundle.CheckPath(id, nil); err != nil || strings.Contains(id, "/") {
		return fmt.Errorf("%q is not a remote id", id)
	}
	return nil
}

func LoadLocal(dataDir, id string) (remotedef.Definition, []byte, error) {
	if err := validRemoteID(id); err != nil {
		return remotedef.Definition{}, nil, err
	}
	dir := filepath.Join(dataDir, RemotesDir, id)
	def, err := os.ReadFile(filepath.Join(dir, DefinitionFile))
	if err != nil {
		return remotedef.Definition{}, nil, err
	}
	entries := []filebundle.Entry{{Path: DefinitionFile, Content: string(def)}}
	var rest []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel = filepath.ToSlash(rel); rel != DefinitionFile && !config.LocalOnlyPath(rel) {
			rest = append(rest, rel)
		}
		return nil
	})
	if err != nil {
		return remotedef.Definition{}, nil, err
	}
	slices.Sort(rest)
	for _, rel := range rest {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return remotedef.Definition{}, nil, err
		}
		entries = append(entries, filebundle.Entry{Path: rel, Content: string(b)})
	}
	bundle, err := filebundle.Encode(entries)
	if err != nil {
		return remotedef.Definition{}, nil, fmt.Errorf("remote %s: %w", id, err)
	}
	d, _, err := ReadBundle(bundle)
	if err != nil {
		return remotedef.Definition{}, nil, fmt.Errorf("remote %s: %w", id, err)
	}
	return d, bundle, nil
}

func SubstrateBundle(tree, dataDir string) ([]byte, error) {
	rel, err := filepath.Rel(tree, dataDir)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)
	files, err := gitfacts.Repo{Dir: tree}.TrackedFiles(rel)
	if err != nil {
		return nil, err
	}
	var entries []filebundle.Entry
	for _, f := range files {
		p, ok := strings.CutPrefix(f, rel+"/")
		if !ok || config.LocalOnlyPath(p) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(f)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, filebundle.Entry{Path: p, Content: string(b)})
	}
	return filebundle.Encode(entries)
}

type LocalRun struct {
	NodeID   string        `json:"node_id"`
	Run      RunDescriptor `json:"run"`
	Steps    int           `json:"steps,omitempty"`
	Checkout string        `json:"checkout,omitempty"`
	Layout   int           `json:"layout,omitempty"`
}

type LocalOutcome struct {
	Verdict string             `json:"verdict"`
	Notes   string             `json:"notes"`
	Steps   []kernel.DriveStep `json:"steps,omitempty"`
}

type LocalRuns struct{ Dir string }

func (r LocalRuns) path(remote, itemID string) string {
	return filepath.Join(r.Dir, remote, itemID+".json")
}

func (r LocalRuns) check(remote, itemID string) error {
	if err := validRemoteID(remote); err != nil {
		return err
	}
	if !ledger.ValidItem(itemID) {
		return fmt.Errorf("%q is not an item id", itemID)
	}
	return nil
}

func (r LocalRuns) detachedPath(remote, itemID string) string {
	return filepath.Join(r.Dir, remote, itemID+".detached.json")
}

func (r LocalRuns) Load(remote, itemID string) (*satellepb.ItemRun, error) {
	if err := r.check(remote, itemID); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(r.path(remote, itemID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("no run of %s for %s here: %w", remote, itemID, err)
		}
		return nil, err
	}
	run := &satellepb.ItemRun{}
	if err := protojson.Unmarshal(b, run); err != nil {
		return nil, fmt.Errorf("the run of %s for %s: %w", remote, itemID, err)
	}
	return run, nil
}

func (r LocalRuns) Save(remote, itemID string, run *satellepb.ItemRun) error {
	if err := r.check(remote, itemID); err != nil {
		return err
	}
	b, err := protojson.MarshalOptions{Multiline: true}.Marshal(run)
	if err != nil {
		return err
	}
	return writeRunFile(r.path(remote, itemID), b)
}

type LocalDetached struct {
	Chains      []string  `json:"chains"`
	Remove      bool      `json:"remove,omitempty"`
	Egress      bool      `json:"egress,omitempty"`
	SecretNames []string  `json:"secret_names,omitempty"`
	Answered    time.Time `json:"answered,omitzero"`
}

func (r LocalRuns) SaveDetached(remote, itemID string, d LocalDetached) error {
	if err := r.check(remote, itemID); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return writeRunFile(r.detachedPath(remote, itemID), b)
}

func (r LocalRuns) LoadDetached(remote, itemID string) (LocalDetached, error) {
	var d LocalDetached
	if err := r.check(remote, itemID); err != nil {
		return d, err
	}
	b, err := os.ReadFile(r.detachedPath(remote, itemID))
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, fmt.Errorf("the detached run of %s for %s: %w", remote, itemID, err)
	}
	return d, nil
}

func (r LocalRuns) DropDetached(remote, itemID string) error {
	if err := r.check(remote, itemID); err != nil {
		return err
	}
	if err := os.Remove(r.detachedPath(remote, itemID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func writeRunFile(p string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

type FireOptions struct {
	Kernel                *kernel.Kernel
	Tree, DataDir         string
	Runs                  LocalRuns
	Remote                string
	Definition            remotedef.Definition
	Bundle                []byte
	Item                  string
	Steps                 int
	Detach                bool
	Actor                 string
	Docker                provider.DockerLocal
	Out                   io.Writer
	Secrets               Secrets
	Binary, BinaryVersion string
}

type Secrets struct {
	Names  []string
	Values map[string]string
	Redact []string
}

func TakeSecrets(def remotedef.Definition, lookup func(string) (string, bool), dataDir string) (Secrets, error) {
	if err := localRefusal(def); err != nil {
		return Secrets{}, err
	}
	names := secretNames(def)
	values, missing, err := collectSecrets(names, nil, lookup, dataDir)
	if err != nil || len(names) == 0 {
		return Secrets{}, err
	}
	switch len(missing) {
	case 0:
	case 1:
		return Secrets{}, fmt.Errorf("secret %s is not set in the firing shell's environment or in %s", missing[0], secretsFileName)
	default:
		return Secrets{}, fmt.Errorf("secrets %s are not set in the firing shell's environment or in %s", strings.Join(missing, ", "), secretsFileName)
	}
	return Secrets{Names: names, Values: values, Redact: longestFirst(values)}, nil
}

func TakeHeldSecrets(def remotedef.Definition, recorded []string, lookup func(string) (string, bool), dataDir string) (Secrets, error) {
	values, missing, err := collectSecrets(recorded, secretNames(def), lookup, dataDir)
	if err != nil {
		return Secrets{}, err
	}
	if len(missing) > 0 {
		return Secrets{}, fmt.Errorf("secret %s was handed to the node and is not set in the shell or %s: it is needed to redact what the node sends back and to check its record before import; set it and run the command again", missing[0], secretsFileName)
	}
	if len(recorded) == 0 {
		return Secrets{}, nil
	}
	return Secrets{Names: slices.Clone(recorded), Values: values, Redact: longestFirst(values)}, nil
}

func collectSecrets(take, unset []string, lookup func(string) (string, bool), dataDir string) (values map[string]string, missing []string, err error) {
	shell := map[string]string{}
	for _, n := range take {
		if v, ok := lookup(n); ok && v != "" {
			shell[n] = v
		}
	}
	for _, n := range append(slices.Clone(take), unset...) {
		if err := os.Unsetenv(n); err != nil {
			return nil, nil, fmt.Errorf("secret %s: %w", n, err)
		}
	}
	if len(take) == 0 {
		return nil, nil, nil
	}
	file, err := fileSecrets(dataDir)
	if err != nil {
		return nil, nil, err
	}
	values = map[string]string{}
	for _, n := range take {
		switch {
		case shell[n] != "":
			values[n] = shell[n]
		case file[n] != "":
			values[n] = file[n]
		default:
			missing = append(missing, n)
		}
	}
	return values, missing, nil
}

func (s Secrets) clean(text string) string { return s.cleanN(text, 2000) }

func (s Secrets) cleanN(text string, n int) string { return tailOf(redact(text, s.Redact), n) }

func heldRefusal(recorded []string, s Secrets) error {
	for _, n := range recorded {
		if _, ok := s.Values[n]; !ok {
			return fmt.Errorf("secret %s: secrets were not taken before the bring-back", n)
		}
	}
	return nil
}

func secretNames(def remotedef.Definition) []string {
	var names []string
	for _, e := range def.SecretEntries() {
		names = append(names, e.Key)
	}
	return names
}

func longestFirst(values map[string]string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return len(b) - len(a) })
	return out
}

func FireLocal(ctx context.Context, o FireOptions) (*satellepb.ItemRun, error) {
	def, k := o.Definition, o.Kernel
	if o.Out == nil {
		o.Out = io.Discard
	}
	if err := localRefusal(def); err != nil {
		return nil, fmt.Errorf("remote %s: %w", o.Remote, err)
	}
	if o.Detach {
		if err := detachRefusal(def); err != nil {
			return nil, fmt.Errorf("remote %s: %w", o.Remote, err)
		}
	}
	for _, n := range secretNames(def) {
		if _, ok := o.Secrets.Values[n]; !ok || !slices.Contains(o.Secrets.Names, n) {
			return nil, fmt.Errorf("remote %s: secret %s: secrets were not taken before the fire", o.Remote, n)
		}
	}
	command := def.Provider == remotedef.ProviderCommand
	if !command {
		if err := egressRefusal(ctx, o.Docker, def); err != nil {
			return nil, fmt.Errorf("remote %s: %w", o.Remote, err)
		}
	}
	if _, err := k.Get(o.Item); err != nil {
		return nil, err
	}
	chains := []string{o.Item}
	if def.RunsTask() {
		if o.Item != def.Drive.Task {
			return nil, fmt.Errorf("remote %s: [drive] task is %s; fire it with satelle remote fire %s %s", o.Remote, def.Drive.Task, o.Remote, def.Drive.Task)
		}
		inputs, err := k.TaskInputs(o.Item)
		if err != nil {
			return nil, fmt.Errorf("remote %s: %w", o.Remote, err)
		}
		chains = append(chains, inputs...)
	}
	if err := recoverStale(ctx, o); err != nil {
		return nil, err
	}
	if !command {
		if err := ensureNodeImage(ctx, o.Docker, o.Tree, def, o.Out); err != nil {
			return nil, err
		}
	}
	give := command || def.Machine.Image != ""
	cannotGive := func(reason string) error {
		if command {
			return fmt.Errorf("remote %s: the node cannot be given satelle: %s", o.Remote, reason)
		}
		return fmt.Errorf("remote %s: image %s cannot be given satelle: %s", o.Remote, def.Machine.Image, reason)
	}
	var arch string
	if give {
		a, err := linuxArch(o.Binary)
		if err != nil {
			return nil, cannotGive(err.Error())
		}
		arch = a
	}
	node, err := newNodeID()
	if err != nil {
		return nil, err
	}
	if command {
		if err := (provider.Command{}).Check(ctx, def, node); err != nil {
			return nil, fmt.Errorf("remote %s: %w", o.Remote, err)
		}
	}
	run := &satellepb.ItemRun{State: RunRunning, NodeId: node}
	if err := o.Runs.Save(o.Remote, o.Item, run); err != nil {
		return nil, err
	}
	stopped := func(err error) (*satellepb.ItemRun, error) {
		if len(o.Secrets.Redact) > 0 {
			err = errors.New(redact(err.Error(), o.Secrets.Redact))
		}
		run.State, run.Reason = RunStopped, tailOf(err.Error(), 2000)
		if serr := o.Runs.Save(o.Remote, o.Item, run); serr != nil {
			err = errors.Join(err, serr)
		}
		return run, err
	}
	var h provider.Handle
	if command {
		h, err = provider.Command{}.Start(ctx, def, provider.Run{NodeID: node, NoSetup: true})
		fmt.Fprintf(o.Out, "node %s started by [connect] start\n", node)
		fmt.Fprintf(o.Out, "stop it: %s\n", provider.RemoveHint(h))
		if err != nil {
			return stopped(errors.Join(err, h.Stop(context.WithoutCancel(ctx))))
		}
	} else {
		h, err = o.Docker.Start(ctx, def, provider.Run{NodeID: node, NoSetup: true})
		if h != nil {
			fmt.Fprintf(o.Out, "node %s in container %s\n", node, h.ID())
			fmt.Fprintf(o.Out, "inspect it: docker exec -it %s sh\n", h.ID())
		}
		if err != nil {
			return stopped(err)
		}
	}
	secret := len(secretNames(def)) > 0
	remove := def.Lifecycle.Scope != remotedef.ScopeJob
	torn, tornOK, handed := false, false, false
	tear := func() error {
		torn = true
		if err := h.Stop(context.WithoutCancel(ctx)); err != nil {
			err = errors.New(redact(err.Error(), o.Secrets.Redact))
			if command {
				fmt.Fprintf(o.Out, "stopping the node %s failed: %v; stop it with %s\n", node, err, provider.RemoveHint(h))
			} else {
				fmt.Fprintf(o.Out, "removing the container %s failed: %v; remove it with %s\n", h.ID(), err, provider.RemoveHint(h))
			}
			return err
		}
		tornOK = true
		if command {
			fmt.Fprintf(o.Out, "the node %s is stopped\n", node)
		} else {
			fmt.Fprintf(o.Out, "the container %s is removed\n", h.ID())
		}
		return nil
	}
	defer func() {
		if secret && !torn && !handed {
			_ = tear()
		}
	}()
	if give {
		fmt.Fprintf(o.Out, "copying satelle into the node at %s\n", NodeSatellePath)
		if err := giveSatelle(ctx, h, o.Binary, o.BinaryVersion, arch); err != nil {
			return stopped(errors.Join(cannotGive(err.Error()), tear()))
		}
	}
	seated := false
	release := func() error {
		if !seated {
			return nil
		}
		seated = false
		return k.Release(o.Item, node, o.Actor)
	}
	defer func() { _ = release() }()
	failAfterStart := func(err error) (*satellepb.ItemRun, error) {
		if rerr := release(); rerr != nil {
			err = errors.Join(err, rerr)
		}
		switch {
		case (command && remove) || secret:
			err = errors.Join(err, tear())
		case command:
			fmt.Fprintf(o.Out, "the node %s is left; stop it with %s\n", node, provider.RemoveHint(h))
		default:
			fmt.Fprintf(o.Out, "the container %s is left to inspect; remove it with %s\n", h.ID(), provider.RemoveHint(h))
		}
		return stopped(err)
	}
	if !def.RunsTask() {
		if _, err := k.Seat(o.Item, node, kernel.LocationRemote+node, "", o.Actor); err != nil {
			return failAfterStart(err)
		}
		seated = true
	}

	if o.Detach {
		if err := startDetached(ctx, o, h, node, chains, remove); err != nil {
			run, err := failAfterStart(err)
			if !torn || tornOK {
				err = errors.Join(err, o.Runs.DropDetached(o.Remote, o.Item))
			}
			return run, err
		}
		handed, seated = true, false
		fmt.Fprintf(o.Out, "node %s is running on %s; bring it back with satelle remote status %s %s\n", node, o.Item, o.Remote, o.Item)
		return run, nil
	}

	out, err := runNode(ctx, o, h, node, chains)
	if err != nil {
		return failAfterStart(err)
	}
	if err := release(); err != nil {
		return failAfterStart(err)
	}
	run.State, run.Verdict, run.Notes = RunConcluded, out.Verdict, out.Notes
	if err := o.Runs.Save(o.Remote, o.Item, run); err != nil {
		return run, err
	}
	switch {
	case remove:
		if err := tear(); err != nil {
			return run, err
		}
	case command:
		fmt.Fprintf(o.Out, "the node %s is left running; stop it with %s\n", node, provider.RemoveHint(h))
	default:
		fmt.Fprintf(o.Out, "the container %s is left running; remove it with %s\n", h.ID(), provider.RemoveHint(h))
	}
	return run, nil
}

func RunsHere(def remotedef.Definition) bool {
	return def.Provider == remotedef.ProviderDockerLocal || def.Provider == remotedef.ProviderCommand
}

func localRefusal(def remotedef.Definition) error {
	switch {
	case !RunsHere(def):
		return fmt.Errorf("provider %s does not run locally", def.Provider)
	case len(def.Secrets.Override) > 0:
		return errors.New("[secrets.override] is not served to a local node: a local node reads each [secrets] name from the firing shell or the repo's .satelle/.env")
	case len(def.Repos) > 0:
		return errors.New("[[repo]] is not fetched by a local node")
	}
	return nil
}

func detachRefusal(def remotedef.Definition) error {
	switch {
	case !RunsHere(def):
		return errors.New("--detach applies to a remote that runs here (docker-local or command)")
	case def.Provider == remotedef.ProviderDockerLocal && len(secretNames(def)) > 0:
		return errors.New("--detach cannot hand a docker-local node its secrets: a docker-local detached start has no stdin; fire it attached")
	}
	return nil
}

func egressRefusal(ctx context.Context, d provider.DockerLocal, def remotedef.Definition) error {
	if len(def.Network.Allow) == 0 {
		return nil
	}
	refuse := func(format string, a ...any) error {
		return fmt.Errorf("[network] allow cannot be enforced here: "+format, a...)
	}
	if d.Egress == "" {
		return refuse("no satelle binary to serve it")
	}
	arch, err := linuxArch(d.Egress)
	if err != nil {
		return refuse("%v", err)
	}
	raw, err := d.Architecture(ctx)
	if err != nil {
		return refuse("docker info: %v", err)
	}
	host, ok := nodeArch(raw)
	if !ok {
		return refuse("the docker host reports architecture %s, which satelle does not build", raw)
	}
	if host != arch {
		return refuse("the docker host is linux/%s; the firing satelle is linux/%s: no egress proxy for it", host, arch)
	}
	return nil
}

func gateNodeRefusal(def remotedef.Definition) error {
	switch {
	case len(def.SecretEntries()) > 0:
		return errors.New("[secrets] is not served to a local node: nothing here hands out secrets")
	case len(def.Repos) > 0:
		return errors.New("[[repo]] is not fetched by a local node")
	}
	return nil
}

func recoverStale(ctx context.Context, o FireOptions) error {
	prev, err := o.Runs.Load(o.Remote, o.Item)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	node := prev.GetNodeId()
	if o.Definition.Provider == remotedef.ProviderCommand {
		_, err := o.Runs.LoadDetached(o.Remote, o.Item)
		switch {
		case err == nil && RunFinished(prev.GetState()):
			return fmt.Errorf("the run of %s on %s is %s but its node %s was not stopped; stop it with satelle remote stop %s %s",
				o.Remote, o.Item, prev.GetState(), node, o.Remote, o.Item)
		case err == nil:
			return fmt.Errorf("a run of %s on %s is running detached on node %s; bring it back with satelle remote status %s %s or end it with satelle remote stop %s %s",
				o.Remote, o.Item, node, o.Remote, o.Item, o.Remote, o.Item)
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	if RunFinished(prev.GetState()) {
		return nil
	}
	reason, left := "the node's container is gone", "its container is gone"
	if o.Definition.Provider == remotedef.ProviderCommand {
		reason, left = "the run was left running; its node was stopped", "its node was stopped"
		if node != "" {
			h := provider.Command{}.Node(o.Definition, node)
			if err := h.Stop(ctx); err != nil {
				fmt.Fprintf(o.Out, "stopping the node %s the run left behind: %v\n", node, err)
			}
		}
	} else if node != "" && o.Docker.Exists(ctx, provider.ContainerName(node)) {
		return fmt.Errorf("a run of %s on %s is running on node %s (container %s)", o.Remote, o.Item, node, provider.ContainerName(node))
	}
	if err := stopLeftRun(o.Kernel, o.Runs, o.Remote, o.Item, prev, reason, left, o.Actor, o.Out); err != nil {
		return err
	}
	return o.Runs.DropDetached(o.Remote, o.Item)
}

func stopLeftRun(k *kernel.Kernel, runs LocalRuns, remote, itemID string, run *satellepb.ItemRun, reason, left, actor string, out io.Writer) error {
	run.State, run.Reason = RunStopped, reason
	if err := runs.Save(remote, itemID, run); err != nil {
		return err
	}
	node := run.GetNodeId()
	held, err := releaseNodeSeat(k, itemID, node, actor)
	if held {
		fmt.Fprintf(out, "the run on node %s was left behind: %s; releasing its seat\n", node, left)
	}
	return err
}

func releaseNodeSeat(k *kernel.Kernel, itemID, node, actor string) (bool, error) {
	if node == "" {
		return false, nil
	}
	events, err := k.Ledger.Read(itemID)
	if err != nil {
		return false, err
	}
	for _, s := range item.FoldSeats(events) {
		if s.Session == node {
			return true, k.Release(itemID, node, actor)
		}
	}
	return false, nil
}

func giveWork(ctx context.Context, o FireOptions, h provider.Handle, node string, chains []string) error {
	tmp, err := os.MkdirTemp("", "satelle-fire-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	in := filepath.Join(tmp, "in")
	commit, shallow, err := stageIn(o, in, node, chains)
	if err != nil {
		return err
	}
	if _, err := h.Exec(ctx, "mkdir", "-p", LocalNodeDir); err != nil {
		return err
	}
	if err := h.CopyIn(ctx, in, path.Join(LocalNodeDir, "in")); err != nil {
		return err
	}
	if commit != "" {
		fmt.Fprintf(o.Out, "checking out %s in the node at %s\n", commit, path.Join(LocalNodeDir, "work"))
		if b, _, err := checkoutHead(ctx, h, LocalNodeDir, "HEAD", commit, shallow); err != nil {
			return fmt.Errorf("%w\n%s", err, tailOf(string(b), 2000))
		}
	}
	return nil
}

func runNode(ctx context.Context, o FireOptions, h provider.Handle, node string, chains []string) (LocalOutcome, error) {
	if err := giveWork(ctx, o, h, node, chains); err != nil {
		return LocalOutcome{}, err
	}
	sx, ok := h.(provider.StdinExec)
	if !ok {
		return LocalOutcome{}, fmt.Errorf("the node's handle %s cannot pass stdin to the node", h.ID())
	}
	payload, err := stdinPayload(o.Secrets)
	if err != nil {
		return LocalOutcome{}, err
	}
	fmt.Fprintf(o.Out, "running the node on %s\n", o.Item)
	raw, execErr := sx.ExecStdin(ctx, bytes.NewReader(payload), "satelle", "node", "run-local", "--dir", LocalNodeDir)
	log := redact(string(raw), o.Secrets.Redact)
	if execErr != nil {
		execErr = errors.New(redact(execErr.Error(), o.Secrets.Redact))
	}
	return bringBack(ctx, o.Kernel, h, chains, node, o.Secrets, execErr, log)
}

func bringBack(ctx context.Context, k *kernel.Kernel, h provider.Handle, chains []string, node string, secrets Secrets, execErr error, log string) (LocalOutcome, error) {
	tmp, err := os.MkdirTemp("", "satelle-back-")
	if err != nil {
		return LocalOutcome{}, err
	}
	defer os.RemoveAll(tmp)
	back := filepath.Join(tmp, "back")
	if err := os.MkdirAll(back, 0o755); err != nil {
		return LocalOutcome{}, err
	}
	outcomeFile := filepath.Join(back, localOutcome)
	if err := h.CopyOut(ctx, path.Join(LocalNodeDir, "out", localOutcome), outcomeFile); err != nil {
		if execErr != nil {
			return LocalOutcome{}, fmt.Errorf("the node failed: %w\n%s", execErr, tailOf(log, 2000))
		}
		return LocalOutcome{}, fmt.Errorf("the node wrote no outcome: %w\n%s", err, tailOf(log, 2000))
	}
	var out LocalOutcome
	b, err := readRegular(outcomeFile)
	if err != nil {
		return LocalOutcome{}, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return LocalOutcome{}, fmt.Errorf("the node's outcome: %w", err)
	}
	out.Notes = redact(out.Notes, secrets.Redact)
	runtime := path.Join(LocalNodeDir, "runtime")
	for _, name := range []string{"ledger", "blobs"} {
		dst := filepath.Join(back, name)
		if err := h.CopyOut(ctx, path.Join(runtime, name), dst); err != nil {
			return LocalOutcome{}, err
		}
		if err := regularTree(dst); err != nil {
			return LocalOutcome{}, err
		}
	}
	res, err := importBack(k, back, chains, node, secrets.Values)
	if broken := (*ledger.BrokenLink)(nil); errors.As(err, &broken) {
		return LocalOutcome{Verdict: kernel.VerdictReject,
			Notes: fmt.Sprintf("the node's chain of %s does not verify: %s; nothing of the run was imported", broken.Item, broken.Reason)}, nil
	}
	if leak := (*SecretInRecord)(nil); errors.As(err, &leak) {
		return LocalOutcome{Verdict: kernel.VerdictReject, Notes: leak.Error() + "; nothing of the run was imported"}, nil
	}
	if err != nil {
		return LocalOutcome{}, err
	}
	return concludeImport(out, res), nil
}

func readRegular(p string) ([]byte, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s from the node is not a regular file", filepath.Base(p))
	}
	return os.ReadFile(p)
}

func regularTree(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s from the node is not a directory", filepath.Base(dir))
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("%s from the node is neither a file nor a directory", filepath.Base(p))
		}
		return nil
	})
}

const (
	detachedLog      = "node.log"
	detachedExit     = "exit"
	detachedProgress = "progress"
)

func startDetached(ctx context.Context, o FireOptions, h provider.Handle, node string, chains []string, remove bool) error {
	if err := giveWork(ctx, o, h, node, chains); err != nil {
		return err
	}
	outDir := path.Join(LocalNodeDir, "out")
	if _, err := h.Exec(ctx, "mkdir", "-p", outDir); err != nil {
		return err
	}
	side := LocalDetached{Chains: chains, Remove: remove, Egress: len(o.Definition.Network.Allow) > 0,
		SecretNames: o.Secrets.Names, Answered: time.Now().UTC()}
	if o.Definition.Provider == remotedef.ProviderCommand {
		return startCommandDetached(ctx, o, h, side)
	}
	dx, ok := h.(provider.DetachedExec)
	if !ok {
		return fmt.Errorf("the node's handle %s cannot run detached", h.ID())
	}
	script := fmt.Sprintf("satelle node run-local --dir %s --progress </dev/null >%s 2>&1; echo $? >%s",
		LocalNodeDir, path.Join(outDir, detachedLog), path.Join(outDir, detachedExit))
	fmt.Fprintf(o.Out, "running the node on %s, detached\n", o.Item)
	if err := dx.ExecDetached(ctx, "sh", "-c", script); err != nil {
		return err
	}
	return o.Runs.SaveDetached(o.Remote, o.Item, side)
}

func startCommandDetached(ctx context.Context, o FireOptions, h provider.Handle, side LocalDetached) error {
	sx, ok := h.(provider.StdinExec)
	if !ok {
		return fmt.Errorf("the node's handle %s cannot pass stdin to the node", h.ID())
	}
	payload, err := stdinPayload(o.Secrets)
	if err != nil {
		return err
	}
	if err := o.Runs.SaveDetached(o.Remote, o.Item, side); err != nil {
		return err
	}
	argv := []string{"satelle", "node", "run-local", "--dir", LocalNodeDir, "--detach"}
	if len(secretNames(o.Definition)) == 0 {
		argv = append(argv, "--progress")
	}
	fmt.Fprintf(o.Out, "running the node on %s, detached\n", o.Item)
	raw, err := sx.ExecStdin(ctx, bytes.NewReader(payload), argv...)
	if said := strings.TrimSpace(redact(string(raw), o.Secrets.Redact)); said != "" {
		fmt.Fprintln(o.Out, said)
	}
	if err != nil {
		return errors.New(redact(err.Error(), o.Secrets.Redact))
	}
	return nil
}

func stdinPayload(s Secrets) ([]byte, error) {
	values := s.Values
	if values == nil {
		values = map[string]string{}
	}
	return json.Marshal(values)
}

type StartDetachedOptions struct {
	Dir, Self string
	Progress  bool
	Stdin     io.Reader
	Out       io.Writer
}

type BringBackOptions struct {
	Kernel     *kernel.Kernel
	Runs       LocalRuns
	Remote     string
	Item       string
	Definition remotedef.Definition
	Docker     provider.DockerLocal
	Actor      string
	Out        io.Writer
	Bound      time.Duration
	Secrets    Secrets
}

const AnswerBound = 15 * time.Second

func BringBackLocal(ctx context.Context, o BringBackOptions) (*satellepb.ItemRun, error) {
	if err := o.Runs.check(o.Remote, o.Item); err != nil {
		return nil, err
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	run, err := o.Runs.Load(o.Remote, o.Item)
	if err != nil {
		return nil, err
	}
	sidecar, err := o.Runs.LoadDetached(o.Remote, o.Item)
	if errors.Is(err, fs.ErrNotExist) {
		return run, nil
	}
	if err != nil {
		return nil, err
	}
	if err := heldRefusal(sidecar.SecretNames, o.Secrets); err != nil {
		return nil, err
	}
	command := o.Definition.Provider == remotedef.ProviderCommand
	secret := len(sidecar.SecretNames) > 0
	mustStop := secret || (command && sidecar.Remove)
	node := run.GetNodeId()
	k := o.Kernel
	if RunFinished(run.GetState()) {
		if !command || node == "" {
			return run, nil
		}
		h := detachedHandle(o, node, sidecar)
		if err := endNode(ctx, o, h); err != nil {
			return run, err
		}
		return run, o.Runs.DropDetached(o.Remote, o.Item)
	}
	stop := func(reason, left string) (*satellepb.ItemRun, error) {
		err := stopLeftRun(k, o.Runs, o.Remote, o.Item, run, o.Secrets.clean(reason), o.Secrets.clean(left), o.Actor, o.Out)
		return run, errors.Join(err, o.Runs.DropDetached(o.Remote, o.Item))
	}
	if node == "" {
		return stop("the node's container is gone", "its container is gone")
	}
	bound := o.Bound
	if bound <= 0 {
		bound = AnswerBound
	}
	probe, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	lost := func() bool { return errors.Is(probe.Err(), context.DeadlineExceeded) && ctx.Err() == nil }
	lostRun := func() (*satellepb.ItemRun, error) {
		l := proto.Clone(run).(*satellepb.ItemRun)
		l.State = RunLost
		l.Reason = fmt.Sprintf("the node did not answer within %s; ", bound)
		if sidecar.Answered.IsZero() {
			l.Reason += "no answer is recorded"
		} else {
			l.Reason += "it last answered at " + sidecar.Answered.Format(time.RFC3339)
		}
		return l, nil
	}
	if !command && !o.Docker.Exists(probe, provider.ContainerName(node)) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if lost() {
			return lostRun()
		}
		return stop("the node's container is gone", "its container is gone")
	}
	h := detachedHandle(o, node, sidecar)
	listing, err := h.Exec(probe, "ls", "-A", path.Join(LocalNodeDir, "out"))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch {
		case command && secret:
			if serr := endNode(ctx, o, h); serr != nil {
				return nil, serr
			}
			return stop(fmt.Sprintf("the node did not answer within %s (%s); [connect] stop ran", bound, firstLine(err.Error())), "it did not answer")
		case lost() || command:
			return lostRun()
		}
		return nil, fmt.Errorf("reading what node %s left: %w", node, err)
	}
	names := strings.Fields(string(listing))
	if !slices.Contains(names, detachedExit) {
		var progress string
		if !secret && slices.Contains(names, detachedProgress) {
			b, err := h.Exec(probe, "cat", path.Join(LocalNodeDir, "out", detachedProgress))
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if lost() {
					return lostRun()
				}
			} else {
				progress = firstLine(strings.TrimSpace(tailOf(string(b), 2000)))
			}
		}
		sidecar.Answered = time.Now().UTC()
		if err := o.Runs.SaveDetached(o.Remote, o.Item, sidecar); err != nil {
			return nil, err
		}
		working := proto.Clone(run).(*satellepb.ItemRun)
		working.State = RunRunning
		if progress != "" {
			working.Notes = "progress: " + progress
		}
		return working, nil
	}
	end := func(reason string) (*satellepb.ItemRun, error) {
		switch {
		case mustStop:
			if err := endNode(ctx, o, h); err != nil {
				return nil, errors.Join(errors.New(o.Secrets.clean(reason)), err)
			}
		case command:
			fmt.Fprintf(o.Out, "the node %s is left; stop it with %s\n", node, provider.RemoveHint(h))
		default:
			fmt.Fprintf(o.Out, "the container %s is left to inspect; remove it with %s\n", h.ID(), provider.RemoveHint(h))
		}
		return stop(reason, "it stopped: "+firstLine(reason))
	}
	if !slices.Contains(names, localOutcome) {
		code, log, err := detachedFailure(ctx, h)
		if err != nil {
			return end(err.Error())
		}
		return end(fmt.Sprintf("the node failed: exit %d\n%s", code, o.Secrets.cleanN(log, 1900)))
	}
	out, err := bringBack(ctx, k, h, sidecar.Chains, node, o.Secrets, nil, "")
	if err != nil {
		return end(err.Error())
	}
	if _, err := releaseNodeSeat(k, o.Item, node, o.Actor); err != nil {
		return end(err.Error())
	}
	run.State, run.Verdict, run.Notes = RunConcluded, out.Verdict, out.Notes
	if err := o.Runs.Save(o.Remote, o.Item, run); err != nil {
		return run, err
	}
	switch {
	case mustStop:
		if err := endNode(ctx, o, h); err != nil {
			return run, err
		}
		return run, o.Runs.DropDetached(o.Remote, o.Item)
	case command:
		fmt.Fprintf(o.Out, "the node %s is left running; stop it with %s\n", node, provider.RemoveHint(h))
		return run, o.Runs.DropDetached(o.Remote, o.Item)
	}
	if err := o.Runs.DropDetached(o.Remote, o.Item); err != nil {
		return run, err
	}
	if !sidecar.Remove {
		fmt.Fprintf(o.Out, "the container %s is left running; remove it with %s\n", h.ID(), provider.RemoveHint(h))
		return run, nil
	}
	if err := h.Stop(context.WithoutCancel(ctx)); err != nil {
		fmt.Fprintf(o.Out, "removing the container %s failed: %v; remove it with %s\n", h.ID(), err, provider.RemoveHint(h))
		return run, err
	}
	fmt.Fprintf(o.Out, "the container %s is removed\n", h.ID())
	return run, nil
}

func detachedHandle(o BringBackOptions, node string, sidecar LocalDetached) provider.Handle {
	if o.Definition.Provider == remotedef.ProviderCommand {
		return provider.Command{}.Node(o.Definition, node)
	}
	return o.Docker.Node(o.Definition, node, sidecar.Egress)
}

func endNode(ctx context.Context, o BringBackOptions, h provider.Handle) error {
	if err := h.Stop(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("stopping the node %s failed: %s; stop it with %s", h.ID(), o.Secrets.clean(err.Error()), provider.RemoveHint(h))
	}
	fmt.Fprintf(o.Out, "the node %s is stopped\n", h.ID())
	return nil
}

func StopLocal(ctx context.Context, o BringBackOptions) (*satellepb.ItemRun, error) {
	if err := o.Runs.check(o.Remote, o.Item); err != nil {
		return nil, err
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	run, err := o.Runs.Load(o.Remote, o.Item)
	if err != nil {
		return nil, err
	}
	finished := RunFinished(run.GetState())
	sidecar, err := o.Runs.LoadDetached(o.Remote, o.Item)
	switch {
	case errors.Is(err, fs.ErrNotExist) && finished:
		return nil, fmt.Errorf("the run of %s on %s is already %s; nothing to stop", o.Remote, o.Item, run.GetState())
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("the run of %s on %s is not detached; an attached fire is stopped by interrupting it", o.Remote, o.Item)
	case err != nil:
		return nil, err
	}
	if err := heldRefusal(sidecar.SecretNames, o.Secrets); err != nil {
		return nil, err
	}
	command := o.Definition.Provider == remotedef.ProviderCommand
	if finished && !command {
		return nil, fmt.Errorf("the run of %s on %s is already %s; nothing to stop", o.Remote, o.Item, run.GetState())
	}
	node := run.GetNodeId()
	if command {
		if node != "" {
			if err := endNode(ctx, o, detachedHandle(o, node, sidecar)); err != nil {
				return nil, err
			}
		}
		if finished {
			return run, o.Runs.DropDetached(o.Remote, o.Item)
		}
	} else {
		removed := false
		if node != "" && o.Docker.Exists(ctx, provider.ContainerName(node)) {
			h := o.Docker.Node(o.Definition, node, sidecar.Egress)
			if err := h.Stop(context.WithoutCancel(ctx)); err != nil {
				return nil, fmt.Errorf("removing the container %s failed: %w; remove it with %s", h.ID(), err, provider.RemoveHint(h))
			}
			removed = true
			fmt.Fprintf(o.Out, "the container %s is removed\n", h.ID())
		} else if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !removed {
			fmt.Fprintln(o.Out, "the node's container was already gone")
		}
	}
	reason := fmt.Sprintf("stopped by %s with satelle remote stop", o.Actor)
	if err := stopLeftRun(o.Kernel, o.Runs, o.Remote, o.Item, run, reason, "it was stopped by "+o.Actor, o.Actor, o.Out); err != nil {
		return run, err
	}
	return run, o.Runs.DropDetached(o.Remote, o.Item)
}

var errNoExitStatus = errors.New("the node failed: its exit file does not hold an exit status")

func detachedFailure(ctx context.Context, h provider.Handle) (code int, log string, err error) {
	tmp, err := os.MkdirTemp("", "satelle-back-")
	if err != nil {
		return 0, "", err
	}
	defer os.RemoveAll(tmp)
	read := func(name string) (string, error) {
		f := filepath.Join(tmp, name)
		if err := h.CopyOut(ctx, path.Join(LocalNodeDir, "out", name), f); err != nil {
			return "", err
		}
		b, err := readRegular(f)
		return string(b), err
	}
	raw, err := read(detachedExit)
	if err != nil {
		return 0, "", err
	}
	if code, err = strconv.Atoi(strings.TrimSpace(raw)); err != nil {
		return 0, "", errNoExitStatus
	}
	if log, err = read(detachedLog); err != nil {
		log = fmt.Sprintf("the node's log could not be read: %v", err)
	}
	return code, log, nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

type imported struct {
	Added  int
	Forked []ForkedChain
}

type ForkedChain struct {
	Item       string
	Seq        int
	Here, Node string
}

func (f ForkedChain) Line() string {
	return fmt.Sprintf("diverged: %s at event %d: this machine holds %s, the node %s; not imported",
		f.Item, f.Seq, ledger.ShortHash(f.Here), ledger.ShortHash(f.Node))
}

func concludeImport(out LocalOutcome, res imported) LocalOutcome {
	out.Notes = strings.TrimRight(out.Notes, "\n") + fmt.Sprintf("\nimported %d event(s)", res.Added)
	for _, f := range res.Forked {
		out.Notes += "\n" + f.Line()
	}
	if len(res.Forked) > 0 {
		out.Verdict = kernel.VerdictReject
	}
	return out
}

func stageIn(o FireOptions, in, node string, chains []string) (string, bool, error) {
	if err := os.MkdirAll(filepath.Join(in, "ledger"), 0o755); err != nil {
		return "", false, err
	}
	mode := ModeItem
	if o.Definition.RunsTask() {
		mode = ModeTask
	}
	lr := LocalRun{NodeID: node, Run: RunDescriptor{Mode: mode, ItemID: o.Item}, Steps: o.Steps, Layout: NodeLayout}
	files := map[string][]byte{localDefinition: o.Bundle}
	var shallow bool
	if o.Definition.Drive.Checkout {
		commit, err := gitfacts.Repo{Dir: o.Tree}.Head()
		if err != nil {
			return "", false, fmt.Errorf("the firing tree's HEAD: %w", err)
		}
		if shallow, err = stageHead(o.Tree, in, "HEAD"); err != nil {
			return "", false, err
		}
		lr.Checkout = commit
	} else {
		sub, err := SubstrateBundle(o.Tree, o.DataDir)
		if err != nil {
			return "", false, err
		}
		files[localSubstrate] = sub
	}
	runJSON, err := json.Marshal(lr)
	if err != nil {
		return "", false, err
	}
	files[localRunFile] = runJSON
	for _, id := range chains {
		chain, err := os.ReadFile(o.Kernel.Ledger.Path(id))
		if err != nil {
			return "", false, err
		}
		files[filepath.Join("ledger", id+".jsonl")] = chain
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(in, name), b, 0o644); err != nil {
			return "", false, err
		}
	}
	return lr.Checkout, shallow, nil
}

func importBack(k *kernel.Kernel, dir string, sent []string, node string, values map[string]string) (imported, error) {
	var res imported
	st, err := ledger.Open(dir)
	if err != nil {
		return res, err
	}
	ids, err := st.Items()
	if err != nil {
		return res, err
	}
	actor := "node:" + node
	var chains [][]ledger.Event
	for _, id := range ids {
		events, err := st.Read(id)
		if err != nil {
			return res, err
		}
		if len(events) == 0 || !slices.Contains(sent, id) && events[0].Actor != actor {
			continue
		}
		if err := ledger.VerifyChain(id, events); err != nil {
			return res, err
		}
		chains = append(chains, events)
	}
	if err := secretInRecord(k, chains, filepath.Join(dir, "blobs"), values); err != nil {
		return res, err
	}
	if err := copyBlobs(k.Ledger, filepath.Join(dir, "blobs")); err != nil {
		return res, err
	}
	for _, events := range chains {
		added, err := k.Ledger.Import(events)
		res.Added += added
		if d := (*ledger.Diverges)(nil); errors.As(err, &d) {
			held, rerr := k.Ledger.Read(d.Item)
			if rerr != nil {
				return res, rerr
			}
			res.Forked = append(res.Forked, ForkedChain{Item: d.Item, Seq: d.Seq,
				Here: held[len(held)-1].Hash, Node: events[len(events)-1].Hash})
			continue
		}
		if err != nil {
			return res, err
		}
	}
	return res, k.Cache.Sync(k.Ledger)
}

type SecretInRecord struct{ Name string }

func (e *SecretInRecord) Error() string {
	return fmt.Sprintf("the node's record carries the value of declared secret %s", e.Name)
}

func secretInRecord(k *kernel.Kernel, chains [][]ledger.Event, blobs string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	names := slices.Sorted(maps.Keys(values))
	hit := func(s string) string {
		for _, n := range names {
			if v := values[n]; v != "" && strings.Contains(s, v) {
				return n
			}
		}
		return ""
	}
	for _, events := range chains {
		held, err := k.Ledger.Read(events[0].Item)
		if err != nil {
			return err
		}
		p, err := ledger.SharedPrefix(held, len(events), func(i int) (string, error) { return events[i].Hash, nil })
		if err != nil {
			return err
		}
		for _, e := range events[p:] {
			b, err := json.Marshal(e)
			if err != nil {
				return err
			}
			if n := walkJSON(b, hit); n != "" {
				return &SecretInRecord{Name: n}
			}
		}
	}
	entries, err := os.ReadDir(blobs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := k.Ledger.GetBlob(e.Name()); err == nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(blobs, e.Name()))
		if err != nil {
			return err
		}
		if n := hit(string(b)); n != "" {
			return &SecretInRecord{Name: n}
		}
		if n := walkJSON(b, hit); n != "" {
			return &SecretInRecord{Name: n}
		}
	}
	return nil
}

func walkJSON(b []byte, hit func(string) string) string {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	return walkValue(v, hit)
}

func walkValue(v any, hit func(string) string) string {
	switch t := v.(type) {
	case map[string]any:
		for key, child := range t {
			if n := walkString(key, hit); n != "" {
				return n
			}
			if n := walkValue(child, hit); n != "" {
				return n
			}
		}
	case []any:
		for _, child := range t {
			if n := walkValue(child, hit); n != "" {
				return n
			}
		}
	case string:
		return walkString(t, hit)
	}
	return ""
}

func walkString(s string, hit func(string) string) string {
	if n := hit(s); n != "" {
		return n
	}
	if t := strings.TrimSpace(s); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") || strings.HasPrefix(t, "\"") {
		return walkJSON([]byte(t), hit)
	}
	return ""
}

func copyBlobs(dst *ledger.Store, blobs string) error {
	src, err := ledger.Open(filepath.Dir(blobs))
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(blobs)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := src.GetBlob(e.Name())
		if err != nil {
			return err
		}
		if _, err := dst.PutBlob(b); err != nil {
			return err
		}
	}
	return nil
}

func newNodeID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "nod_" + hex.EncodeToString(b[:]), nil
}

func tailOf(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func ensureNodeImage(ctx context.Context, d provider.DockerLocal, tree string, def remotedef.Definition, out io.Writer) error {
	if def.Machine.Image != "" {
		return nil
	}
	fmt.Fprintf(out, "building %s from %s\n", provider.NodeImage, filepath.Join(tree, "Dockerfile"))
	return d.BuildNodeImage(ctx, tree)
}
