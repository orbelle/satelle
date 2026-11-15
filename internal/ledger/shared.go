package ledger

import "encoding/json"

func Encode(e Event) ([]byte, error) { return json.Marshal(e) }

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
	lo, hi := 0, m-1
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
