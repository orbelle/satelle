package ledger

import "encoding/json"

// Encode is an event as the store writes it: its JSON, the chain file's line
// without the newline. A replicated event object's body is exactly this.
func Encode(e Event) ([]byte, error) { return json.Marshal(e) }

// SharedPrefix is the number of leading events that local and another chain of
// n events share, where hashAt(seq) is the other chain's hash at seq.
//
// It compares at the last position both chains hold, min(len(local), n)-1, so
// a fork is found whether the other chain is longer than, as long as, or
// shorter than local; equal there means every earlier event is shared too.
// Otherwise it binary-searches the first differing position. Both steps rely
// on each hash covering prev: a hash equal at k means the chains are equal at
// every position up to k. A caller that appends what it fetched past the
// shared prefix still verifies it (VerifyChain or AppendRaw).
func SharedPrefix(local []Event, n int, hashAt func(seq int) (string, error)) (int, error) {
	m := min(len(local), n)
	if m == 0 {
		return 0, nil
	}
	h, err := hashAt(m - 1)
	if err != nil {
		return 0, err
	}
	if h == local[m-1].Hash {
		return m, nil
	}
	lo, hi := 0, m-1 // the chains differ at hi
	for lo < hi {
		mid := (lo + hi) / 2
		h, err := hashAt(mid)
		if err != nil {
			return 0, err
		}
		if h == local[mid].Hash {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, nil
}
