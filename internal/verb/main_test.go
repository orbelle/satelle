package verb

import (
	"unsafe"
)

// startWiring is verb's wiring as the process left package init, recorded before
// any test runs so a test that reads it does not depend on shuffle order.
var startWiring struct {
	funcs    map[string]unsafe.Pointer // closure word of each func seam with a production default
	parallel string
}
