//go:build !unix

package remotenode

import "errors"

func StartDetachedRun(StartDetachedOptions) error {
	return errors.New("--detach is only served on a unix node")
}
