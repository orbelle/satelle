package remotenode

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

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

const (
	RunRunning   = "running"
	RunConcluded = "concluded"
	RunStopped   = "stopped"
)

func RunFinished(state string) bool { return state == RunConcluded || state == RunStopped }

func LoadLocal(dataDir, id string) (remotedef.Definition, []byte, error) {
	if err := filebundle.CheckPath(id, nil); err != nil || strings.Contains(id, "/") {
		return remotedef.Definition{}, nil, fmt.Errorf("%q is not a remote id", id)
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
	NodeID string        `json:"node_id"`
	Run    RunDescriptor `json:"run"`
	Steps  int           `json:"steps,omitempty"`
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

func (r LocalRuns) Load(remote, itemID string) (*satellepb.ItemRun, error) {
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
	p := r.path(remote, itemID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := protojson.MarshalOptions{Multiline: true}.Marshal(run)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

type FireOptions struct {
	Kernel        *kernel.Kernel
	Tree, DataDir string
	Runs          LocalRuns
	Remote        string
	Definition    remotedef.Definition
	Bundle        []byte
	Item          string
	Steps         int
	Actor         string
	Docker        provider.DockerLocal
	Out           io.Writer
}

func FireLocal(ctx context.Context, o FireOptions) (*satellepb.ItemRun, error) {
	def, k := o.Definition, o.Kernel
	if o.Out == nil {
		o.Out = io.Discard
	}
	if err := localRefusal(def); err != nil {
		return nil, fmt.Errorf("remote %s: %w", o.Remote, err)
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
	if err := ensureNodeImage(ctx, o.Docker, o.Tree, def, o.Out); err != nil {
		return nil, err
	}
	node, err := newNodeID()
	if err != nil {
		return nil, err
	}
	run := &satellepb.ItemRun{State: RunRunning, NodeId: node}
	if err := o.Runs.Save(o.Remote, o.Item, run); err != nil {
		return nil, err
	}
	stopped := func(err error) (*satellepb.ItemRun, error) {
		run.State, run.Reason = RunStopped, tailOf(err.Error(), 2000)
		if serr := o.Runs.Save(o.Remote, o.Item, run); serr != nil {
			err = errors.Join(err, serr)
		}
		return run, err
	}
	h, err := o.Docker.Start(ctx, def, provider.Run{NodeID: node})
	if h != nil {
		fmt.Fprintf(o.Out, "node %s in container %s\n", node, h.ID())
		fmt.Fprintf(o.Out, "inspect it: docker exec -it %s sh\n", h.ID())
	}
	if err != nil {
		return stopped(err)
	}
	seated := !def.RunsTask()
	if seated {
		if _, err := k.Seat(o.Item, node, kernel.LocationRemote+node, "", o.Actor); err != nil {
			return stopped(err)
		}
	}
	release := func() error {
		if !seated {
			return nil
		}
		seated = false
		return k.Release(o.Item, node, o.Actor)
	}
	defer func() { _ = release() }()

	out, err := runNode(ctx, o, h, node, chains)
	if err != nil {
		if rerr := release(); rerr != nil {
			err = errors.Join(err, rerr)
		}
		fmt.Fprintf(o.Out, "the container %s is left to inspect; remove it with docker rm -f %s\n", h.ID(), h.ID())
		return stopped(err)
	}
	if err := release(); err != nil {
		return stopped(err)
	}
	run.State, run.Verdict, run.Notes = RunConcluded, out.Verdict, out.Notes
	if err := o.Runs.Save(o.Remote, o.Item, run); err != nil {
		return run, err
	}
	if out.Verdict == kernel.VerdictAccept && def.Lifecycle.Scope != remotedef.ScopeJob {
		if err := h.Stop(context.WithoutCancel(ctx)); err != nil {
			return run, err
		}
	} else {
		fmt.Fprintf(o.Out, "the container %s is left running; remove it with docker rm -f %s\n", h.ID(), h.ID())
	}
	return run, nil
}

func localRefusal(def remotedef.Definition) error {
	if def.Provider != remotedef.ProviderDockerLocal {
		return fmt.Errorf("provider %s does not run locally", def.Provider)
	}
	return gateNodeRefusal(def)
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
	if RunFinished(prev.GetState()) {
		return nil
	}
	node := prev.GetNodeId()
	if node != "" && o.Docker.Exists(ctx, provider.ContainerName(node)) {
		return fmt.Errorf("a run of %s on %s is running on node %s (container %s)", o.Remote, o.Item, node, provider.ContainerName(node))
	}
	prev.State, prev.Reason = RunStopped, "the node's container is gone"
	if err := o.Runs.Save(o.Remote, o.Item, prev); err != nil {
		return err
	}
	if node == "" {
		return nil
	}
	events, err := o.Kernel.Ledger.Read(o.Item)
	if err != nil {
		return err
	}
	for _, s := range item.FoldSeats(events) {
		if s.Session == node {
			fmt.Fprintf(o.Out, "the run on node %s was left behind: its container is gone; releasing its seat\n", node)
			return o.Kernel.Release(o.Item, node, o.Actor)
		}
	}
	return nil
}

func runNode(ctx context.Context, o FireOptions, h provider.Handle, node string, chains []string) (LocalOutcome, error) {
	k := o.Kernel
	tmp, err := os.MkdirTemp("", "satelle-fire-")
	if err != nil {
		return LocalOutcome{}, err
	}
	defer os.RemoveAll(tmp)
	in := filepath.Join(tmp, "in")
	if err := stageIn(o, in, node, chains); err != nil {
		return LocalOutcome{}, err
	}
	if _, err := h.Exec(ctx, "mkdir", "-p", LocalNodeDir); err != nil {
		return LocalOutcome{}, err
	}
	if err := h.CopyIn(ctx, in, path.Join(LocalNodeDir, "in")); err != nil {
		return LocalOutcome{}, err
	}
	fmt.Fprintf(o.Out, "running the node on %s\n", o.Item)
	log, execErr := h.Exec(ctx, "satelle", "node", "run-local", "--dir", LocalNodeDir)

	back := filepath.Join(tmp, "back")
	if err := os.MkdirAll(back, 0o755); err != nil {
		return LocalOutcome{}, err
	}
	outcomeFile := filepath.Join(back, localOutcome)
	if err := h.CopyOut(ctx, path.Join(LocalNodeDir, "out", localOutcome), outcomeFile); err != nil {
		if execErr != nil {
			return LocalOutcome{}, fmt.Errorf("the node failed: %w", execErr)
		}
		return LocalOutcome{}, fmt.Errorf("the node wrote no outcome: %w\n%s", err, tailOf(string(log), 2000))
	}
	var out LocalOutcome
	b, err := os.ReadFile(outcomeFile)
	if err != nil {
		return LocalOutcome{}, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return LocalOutcome{}, fmt.Errorf("the node's outcome: %w", err)
	}
	runtime := path.Join(LocalNodeDir, "runtime")
	if err := h.CopyOut(ctx, path.Join(runtime, "ledger"), filepath.Join(back, "ledger")); err != nil {
		return LocalOutcome{}, err
	}
	if err := h.CopyOut(ctx, path.Join(runtime, "blobs"), filepath.Join(back, "blobs")); err != nil {
		return LocalOutcome{}, err
	}
	added, err := importBack(k, back, chains, node)
	if broken := (*ledger.BrokenLink)(nil); errors.As(err, &broken) {
		return LocalOutcome{Verdict: kernel.VerdictReject,
			Notes: fmt.Sprintf("the node's chain of %s does not verify: %s; nothing of the run was imported", broken.Item, broken.Reason)}, nil
	}
	if err != nil {
		return LocalOutcome{}, err
	}
	out.Notes = strings.TrimRight(out.Notes, "\n") + fmt.Sprintf("\nimported %d event(s)", added)
	return out, nil
}

func stageIn(o FireOptions, in, node string, chains []string) error {
	if err := os.MkdirAll(filepath.Join(in, "ledger"), 0o755); err != nil {
		return err
	}
	sub, err := SubstrateBundle(o.Tree, o.DataDir)
	if err != nil {
		return err
	}
	mode := ModeItem
	if o.Definition.RunsTask() {
		mode = ModeTask
	}
	runJSON, err := json.Marshal(LocalRun{NodeID: node, Run: RunDescriptor{Mode: mode, ItemID: o.Item}, Steps: o.Steps})
	if err != nil {
		return err
	}
	files := map[string][]byte{localDefinition: o.Bundle, localSubstrate: sub, localRunFile: runJSON}
	for _, id := range chains {
		chain, err := os.ReadFile(o.Kernel.Ledger.Path(id))
		if err != nil {
			return err
		}
		files[filepath.Join("ledger", id+".jsonl")] = chain
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(in, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func importBack(k *kernel.Kernel, dir string, sent []string, node string) (int, error) {
	st, err := ledger.Open(dir)
	if err != nil {
		return 0, err
	}
	ids, err := st.Items()
	if err != nil {
		return 0, err
	}
	actor := "node:" + node
	var chains [][]ledger.Event
	for _, id := range ids {
		events, err := st.Read(id)
		if err != nil {
			return 0, err
		}
		if len(events) == 0 || !slices.Contains(sent, id) && events[0].Actor != actor {
			continue
		}
		if err := ledger.VerifyChain(id, events); err != nil {
			return 0, err
		}
		chains = append(chains, events)
	}
	if err := copyBlobs(k.Ledger, filepath.Join(dir, "blobs")); err != nil {
		return 0, err
	}
	total := 0
	for _, events := range chains {
		added, err := k.Ledger.Import(events)
		total += added
		if err != nil {
			return total, fmt.Errorf("%s changed here while the node ran: %w", events[0].Item, err)
		}
	}
	return total, k.Cache.Sync(k.Ledger)
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
