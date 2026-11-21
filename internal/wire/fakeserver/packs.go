package fakeserver

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

type packKey struct {
	ref     string
	version string
}

type seededPack struct {
	bundle  *satellepb.PackBundle
	readers map[string]bool
}

func (s *Server) SeedPack(ref, version string, bundle *satellepb.PackBundle, readers ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.packs == nil {
		s.packs = map[packKey]*seededPack{}
	}
	var allowed map[string]bool
	if len(readers) > 0 {
		allowed = make(map[string]bool, len(readers))
		for _, tok := range readers {
			allowed[tok] = true
		}
	}
	var cloned *satellepb.PackBundle
	if bundle != nil {
		cloned = proto.Clone(bundle).(*satellepb.PackBundle)
	}
	s.packs[packKey{ref: ref, version: version}] = &seededPack{
		bundle:  cloned,
		readers: allowed,
	}
}

type packsSvc struct {
	satellepb.UnimplementedPackServiceServer
	s *Server
}

func (p packsSvc) FetchPack(ctx context.Context, r *satellepb.GetPackRequest) (*satellepb.PackBundle, error) {
	tok := bearer(ctx)
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	entry := p.s.packs[packKey{ref: r.GetRef(), version: r.GetVersion()}]
	if entry == nil {
		return nil, status.Errorf(codes.NotFound, "pack %s@%s not found", r.GetRef(), r.GetVersion())
	}
	if len(entry.readers) > 0 && !entry.readers[tok] {
		return nil, status.Errorf(codes.NotFound, "pack %s@%s not found", r.GetRef(), r.GetVersion())
	}
	if entry.bundle == nil {
		return &satellepb.PackBundle{}, nil
	}
	return proto.Clone(entry.bundle).(*satellepb.PackBundle), nil
}
