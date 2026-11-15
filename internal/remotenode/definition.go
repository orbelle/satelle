package remotenode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/bobmcallan/satelle/internal/filebundle"
	"github.com/bobmcallan/satelle/internal/kernel"
	"github.com/bobmcallan/satelle/internal/remotedef"
)

const DefinitionFile = kernel.RemoteFile

func InstallDefinition(dataDir string, bundle []byte) (remotedef.Definition, error) {
	def, overlay, err := ReadBundle(bundle)
	if err != nil {
		return remotedef.Definition{}, err
	}
	return def, writeOverlay(dataDir, overlay)
}

type fileEntry = filebundle.Entry

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

func DefinitionDigest(bundle []byte) (string, error) {
	entries, err := filebundle.Decode(bundle, nil)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.Path == DefinitionFile {
			sum := sha256.Sum256([]byte(e.Content))
			return hex.EncodeToString(sum[:]), nil
		}
	}
	return "", fmt.Errorf("the bundle has no %s", DefinitionFile)
}

func writeOverlay(dataDir string, overlay []fileEntry) error {
	if len(overlay) == 0 {
		return nil
	}
	return filebundle.Write(dataDir, overlay, true, nil)
}
