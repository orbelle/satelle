package fakeserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/bobmcallan/satelle/internal/wire/satellepb"
)

type PackFile struct {
	Path       string
	Body       []byte
	Executable bool
}

func PackBundle(name, version string, files []PackFile) (*satellepb.PackBundle, string) {
	type entry struct {
		path string
		sum  string
	}
	refs := make([]*satellepb.PackFileRef, len(files))
	pfiles := make([]*satellepb.PackFile, len(files))
	entries := make([]entry, len(files))
	for i, f := range files {
		raw := sha256.Sum256(f.Body)
		sum := hex.EncodeToString(raw[:])
		refs[i] = &satellepb.PackFileRef{
			Path:       f.Path,
			Sha256:     sum,
			Size:       int64(len(f.Body)),
			Executable: f.Executable,
		}
		pfiles[i] = &satellepb.PackFile{
			Path:   f.Path,
			Sha256: sum,
			Body:   slices.Clone(f.Body),
		}
		entries[i] = entry{path: f.Path, sum: sum}
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return strings.Compare(a.path, b.path)
	})
	var buf strings.Builder
	for _, e := range entries {
		buf.WriteString(e.sum)
		buf.WriteString("  ")
		buf.WriteString(e.path)
		buf.WriteByte('\n')
	}
	digestRaw := sha256.Sum256([]byte(buf.String()))
	digest := hex.EncodeToString(digestRaw[:])
	return &satellepb.PackBundle{
		Version: &satellepb.PackVersion{
			Manifest: &satellepb.PackManifest{
				Name:    name,
				Version: version,
				Files:   refs,
				Digest:  digest,
			},
		},
		Files: pfiles,
	}, digest
}

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
	s.packs[packKey{ref: ref, version: version}] = &seededPack{
		bundle:  proto.Clone(bundle).(*satellepb.PackBundle),
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
	return proto.Clone(entry.bundle).(*satellepb.PackBundle), nil
}
