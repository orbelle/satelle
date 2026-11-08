//go:build !windows

package remotenode

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bobmcallan/satelle/internal/remote/provider"
)

// gateJobs is a node with the gate jobs, and a call that runs one handler.
func gateJobs(t *testing.T) (string, func(job string, req any) (json.RawMessage, error)) {
	t.Helper()
	node := NewNode()
	dir := t.TempDir()
	if err := serveGateJobs(node, dir, "d1g"); err != nil {
		t.Fatal(err)
	}
	call := func(job string, req any) (json.RawMessage, error) {
		h, ok := node.handler(job)
		if !ok {
			t.Fatalf("no handler for %s", job)
		}
		body, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		return h(testCtx(t), body)
	}
	body, err := call(GateHelloJob, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	var hello gateHelloReply
	if err := json.Unmarshal(body, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Root != filepath.Join(dir, "gate") || hello.DefinitionSHA256 != "d1g" {
		t.Fatalf("hello = %+v", hello)
	}
	return hello.Root, call
}

// gate.put writes only under the gate root, and only in order; gate.get
// returns a file larger than one chunk in chunks.
func TestGatePutAndGet(t *testing.T) {
	root, call := gateJobs(t)
	for _, p := range []string{"/etc/passwd", filepath.Join(root, "..", "x"), root, "in/x", filepath.Join(root, "in", "..", "..", "y")} {
		if _, err := call(GatePutJob, gatePutRequest{Path: p, Data: []byte("x")}); err == nil {
			t.Errorf("put %q was accepted", p)
		}
		if _, err := call(GateGetJob, gateGetRequest{Path: p}); err == nil {
			t.Errorf("get %q was accepted", p)
		}
	}
	file := filepath.Join(root, "in", "big")
	want := bytes.Repeat([]byte("0123456789abcdef"), gateChunkBytes/16*2+5)
	if _, err := call(GatePutJob, gatePutRequest{Path: file, Data: want[:gateChunkBytes], Mode: 0o600}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(GatePutJob, gatePutRequest{Path: file, Offset: 3, Data: []byte("x")}); err == nil || !strings.Contains(err.Error(), "holds") {
		t.Fatalf("an out-of-order put = %v", err)
	}
	if _, err := call(GatePutJob, gatePutRequest{Path: file, Offset: gateChunkBytes, Data: want[gateChunkBytes:], Mode: 0o600}); err != nil {
		t.Fatal(err)
	}
	var got []byte
	chunks := 0
	for {
		body, err := call(GateGetJob, gateGetRequest{Path: file, Offset: int64(len(got))})
		if err != nil {
			t.Fatal(err)
		}
		var rep gateGetReply
		if err := json.Unmarshal(body, &rep); err != nil {
			t.Fatal(err)
		}
		if len(rep.Data) > gateChunkBytes {
			t.Fatalf("a chunk of %d bytes", len(rep.Data))
		}
		got = append(got, rep.Data...)
		chunks++
		if rep.EOF {
			break
		}
	}
	if !bytes.Equal(got, want) || chunks != 3 {
		t.Fatalf("got %d bytes in %d chunks, want %d in 3", len(got), chunks, len(want))
	}
}

// gate.exec runs argv in the gate root and returns its output's tail and why
// it failed; it is bounded by timeout_ms and never sees the node's own
// credentials.
func TestGateExec(t *testing.T) {
	t.Setenv(provider.EnvNodeToken, "tok-secret")
	t.Setenv(provider.EnvNodeID, "nod_x")
	t.Setenv(provider.EnvServer, "https://srv")
	t.Setenv("KEEP_ME", "kept")
	root, call := gateJobs(t)
	exec := func(ms int64, argv ...string) gateExecReply {
		t.Helper()
		body, err := call(GateExecJob, gateExecRequest{Argv: argv, TimeoutMS: ms})
		if err != nil {
			t.Fatal(err)
		}
		var rep gateExecReply
		if err := json.Unmarshal(body, &rep); err != nil {
			t.Fatal(err)
		}
		return rep
	}
	rep := exec(10_000, "sh", "-c", `pwd; env; echo "it's"; exit 3`)
	out := string(rep.Output)
	if rep.Failed != "exit status 3" || !strings.Contains(out, root) || !strings.Contains(out, "it's") || !strings.Contains(out, "KEEP_ME=kept") {
		t.Fatalf("exec = %q %q", rep.Failed, out)
	}
	for _, k := range []string{provider.EnvNodeToken, provider.EnvNodeID, provider.EnvServer, "tok-secret"} {
		if strings.Contains(out, k) {
			t.Errorf("the check's environment carries %s", k)
		}
	}
	rep = exec(10_000, "sh", "-c", `head -c 300000 /dev/zero | tr '\0' a; echo END`)
	if rep.Failed != "" || len(rep.Output) != gateExecTail || !strings.HasSuffix(string(rep.Output), "aEND\n") {
		t.Fatalf("exec kept %d bytes, failed %q", len(rep.Output), rep.Failed)
	}
	start := time.Now()
	rep = exec(200, "sleep", "10")
	if !strings.HasPrefix(rep.Failed, "timed out") || time.Since(start) > 5*time.Second {
		t.Fatalf("exec past its bound = %q after %s", rep.Failed, time.Since(start))
	}
	if _, err := call(GateExecJob, gateExecRequest{Argv: []string{"true"}}); err == nil {
		t.Fatal("an exec with no time left ran")
	}
}
