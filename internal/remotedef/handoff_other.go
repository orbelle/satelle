//go:build !unix

package remotedef

import "os"

// privateToMe cannot read a file's owner off unix, so no hand-off is read
// there: a node runs on linux.
func privateToMe(os.FileInfo) bool { return false }

// joinAlive is never asked off unix: privateToMe refuses first.
func joinAlive(int) bool { return false }
