package remotenode

import (
	"fmt"

	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/remotedef"
)

// DefinitionFile is the definition's own file in the bundle. The node reads it
// (remotedef.Parse) and does not install it.
const DefinitionFile = "remote.toml"

// InstallDefinition reads the bundle's remote.toml and overlays every other
// file of the bundle onto dataDir, the node's .satelle directory, at the same
// relative path: a remote may carry its own workflows, skills and agents.toml
// over the repo's. Files the bundle does not carry are left as they are.
// Everything is checked before anything is written: a bundle of another
// format, a path that is not plain and relative, a missing remote.toml or one
// the node cannot read installs nothing.
func InstallDefinition(dataDir string, bundle []byte) (remotedef.Definition, error) {
	def, overlay, err := ReadBundle(bundle)
	if err != nil {
		return remotedef.Definition{}, err
	}
	return def, writeOverlay(dataDir, overlay)
}

// fileEntry is one overlay file of a bundle.
type fileEntry = filebundle.Entry

// ReadBundle decodes a definition bundle: its remote.toml, read and validated,
// and every other file as the overlay. Nothing is written.
func ReadBundle(bundle []byte) (remotedef.Definition, []fileEntry, error) {
	entries, err := filebundle.Decode(bundle, nil)
	if err != nil {
		return remotedef.Definition{}, nil, err
	}
	var def remotedef.Definition
	var haveDef bool
	var overlay []fileEntry
	for _, e := range entries {
		if e.Path == DefinitionFile {
			if def, err = remotedef.Parse([]byte(e.Content)); err != nil {
				return remotedef.Definition{}, nil, err
			}
			haveDef = true
			continue
		}
		overlay = append(overlay, e)
	}
	if !haveDef {
		return remotedef.Definition{}, nil, fmt.Errorf("the bundle has no %s", DefinitionFile)
	}
	return def, overlay, nil
}

// writeOverlay writes the overlay files into dataDir, leaving every other file.
func writeOverlay(dataDir string, overlay []fileEntry) error {
	if len(overlay) == 0 {
		return nil
	}
	return filebundle.Write(dataDir, overlay, true, nil)
}
