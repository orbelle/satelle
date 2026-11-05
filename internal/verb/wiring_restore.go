package verb

import (
	"reflect"
	"unsafe"
)

// Test support: wiringVars is the one list of verb's per-invocation wiring —
// every package-level variable a Set*/Clear*/Add* function (or a test seam)
// writes. SnapshotWiring reads it to put a test back exactly where it found
// the package. A package-level var that is neither here nor in wiringExempt
// fails TestWiringVarsComplete, so a new piece of wiring cannot be added and
// forgotten.
type wiringVar struct {
	name string
	ptr  any // pointer to the package-level variable
}

var wiringVars = []wiringVar{
	// Stores and the transaction seam (wiring.go).
	{"workItemStore", &workItemStore},
	{"ledgerStore", &ledgerStore},
	{"docIndexStore", &docIndexStore},
	{"leaseStore", &leaseStore},
	{"txRunner", &txRunner},
	{"changeNotifiers", &changeNotifiers},
	// Directories and substrate roots.
	{"storyDir", &storyDir},
	{"dataDir", &dataDir},
	{"backupsDir", &backupsDir},
	{"taskDir", &taskDir},
	// Config-derived policy.
	{"agentsLayer", &agentsLayer},
	{"agentsVars", &agentsVars},
	{"agentsWired", &agentsWired},
	{"attachMaxBytes", &attachMaxBytes},
	{"attachAllowTypes", &attachAllowTypes},
	// Resolvers, reviewers and hooks.
	{"assigneeResolver", &assigneeResolver},
	{"engageGuard", &engageGuard},
	{"dirtyPathsProbe", &dirtyPathsProbe},
	{"actorResolver", &actorResolver},
	{"locationResolver", &locationResolver},
	{"transitionGater", &transitionGater},
	{"createReviewer", &createReviewer},
	{"amendReviewer", &amendReviewer},
	{"workflowResolver", &workflowResolver},
	{"executorDispatcher", &executorDispatcher},
	{"afterTagCASGetHook", &afterTagCASGetHook},
	// Seams with a non-zero production default: restore puts the prior value
	// back, never nil, so the default survives.
	{"verdictRecord", &verdictRecord},
}

// wiringExempt names every package-level variable that is NOT per-invocation
// wiring, with the reason. SnapshotWiring never copies or resets these.
var wiringExempt = map[string]string{
	"verbs": "the verb registry: process-lifetime state filled at init (test-only verbs included), guarded by mu",
	"mu":    "the registry's sync.RWMutex; copying a mutex by value is a vet copylocks defect",

	"ErrStoreNotConfigured": "Err* sentinel, assigned once at init and compared by identity",
	"errNoBaseline":         "error sentinel, assigned once at init and compared by identity",
	"errEmptyHead":          "error sentinel, assigned once at init and compared by identity",
	"errForeignTree":        "error sentinel, assigned once at init and compared by identity",
	"errNoGit":              "error sentinel, assigned once at init and compared by identity",
	"errNoReadinessStep":    "error sentinel, assigned once at init and matched with errors.Is",
	"errEpicAcceptFound":    "error sentinel, assigned once at init to stop a ledger scan",

	"retrieveHashRE":    "compiled regexp, immutable",
	"summaryStoryID":    "compiled regexp, immutable",
	"goTestFunc":        "compiled regexp, immutable",
	"secretKeyPattern":  "compiled regexp, immutable",
	"worktreeIDPattern": "compiled regexp, immutable",

	"recoveryChoices":         "immutable lookup table, never written after init",
	"terminalStoryStates":     "immutable lookup table, never written after init",
	"terminalExecutionStates": "immutable lookup table, never written after init",

	"wiringVars":   "the snapshot table itself",
	"wiringExempt": "the exemption table itself",
}

// SnapshotWiring is test support. It records the value of every per-invocation
// wiring variable in package verb and returns two functions: restore writes the
// recorded values back (idempotent), and changed lists, by variable name, every
// wiring variable whose current value differs from the snapshot.
//
// A test that runs the CLI or a verb in-process, or that calls a Set*, Clear*
// or Add* function, takes a snapshot first and restores it in t.Cleanup, so the
// next test finds the package as it was. Restoring the snapshot — not nil or a
// zero value — is what keeps the production defaults (verdictRecord,
// dispatchLogsDir, changelogPath, driverWindowReader, driverSnapshotter,
// engagementParallel) alive.
//
// Func variables are compared by closure identity: two closures built from one
// literal over different captures are different values, which is how the
// per-run resolvers installed by the CLI are told apart. Pointers, maps and
// slices are compared by identity (a slice also by length); anything else by
// deep equality.
func SnapshotWiring() (restore func(), changed func() []string) {
	saved := make([]reflect.Value, len(wiringVars))
	for i, w := range wiringVars {
		cur := reflect.ValueOf(w.ptr).Elem()
		cp := reflect.New(cur.Type()).Elem()
		cp.Set(cur)
		saved[i] = cp
	}
	restore = func() {
		for i, w := range wiringVars {
			reflect.ValueOf(w.ptr).Elem().Set(saved[i])
		}
	}
	changed = func() []string {
		var names []string
		for i, w := range wiringVars {
			if !sameWiring(reflect.ValueOf(w.ptr).Elem(), saved[i]) {
				names = append(names, w.name)
			}
		}
		return names
	}
	return restore, changed
}

// sameWiring reports whether two values of one wiring variable's type are the
// same wiring.
func sameWiring(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Func:
		return funcWord(a) == funcWord(b)
	case reflect.Map, reflect.Pointer, reflect.Chan, reflect.UnsafePointer:
		return a.Pointer() == b.Pointer()
	case reflect.Slice:
		return a.IsNil() == b.IsNil() && a.Len() == b.Len() && (a.Len() == 0 || a.Pointer() == b.Pointer())
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		ae, be := a.Elem(), b.Elem()
		if ae.Type() != be.Type() {
			return false
		}
		switch ae.Kind() {
		case reflect.Func, reflect.Map, reflect.Pointer, reflect.Chan, reflect.UnsafePointer:
			// An interface's dynamic value is not addressable, so a func inside
			// one compares by code pointer; the wiring interfaces hold pointers.
			return ae.Pointer() == be.Pointer()
		}
		return reflect.DeepEqual(ae.Interface(), be.Interface())
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

// funcWord reads a func variable's own value word: the pointer to its closure.
// reflect.Value.Pointer returns only the code pointer, which two closures built
// from one literal share, so it cannot tell a per-run resolver from the one it
// replaced. v must be addressable (every wiring variable and snapshot copy is).
func funcWord(v reflect.Value) unsafe.Pointer {
	return *(*unsafe.Pointer)(v.Addr().UnsafePointer())
}
