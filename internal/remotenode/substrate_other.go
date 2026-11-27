//go:build !unix

package remotenode

func identity(string) (statID, error) { return statID{}, nil }
