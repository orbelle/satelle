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
	entries, err := filebundle.Decode(bundle, nil)
	if err != nil {
		return remotedef.Definition{}, err
	}
	var def remotedef.Definition
	var haveDef bool
	var overlay []filebundle.Entry
	for _, e := range entries {
		if e.Path == DefinitionFile {
			if def, err = remotedef.Parse([]byte(e.Content)); err != nil {
				return remotedef.Definition{}, err
			}
			haveDef = true
			continue
		}
		overlay = append(overlay, e)
	}
	if !haveDef {
		return remotedef.Definition{}, fmt.Errorf("the bundle has no %s", DefinitionFile)
	}
	if err := filebundle.Write(dataDir, overlay, true, nil); err != nil {
		return remotedef.Definition{}, err
	}
	return def, nil
}
