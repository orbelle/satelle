//go:build !windows

package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/kernel"
)

func readStagedRun(t *testing.T, nodeDir string) LocalRun {
	t.Helper()
	var run LocalRun
	b, err := os.ReadFile(filepath.Join(nodeDir, "in", localRunFile))
	if err != nil || json.Unmarshal(b, &run) != nil {
		t.Fatalf("staged run.json = %s, %v", b, err)
	}
	return run
}

func reloadDefinition(t *testing.T, f *taskFixture) {
	t.Helper()
	d, bundle, err := LoadLocal(f.def.DataDir, "tprobe")
	if err != nil {
		t.Fatal(err)
	}
	f.def.Definition, f.def.Bundle = d, bundle
}

func TestStageInCarriesTheStagedSubstrateDigest(t *testing.T) {
	f := setupFixture(t, "echo ok", nil)
	nodeDir := f.stage(t, "nod_e1")
	run := readStagedRun(t, nodeDir)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(run.SubstrateDigest) {
		t.Fatalf("substrate_digest = %q, want a SHA-256", run.SubstrateDigest)
	}
	in := filepath.Join(nodeDir, "in")
	definition, err := os.ReadFile(filepath.Join(in, localDefinition))
	if err != nil {
		t.Fatal(err)
	}
	_, overlay, err := ReadBundle(definition)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := unpackSubstrate(in, root, overlay); err != nil {
		t.Fatal(err)
	}
	if want, err := kernel.SubstrateDigest(root); err != nil || want != run.SubstrateDigest {
		t.Fatalf("unpacked digest %s (%v), run file %s", want, err, run.SubstrateDigest)
	}

	writeFiles(t, f.def.DataDir, map[string]string{"remotes/tprobe/skills/overlaid.md": "first\n"})
	reloadDefinition(t, &f)
	overlaid := readStagedRun(t, f.stage(t, "nod_e2")).SubstrateDigest
	if overlaid == run.SubstrateDigest {
		t.Fatal("an added overlay file left the digest unchanged")
	}
	writeFiles(t, f.def.DataDir, map[string]string{"remotes/tprobe/skills/overlaid.md": "second\n"})
	reloadDefinition(t, &f)
	changed := readStagedRun(t, f.stage(t, "nod_e3")).SubstrateDigest
	if changed == overlaid {
		t.Fatal("a changed overlay file left the digest unchanged")
	}
	commitTree(t, f.tree, map[string]string{"skills/extra.md": "---\ntype: check\n---\n```sh\necho extra\n```\n"})
	if tracked := readStagedRun(t, f.stage(t, "nod_e4")).SubstrateDigest; tracked == changed {
		t.Fatal("a changed tracked substrate file left the digest unchanged")
	}
}

func TestRunLocalRunsAStagedSubstrateThatMatches(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker")
	f := setupFixture(t, "echo ok", []string{"touch " + marker})
	nodeDir := f.stage(t, "nod_e5")
	run := readStagedRun(t, nodeDir)
	var log strings.Builder
	out, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0", Log: &log})
	if err != nil || out.Verdict != kernel.VerdictAccept {
		t.Fatalf("RunLocal = %+v, %v\n%s", out, err, log.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("setup did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nodeDir, "out", localOutcome)); err != nil {
		t.Fatalf("no outcome: %v", err)
	}
	runs := stagedGateRuns(t, nodeDir, "nod_e5")
	if len(runs) == 0 {
		t.Fatal("no gate.run recorded")
	}
	for _, s := range runs {
		if s.Digest != run.SubstrateDigest {
			t.Fatalf("gate.run digest %s, run file %s", s.Digest, run.SubstrateDigest)
		}
	}
}

func TestRunLocalRefusesASubstrateThatDoesNotMatch(t *testing.T) {
	for _, c := range []struct {
		name string
		node string
		edit func(t *testing.T, nodeDir string)
	}{
		{"run file names another digest", "nod_e6", func(t *testing.T, nodeDir string) {
			rewriteRun(t, nodeDir, func(m map[string]any) { m["substrate_digest"] = strings.Repeat("0", 64) })
		}},
		{"substrate bundle altered after staging", "nod_e7", func(t *testing.T, nodeDir string) {
			p := filepath.Join(nodeDir, "in", localSubstrate)
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := filebundle.Decode(b, nil)
			if err != nil {
				t.Fatal(err)
			}
			entries = append(entries, filebundle.Entry{Path: "skills/planted.md", Content: "planted\n"})
			if b, err = filebundle.Encode(entries); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "marker")
			f := secretFixture(t, "echo checked", []string{"touch " + marker}, "")
			nodeDir := f.stage(t, c.node)
			c.edit(t, nodeDir)
			want := readStagedRun(t, nodeDir).SubstrateDigest
			_, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0",
				Secrets: secretPayload(t, map[string]string{probeSecret: "the-value"})})
			got, derr := kernel.SubstrateDigest(filepath.Join(nodeDir, nodeSubstrateDir))
			if derr != nil {
				t.Fatal(derr)
			}
			if got == want {
				t.Fatalf("the unpacked substrate hashes to the run file's digest %s", want)
			}
			if err == nil || !strings.Contains(err.Error(), "refused before setup") ||
				!strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), got) {
				t.Fatalf("RunLocal = %v, want a refusal naming %s and %s", err, got, want)
			}
			if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a refused run ran its setup: %v", err)
			}
			for _, p := range []string{filepath.Join("out", localOutcome), filepath.Join("runtime", "ledger")} {
				if _, err := os.Stat(filepath.Join(nodeDir, p)); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("a refused run made %s: %v", p, err)
				}
			}
		})
	}
}

func TestRunLocalRefusesARunFileWithNoSubstrateDigest(t *testing.T) {
	for _, c := range []struct {
		name string
		node string
		edit func(map[string]any)
	}{
		{"key missing", "nod_e8", func(m map[string]any) { delete(m, "substrate_digest") }},
		{"empty", "nod_e9", func(m map[string]any) { m["substrate_digest"] = "" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "marker")
			f := secretFixture(t, "echo checked", []string{"touch " + marker}, "")
			nodeDir := f.stage(t, c.node)
			rewriteRun(t, nodeDir, c.edit)
			_, err := RunLocal(context.Background(), LocalOptions{Dir: nodeDir, Version: "1.0.0",
				Secrets: secretPayload(t, map[string]string{probeSecret: "the-value"})})
			if err == nil || !strings.Contains(err.Error(), "carries no substrate digest; refused before setup") {
				t.Fatalf("RunLocal = %v", err)
			}
			for _, d := range []string{"substrate", "work", "runtime", "out"} {
				if _, err := os.Stat(filepath.Join(nodeDir, d)); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("a refused run made %s: %v", d, err)
				}
			}
			if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a refused run ran its setup: %v", err)
			}
		})
	}
}
