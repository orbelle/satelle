package remotenode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sync"
	"time"

	"github.com/bobmcallan/satelle/internal/gate"
)

type NodePlacer interface {
	StartNode(ctx context.Context, remote, itemID, binding string) (string, error)
	Attach(ctx context.Context, node string) (*Run, error)
}

type ServerSprites struct {
	Placer func(ctx context.Context) (NodePlacer, func(), error)
}

func (ss ServerSprites) StartSprite(ctx context.Context, s SpriteStart) (SpriteNode, error) {
	if ss.Placer == nil {
		return SpriteNode{}, errors.New("a fly-sprite gate needs a bound project (satelle project bind)")
	}
	p, closeFn, err := ss.Placer(ctx)
	if err != nil {
		return SpriteNode{}, err
	}
	node, err := p.StartNode(ctx, s.Remote, s.Item, "")
	if err != nil {
		closeFn()
		return SpriteNode{}, err
	}
	run, err := p.Attach(ctx, node)
	if err != nil {
		closeFn()
		return SpriteNode{Node: node}, err
	}
	h := &chanHandle{run: run, node: node, close: closeFn}
	body, err := run.Request(ctx, GateHelloJob, struct{}{})
	if err != nil {
		_ = h.Stop(ctx)
		return SpriteNode{Node: node}, fmt.Errorf("node %s: %s: %w", node, GateHelloJob, err)
	}
	var hello gateHelloReply
	if err := json.Unmarshal(body, &hello); err != nil {
		_ = h.Stop(ctx)
		return SpriteNode{Node: node}, fmt.Errorf("node %s: malformed %s reply: %w", node, GateHelloJob, err)
	}
	if hello.DefinitionSHA256 != s.DefinitionSHA256 {
		_ = h.Stop(ctx)
		return SpriteNode{Node: node}, fmt.Errorf("node %s runs remote %s at definition sha256 %s, this repo's is %s: satelle-server serves the definition it holds; update it there",
			node, s.Remote, hello.DefinitionSHA256, s.DefinitionSHA256)
	}
	return SpriteNode{Node: node, Handle: h, Root: hello.Root}, nil
}

type chanHandle struct {
	run   *Run
	node  string
	close func()
	once  sync.Once
}

func (h *chanHandle) ID() string { return h.node }

func (h *chanHandle) Exec(ctx context.Context, argv ...string) ([]byte, error) {
	limit := gate.DefaultCheckTimeout
	if dl, ok := ctx.Deadline(); ok {
		limit = time.Until(dl)
	}
	if limit <= 0 {
		return nil, context.DeadlineExceeded
	}
	ms := limit.Milliseconds()
	if ms <= 0 {
		ms = 1
	}
	body, err := h.run.Request(ctx, GateExecJob, gateExecRequest{Argv: argv, TimeoutMS: ms})
	if err != nil {
		return nil, fmt.Errorf("node %s: %w", h.node, err)
	}
	var rep gateExecReply
	if err := json.Unmarshal(body, &rep); err != nil {
		return nil, fmt.Errorf("node %s: malformed %s reply: %w", h.node, GateExecJob, err)
	}
	if rep.Failed != "" {
		return rep.Output, fmt.Errorf("node %s: %s: %s", h.node, argv[0], rep.Failed)
	}
	return rep.Output, nil
}

func (h *chanHandle) CopyIn(ctx context.Context, host, machine string) error {
	st, err := os.Stat(host)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return h.put(ctx, host, machine, st.Mode())
	}
	return filepath.WalkDir(host, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(host, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return h.put(ctx, p, path.Join(machine, filepath.ToSlash(rel)), info.Mode())
	})
}

func (h *chanHandle) put(ctx context.Context, host, machine string, mode fs.FileMode) error {
	f, err := os.Open(host)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, gateChunkBytes)
	var off int64
	for {
		n, rerr := io.ReadFull(f, buf)
		if rerr != nil && !errors.Is(rerr, io.EOF) && !errors.Is(rerr, io.ErrUnexpectedEOF) {
			return rerr
		}
		if n > 0 || off == 0 {
			if _, err := h.run.Request(ctx, GatePutJob, gatePutRequest{Path: machine, Offset: off, Data: buf[:n], Mode: uint32(mode.Perm())}); err != nil {
				return fmt.Errorf("node %s: copying %s: %w", h.node, machine, err)
			}
			off += int64(n)
		}
		if rerr != nil {
			return nil
		}
	}
}

func (h *chanHandle) CopyOut(ctx context.Context, machine, host string) error {
	f, err := os.Create(host)
	if err != nil {
		return err
	}
	var off int64
	for {
		body, err := h.run.Request(ctx, GateGetJob, gateGetRequest{Path: machine, Offset: off})
		if err != nil {
			_ = f.Close()
			return fmt.Errorf("node %s: copying %s: %w", h.node, machine, err)
		}
		var rep gateGetReply
		if err := json.Unmarshal(body, &rep); err != nil {
			_ = f.Close()
			return fmt.Errorf("node %s: malformed %s reply: %w", h.node, GateGetJob, err)
		}
		if _, err := f.Write(rep.Data); err != nil {
			_ = f.Close()
			return err
		}
		off += int64(len(rep.Data))
		if rep.EOF {
			return f.Close()
		}
		if len(rep.Data) == 0 {
			_ = f.Close()
			return fmt.Errorf("node %s: copying %s: no data before its end", h.node, machine)
		}
	}
}

func (h *chanHandle) Logs(context.Context) ([]byte, error) { return nil, nil }

func (h *chanHandle) Stop(context.Context) error {
	var err error
	h.once.Do(func() {
		err = h.run.End()
		if h.close != nil {
			h.close()
		}
	})
	return err
}
