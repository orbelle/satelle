# Fixtures — satelle-code-ac-review (prohibition-shaped criteria)

Three reference cases for the prohibition-falsification section of the
`satelle-code-ac-review` gate (`in_progress → integration`). Each states the
acceptance criterion under test, the presented code's relevant paths, and the
verdict the rubric must return. Modelled on the real regression this section
was written to catch (`tests/plannerbench/evidence.go`, `parseUsageEvidence`).

## Case A — REJECT (compliant branch present, violating branch also present)

Acceptance criterion:

> "Unreported token or cost usage is represented as unavailable with
> provenance, never numeric zero."

Presented code (one function, two return paths):

```go
func parseUsage(cost, ledger string) Usage {
    if n, ok := parseCost(cost); ok {
        return Usage{Available: true, Total: n}
    }
    if usageAvailable(ledger) {
        n := parseLedgerTotal(ledger) // 0 when no match
        return Usage{Available: true, Total: n}
    }
    return Usage{Available: false}
}
```

Expected verdict:

```json
{"decision": "reject", "notes": "artifact.usage: parseUsage's ledger branch returns Available:true with a zero Total when parseLedgerTotal finds no match, so unreported usage surfaces as a measured zero, violating the never-numeric-zero criterion. The unavailable branch below it is compliant but does not excuse the violating one."}
```

Rationale: a reviewer scanning for *a* compliant path (the final `return
Usage{Available: false}`) would accept. Falsification requires checking every
return path that can produce a `Usage` value; the middle branch violates the
criterion even though a compliant sibling exists.

## Case B — ACCEPT (every path holds the prohibition)

Same acceptance criterion. Presented code:

```go
func parseUsage(cost, ledger string) Usage {
    if n, ok := parseCost(cost); ok {
        return Usage{Available: true, Total: n}
    }
    if n, ok := parseLedgerTotal(ledger); ok {
        return Usage{Available: true, Total: n}
    }
    return Usage{Available: false}
}
```

Expected verdict:

```json
{"decision": "accept", "notes": "artifact.usage: every return path either reports a parsed, available total or Available:false; no path can produce an available zero."}
```

Rationale: the ledger branch now only reports `Available: true` when
`parseLedgerTotal` actually found a value (`ok`), closing the gap Case A
exploited. The surface (both `if` branches plus the final `return`) is fully
enumerated and every member holds.

## Case C — REJECT (surface cannot be enumerated from the presented tree)

Same acceptance criterion. Presented code calls an external, unread
dependency for the value:

```go
func parseUsage(cost, ledger string) Usage {
    return thirdparty.ParseUsage(cost, ledger) // vendored, not in this tree
}
```

Expected verdict:

```json
{"decision": "reject", "notes": "artifact.usage: the criterion's surface (every path that can produce a Usage value) is not enumerable from the presented tree — the implementation is delegated to an unread vendored dependency. Insufficient evidence to falsify; not accepted on the absence of a visible violation."}
```

Rationale: no path is visibly wrong, but none is visibly right either. A
presence-only reading would accept (nothing failing is in view); falsification
requires the surface to be enumerable, and here it is not.
