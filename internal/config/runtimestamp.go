package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bobmcallan/satelle/internal/procgroup"
)

const (
	RuntimeStampName = "runtime.json"
	RuntimeMachine   = "machine"
	RuntimeNode      = "node"
	nodeRuntimeBase  = "runtime"
)

// RuntimeStamp records what a runtime is: the machine's, or one a node run
// created, with the substrate and tree that run was given, the substrate's
// digest and the node process that wrote it.
type RuntimeStamp struct {
	Kind      string         `json:"kind"`
	Node      string         `json:"node,omitempty"`
	Substrate string         `json:"substrate,omitempty"`
	Tree      string         `json:"tree,omitempty"`
	Digest    string         `json:"digest,omitempty"`
	Process   procgroup.Proc `json:"process,omitzero"`
}

func WriteRuntimeStamp(runtimeDir string, s RuntimeStamp) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(runtimeDir, ".runtime-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(runtimeDir, RuntimeStampName)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func ReadRuntimeStamp(runtimeDir string) (RuntimeStamp, error) {
	var s RuntimeStamp
	b, err := os.ReadFile(filepath.Join(runtimeDir, RuntimeStampName))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", filepath.Join(runtimeDir, RuntimeStampName), err)
	}
	return s, nil
}

// MarkMachineRuntime stamps runtimeDir as this machine's runtime, so it is
// never opened as a node's.
func MarkMachineRuntime(runtimeDir string) error {
	if !IsRuntimeKeyDir(filepath.Base(runtimeDir)) {
		return nil
	}
	if s, err := ReadRuntimeStamp(runtimeDir); err == nil && s.Kind == RuntimeMachine {
		return nil
	}
	return WriteRuntimeStamp(runtimeDir, RuntimeStamp{Kind: RuntimeMachine})
}

// CanonicalPath is p made absolute, with every symlink resolved, cleaned.
func CanonicalPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	r, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(r), nil
}

// NodeRuntime returns the stamp and canonical path of the node runtime under
// runDir, or refuses. It refuses unless runDir is absolute, its runtime entry
// is a real directory outside either machine home, that runtime holds no
// machine marker, its stamp is a well-formed node stamp written by an
// ancestor of this process, and nothing in it is a link or special file.
func NodeRuntime(runDir string) (RuntimeStamp, string, error) {
	refuse := func(path, why string) (RuntimeStamp, string, error) {
		return RuntimeStamp{}, "", fmt.Errorf("%s %s: %s is not a node runtime this command may open", NodeRunEnv, path, why)
	}
	if !filepath.IsAbs(runDir) {
		return refuse(runDir, "the path is not absolute")
	}
	entry := filepath.Join(runDir, nodeRuntimeBase)
	st, err := os.Lstat(entry)
	if err != nil {
		return refuse(entry, "it does not exist")
	}
	if st.Mode()&fs.ModeSymlink != 0 || !st.IsDir() {
		return refuse(entry, "it is not a real directory")
	}
	dir, err := CanonicalPath(runDir)
	if err != nil {
		return refuse(runDir, err.Error())
	}
	runtime := filepath.Join(dir, nodeRuntimeBase)
	if IsRuntimeKeyDir(filepath.Base(runtime)) {
		return refuse(runtime, "it is named like a machine runtime")
	}
	for _, home := range []string{GlobalDir(), defaultGlobalDir()} {
		if within(runtime, home) {
			return refuse(runtime, "it is under a machine home "+home)
		}
	}
	if _, err := os.Lstat(filepath.Join(runtime, RepoPathMarkerName)); err == nil {
		return refuse(runtime, "it holds the machine's "+RepoPathMarkerName)
	}
	s, err := ReadRuntimeStamp(runtime)
	if err != nil {
		return refuse(runtime, "it has no readable stamp")
	}
	if s.Kind != RuntimeNode {
		return refuse(runtime, fmt.Sprintf("its stamp says %q", s.Kind))
	}
	for name, p := range map[string]string{"substrate path": s.Substrate, "tree path": s.Tree} {
		if p == "" || !filepath.IsAbs(p) {
			return refuse(runtime, "its stamp's "+name+" is not an absolute path")
		}
		if c, err := CanonicalPath(p); err != nil || c != p {
			return refuse(runtime, "its stamp's "+name+" is not canonical")
		}
	}
	if s.Digest == "" {
		return refuse(runtime, "its stamp has no digest")
	}
	if s.Process.PID <= 0 || s.Process.Start == "" {
		return refuse(runtime, "its stamp names no process")
	}
	if !startedBy(s.Process) {
		return refuse(runtime, "this command was not started by the node run")
	}
	if err := plainTree(runtime); err != nil {
		return refuse(runtime, err.Error())
	}
	return s, runtime, nil
}

func startedBy(p procgroup.Proc) bool {
	for _, a := range procgroup.Ancestors(os.Getpid()) {
		if a.PID == p.PID && a.Start == p.Start {
			return true
		}
	}
	return false
}

func within(p, home string) bool {
	if strings.TrimSpace(home) == "" {
		return false
	}
	h, err := CanonicalPath(home)
	if err != nil {
		h, err = filepath.Abs(home)
		if err != nil {
			return false
		}
		h = filepath.Clean(h)
	}
	rel, err := filepath.Rel(h, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

var errNotPlain = errors.New("not a plain file")

// plainTree walks dir without following links and refuses any entry that is
// not a directory or a regular file, and any regular file with another link.
func plainTree(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		m := info.Mode()
		switch {
		case m.IsDir():
			return nil
		case m.IsRegular():
			if extraLinks(info) {
				return fmt.Errorf("%s has another hard link", p)
			}
			return nil
		}
		return fmt.Errorf("%s is %w (%s)", p, errNotPlain, m.Type())
	})
}
