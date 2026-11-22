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
	Kernel                *kernel.Kernel
	Tree, DataDir         string
	Runs                  LocalRuns
	Remote                string
	Definition            remotedef.Definition
	Bundle                []byte
	Item                  string
	Steps                 int
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

func TakeSecrets(def remotedef.Definition, lookup func(string) (string, bool)) (Secrets, error) {
	if err := localRefusal(def); err != nil {
		return Secrets{}, err
	}
	names := secretNames(def)
	values := map[string]string{}
	var missing []string
	for _, n := range names {
		v, ok := lookup(n)
		if !ok || v == "" {
			missing = append(missing, n)
			continue
		}
		values[n] = v
	}
	switch len(missing) {
	case 0:
	case 1:
		return Secrets{}, fmt.Errorf("secret %s is not set in the firing shell's environment", missing[0])
	default:
		return Secrets{}, fmt.Errorf("secrets %s are not set in the firing shell's environment", strings.Join(missing, ", "))
	}
	for _, n := range names {
		if err := os.Unsetenv(n); err != nil {
			return Secrets{}, fmt.Errorf("secret %s: %w", n, err)
		}
	}
	return Secrets{Names: names, Values: values, Redact: longestFirst(values)}, nil
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
	for _, n := range secretNames(def) {
		if _, ok := o.Secrets.Values[n]; !ok || !slices.Contains(o.Secrets.Names, n) {
			return nil, fmt.Errorf("remote %s: secret %s: secrets were not taken before the fire", o.Remote, n)
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
	if err := ensureNodeImage(ctx, o.Docker, o.Tree, def, o.Out); err != nil {
		return nil, err
	}
	plain := def.Machine.Image != ""
	cannotGive := func(reason string) error {
		return fmt.Errorf("remote %s: image %s cannot be given satelle: %s", o.Remote, def.Machine.Image, reason)
	}
	var arch string
	if plain {
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
	h, err := o.Docker.Start(ctx, def, provider.Run{NodeID: node, NoSetup: true})
	if h != nil {
		fmt.Fprintf(o.Out, "node %s in container %s\n", node, h.ID())
		fmt.Fprintf(o.Out, "inspect it: docker exec -it %s sh\n", h.ID())
	}
	if err != nil {
		return stopped(err)
	}
	if plain {
		fmt.Fprintf(o.Out, "copying satelle into the node at %s\n", NodeSatellePath)
		if err := giveSatelle(ctx, h, o.Binary, o.BinaryVersion, arch); err != nil {
			serr := h.Stop(context.WithoutCancel(ctx))
			fmt.Fprintf(o.Out, "the container %s is removed\n", h.ID())
			return stopped(errors.Join(cannotGive(err.Error()), serr))
		}
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
	switch {
	case def.Provider != remotedef.ProviderDockerLocal:
		return fmt.Errorf("provider %s does not run locally", def.Provider)
	case len(def.Secrets.Override) > 0:
		return errors.New("[secrets.override] is not served to a local node: a docker-local node reads each [secrets] name from the firing shell")
	case len(def.SecretEntries()) > 0 && len(def.Network.Allow) > 0:
		return errors.New("a docker-local definition that declares [secrets] may allow no host: the [network] allow-list is not enforced on docker")
	case len(def.Repos) > 0:
		return errors.New("[[repo]] is not fetched by a local node")
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
	sx, ok := h.(provider.StdinExec)
	if !ok {
		return LocalOutcome{}, fmt.Errorf("the node's handle %s cannot pass stdin to the node", h.ID())
	}
	values := o.Secrets.Values
	if values == nil {
		values = map[string]string{}
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return LocalOutcome{}, err
	}
	fmt.Fprintf(o.Out, "running the node on %s\n", o.Item)
	raw, execErr := sx.ExecStdin(ctx, bytes.NewReader(payload), "satelle", "node", "run-local", "--dir", LocalNodeDir)
	log := redact(string(raw), o.Secrets.Redact)
	if execErr != nil {
		execErr = errors.New(redact(execErr.Error(), o.Secrets.Redact))
	}

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
	b, err := os.ReadFile(outcomeFile)
	if err != nil {
		return LocalOutcome{}, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return LocalOutcome{}, fmt.Errorf("the node's outcome: %w", err)
	}
	out.Notes = redact(out.Notes, o.Secrets.Redact)
	runtime := path.Join(LocalNodeDir, "runtime")
	if err := h.CopyOut(ctx, path.Join(runtime, "ledger"), filepath.Join(back, "ledger")); err != nil {
		return LocalOutcome{}, err
	}
	if err := h.CopyOut(ctx, path.Join(runtime, "blobs"), filepath.Join(back, "blobs")); err != nil {
		return LocalOutcome{}, err
	}
	res, err := importBack(k, back, chains, node, o.Secrets.Values)
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
