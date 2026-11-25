package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/remote/provider"
)

type gateHelloReply struct {
	Root             string `json:"root"`
	DefinitionSHA256 string `json:"definition_sha256"`
}

type gatePutRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Data   []byte `json:"data,omitempty"`
	Mode   uint32 `json:"mode,omitempty"`
}

type gateGetRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
}

type gateGetReply struct {
	Data []byte `json:"data,omitempty"`
	EOF  bool   `json:"eof"`
}

type gateExecRequest struct {
	Argv      []string `json:"argv"`
	TimeoutMS int64    `json:"timeout_ms"`
}

type gateExecReply struct {
	Output []byte `json:"output,omitempty"`
	Failed string `json:"failed,omitempty"`
}

func serveGateJobs(node *Node, dir, digest string) error {
	root, err := filepath.Abs(filepath.Join(dir, "gate"))
	if err != nil {
		return err
	}
	node.Handle(GateHelloJob, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
		return json.Marshal(gateHelloReply{Root: root, DefinitionSHA256: digest})
	})
	node.Handle(GatePutJob, func(_ context.Context, body json.RawMessage) (json.RawMessage, error) {
		var req gatePutRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		p, err := underRoot(root, req.Path)
		if err != nil {
			return nil, err
		}
		if err := putChunk(p, req); err != nil {
			return nil, err
		}
		return json.Marshal(struct{}{})
	})
	node.Handle(GateGetJob, func(_ context.Context, body json.RawMessage) (json.RawMessage, error) {
		var req gateGetRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		p, err := underRoot(root, req.Path)
		if err != nil {
			return nil, err
		}
		rep, err := getChunk(p, req.Offset)
		if err != nil {
			return nil, err
		}
		return json.Marshal(rep)
	})
	node.Handle(GateExecJob, func(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
		var req gateExecRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, err
		}
		if len(req.Argv) == 0 {
			return nil, errors.New("gate.exec: no argv")
		}
		if req.TimeoutMS <= 0 {
			return nil, errors.New("gate.exec: no time left to run in")
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
		quoted := make([]string, len(req.Argv))
		for i, a := range req.Argv {
			quoted[i] = ShQuote(a)
		}
		out := &boundedTail{max: gateExecTail}
		failed := runSetupCommand(ctx, root, strings.Join(quoted, " "), gateExecEnv(os.Environ()), time.Duration(req.TimeoutMS)*time.Millisecond, out)
		return json.Marshal(gateExecReply{Output: out.b, Failed: failed})
	})
	return nil
}

func gateExecEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if k == provider.EnvNodeID || k == provider.EnvNodeToken || k == provider.EnvServer || k == config.NodeRunEnv || k == config.NodeWithholdEnv {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func underRoot(root, p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("gate path %q is not absolute", p)
	}
	clean := filepath.Clean(p)
	rel, err := filepath.Rel(root, clean)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("gate path %q is not under the gate root %s", p, root)
	}
	return clean, nil
}

func putChunk(p string, req gatePutRequest) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if req.Offset != 0 {
		st, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("gate.put %s at offset %d: %w", p, req.Offset, err)
		}
		if st.Size() != req.Offset {
			return fmt.Errorf("gate.put %s at offset %d: the file holds %d bytes", p, req.Offset, st.Size())
		}
		flags = os.O_WRONLY | os.O_APPEND
	}
	mode := os.FileMode(req.Mode).Perm()
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(p, flags, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(req.Data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(p, mode)
}

func getChunk(p string, offset int64) (gateGetReply, error) {
	f, err := os.Open(p)
	if err != nil {
		return gateGetReply{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return gateGetReply{}, err
	}
	if st.IsDir() {
		return gateGetReply{}, fmt.Errorf("gate.get %s: a directory", p)
	}
	if offset < 0 || offset > st.Size() {
		return gateGetReply{}, fmt.Errorf("gate.get %s at offset %d: the file holds %d bytes", p, offset, st.Size())
	}
	buf := make([]byte, min(int64(gateChunkBytes), st.Size()-offset))
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return gateGetReply{}, err
	}
	return gateGetReply{Data: buf[:n], EOF: offset+int64(n) >= st.Size()}, nil
}

type boundedTail struct {
	max int
	b   []byte
}

func (t *boundedTail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}
