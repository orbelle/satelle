package ledgerstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ConformancePrefix is the only prefix store-test creates or reads keys under.
const ConformancePrefix = "conformance/"

// OpUnknown is the operation store-test sends to provoke an error line: the
// contract makes an operation the command does not know a permanent error.
const OpUnknown = "store-test-unknown-operation"

// The conformance checks, in the order they run. The two read-only checks run
// before anything is written, so a store that has never been written is
// exercised too.
const (
	CheckListEmpty     = "list of a prefix with no keys is empty"
	CheckGetMissing    = "get reports a missing key"
	CheckPutNew        = "put-if-absent creates a new key"
	CheckPutExisting   = "put-if-absent refuses an existing key and returns its body"
	CheckGetPresent    = "get returns a present key's body"
	CheckListSorted    = "list returns the keys under a prefix, sorted"
	CheckListAfter     = "list starts after <after-key>"
	CheckErrorLine     = "an error prints error: <code>: <message>"
	ConformanceChecks  = 8
	conformanceBodyFmt = "{\"satelle\":\"store-test\",\"run\":%q,\"body\":%q}\n"
)

// CheckResult is one conformance check's outcome.
type CheckResult struct {
	Name   string
	OK     bool
	Reason string
}

// RunPrefix is a fresh run prefix under ConformancePrefix:
// conformance/<UTC yyyymmddThhmmssZ>-<8 hex>/. The contract has no delete, so
// each run's keys stay; a fresh prefix keeps runs apart.
func RunPrefix(now time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return ConformancePrefix + now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b) + "/"
}

// Conformance runs the checks against c under a fresh run prefix and returns
// one result per check, in order.
func Conformance(ctx context.Context, c Command) []CheckResult {
	return conformance(ctx, c, RunPrefix(time.Now()))
}

func conformance(ctx context.Context, c Command, run string) []CheckResult {
	var out []CheckResult
	pass := func(name string) { out = append(out, CheckResult{Name: name, OK: true}) }
	fail := func(name, format string, args ...any) {
		out = append(out, CheckResult{Name: name, Reason: fmt.Sprintf(format, args...)})
	}
	bodyA := []byte(fmt.Sprintf(conformanceBodyFmt, run, "A"))
	bodyB := []byte(fmt.Sprintf(conformanceBodyFmt, run, "B"))
	keyA := run + "a"

	// 1. list of a prefix with no keys, before any write.
	if keys, err := c.List(ctx, run+"none/", ""); err != nil {
		fail(CheckListEmpty, "%v", err)
	} else if len(keys) > 0 {
		fail(CheckListEmpty, "list %snone/ returned %d keys where none exist (%s); the contract expects exit 0 and no output", run, len(keys), strings.Join(keys, ", "))
	} else {
		pass(CheckListEmpty)
	}

	// 2. get of a missing key, before any write.
	if _, found, err := c.Get(ctx, run+"missing"); err != nil {
		fail(CheckGetMissing, "%v", err)
	} else if found {
		fail(CheckGetMissing, "get %smissing exited 0 (found) for a key never written; the contract expects exit 4", run)
	} else {
		pass(CheckGetMissing)
	}

	// 3. put-if-absent of a new key.
	created := false
	if _, ok, err := c.PutIfAbsent(ctx, keyA, bodyA); err != nil {
		fail(CheckPutNew, "%v", err)
	} else if !ok {
		fail(CheckPutNew, "put-if-absent %s exited 3 (already present) for a new key; the contract expects exit 0", keyA)
	} else {
		created = true
		pass(CheckPutNew)
	}

	// 4. put-if-absent of the same key with a different body.
	if !created {
		fail(CheckPutExisting, "setup: %s did not pass, so %s does not exist", CheckPutNew, keyA)
	} else if existing, ok, err := c.PutIfAbsent(ctx, keyA, bodyB); err != nil {
		fail(CheckPutExisting, "%v", err)
	} else if ok {
		fail(CheckPutExisting, "put-if-absent %s exited 0 (created) for an existing key; the contract expects exit 3 with the existing body on stdout", keyA)
	} else if !bytes.Equal(existing, bodyA) {
		fail(CheckPutExisting, "put-if-absent %s exited 3 but printed %d bytes that are not the existing %d-byte body", keyA, len(existing), len(bodyA))
	} else {
		pass(CheckPutExisting)
	}

	// 5. get of a present key.
	if !created {
		fail(CheckGetPresent, "setup: %s did not pass, so %s does not exist", CheckPutNew, keyA)
	} else if body, found, err := c.Get(ctx, keyA); err != nil {
		fail(CheckGetPresent, "%v", err)
	} else if !found {
		fail(CheckGetPresent, "get %s exited 4 (no such key) for a key put-if-absent created", keyA)
	} else if bytes.Equal(body, bodyB) {
		fail(CheckGetPresent, "get %s returned the second body: put-if-absent overwrote an existing key", keyA)
	} else if !bytes.Equal(body, bodyA) {
		fail(CheckGetPresent, "get %s returned %d bytes that are not the %d-byte body put", keyA, len(body), len(bodyA))
	} else {
		pass(CheckGetPresent)
	}

	// 6 and 7. list with and without an after-key, over keys put out of order
	// beside a sibling outside the prefix.
	listPrefix := run + "l/"
	var setupErr error
	for _, k := range []string{listPrefix + "b", listPrefix + "a", listPrefix + "c", run + "m"} {
		if _, ok, err := c.PutIfAbsent(ctx, k, []byte(fmt.Sprintf(conformanceBodyFmt, run, k))); err != nil {
			setupErr = err
			break
		} else if !ok {
			setupErr = fmt.Errorf("put-if-absent %s exited 3 (already present) for a new key", k)
			break
		}
	}
	listCheck := func(name, after string, want []string) {
		if setupErr != nil {
			fail(name, "setup: %v", setupErr)
			return
		}
		keys, err := c.List(ctx, listPrefix, after)
		if err != nil {
			fail(name, "%v", err)
			return
		}
		if !slices.Equal(keys, want) {
			args := listPrefix
			if after != "" {
				args += " " + after
			}
			fail(name, "list %s returned [%s]; the contract expects [%s]", args, strings.Join(keys, ", "), strings.Join(want, ", "))
			return
		}
		pass(name)
	}
	listCheck(CheckListSorted, "", []string{listPrefix + "a", listPrefix + "b", listPrefix + "c"})
	listCheck(CheckListAfter, listPrefix+"a", []string{listPrefix + "b", listPrefix + "c"})

	// 8. the error line, provoked by an operation the command does not know.
	key := run + "x"
	if r, err := c.run(ctx, OpUnknown, []string{key}, nil); err != nil {
		fail(CheckErrorLine, "%v", err)
	} else if r.exit != ExitPermanent {
		fail(CheckErrorLine, "%s %s exited %d; the contract expects exit 2 (permanent) for an unknown operation", OpUnknown, key, r.exit)
	} else if r.line == "" {
		fail(CheckErrorLine, "%s %s exited 2 but printed no error: <code>: <message> line on stderr (code one of auth, not-found, conflict, unavailable, invalid; stderr was %d bytes)", OpUnknown, key, r.stderrLen)
	} else {
		pass(CheckErrorLine)
	}
	return out
}
