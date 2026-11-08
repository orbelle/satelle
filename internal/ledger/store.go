package ledger

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Store is a ledger rooted at a directory: ledger/<item>.jsonl holds each
// item's chain, blobs/<sha256> holds content-addressed blobs.
type Store struct {
	dir string
	// Now stamps appended events; nil means time.Now.
	Now func() time.Time
	// OnWrite, when set, is called with every event this store writes (an
	// append, or an import from elsewhere), after it is on disk.
	OnWrite func(Event)
}

var itemPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// ValidItem reports whether id can name a chain (it becomes a file name).
func ValidItem(id string) bool { return itemPattern.MatchString(id) && len(id) <= 128 }

// Open opens (creating) a store rooted at dir.
func Open(dir string) (*Store, error) {
	for _, d := range []string{filepath.Join(dir, "ledger"), filepath.Join(dir, "blobs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("ledger: %w", err)
		}
	}
	return &Store{dir: dir}, nil
}

// Dir is the store's root.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(item string) string { return filepath.Join(s.dir, "ledger", item+".jsonl") }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Path returns the chain file of item.
func (s *Store) Path(item string) string { return s.path(item) }

// Append adds an event to item's chain. payload is marshalled to JSON (a
// json.RawMessage is taken as is); nil means no payload.
func (s *Store) Append(item, kind, actor string, payload any) (Event, error) {
	var e Event
	err := s.withLock(func() error {
		prev, err := s.tailHash(item)
		if err != nil {
			return err
		}
		if e, err = s.NewEvent(item, kind, actor, payload, prev); err != nil {
			return err
		}
		return s.write(e)
	})
	return e, err
}

// NewEvent builds a complete event linked to prev (id, time and hash set)
// without writing it, so a caller can offer it elsewhere first (a server's
// compare-and-append) and import it after. payload is as for Append.
func (s *Store) NewEvent(item, kind, actor string, payload any, prev string) (Event, error) {
	return NewEventWith(newEventID(), s.now().UTC(), item, kind, actor, payload, prev)
}

// NewEventWith builds a complete event with a given id and time, linked to
// prev and hashed, without writing it: for a caller that must rebuild the same
// event twice (an import that resumes) or carry another store's time.
func NewEventWith(id string, at time.Time, item, kind, actor string, payload any, prev string) (Event, error) {
	if !ValidItem(item) {
		return Event{}, fmt.Errorf("ledger: invalid item id %q", item)
	}
	if strings.TrimSpace(kind) == "" {
		return Event{}, errors.New("ledger: empty event kind")
	}
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Event{}, fmt.Errorf("ledger: payload: %w", err)
		}
		if string(b) != "null" {
			raw = b
		}
	}
	e := Event{ID: id, Item: item, Kind: kind, At: at.UTC(), Actor: actor, Payload: raw, Prev: prev}
	var err error
	e.Hash, err = HashOf(e)
	return e, err
}

// AppendRaw writes a complete event (from another store) after checking its
// hash and that it links to the current tail of its chain.
func (s *Store) AppendRaw(e Event) error {
	if !ValidItem(e.Item) {
		return fmt.Errorf("ledger: invalid item id %q", e.Item)
	}
	h, err := HashOf(e)
	if err != nil {
		return err
	}
	if h != e.Hash {
		return fmt.Errorf("ledger: event %s: hash does not match content", e.ID)
	}
	return s.withLock(func() error {
		prev, err := s.tailHash(e.Item)
		if err != nil {
			return err
		}
		if e.Prev != prev {
			return &StaleTail{Event: e.ID, Prev: e.Prev, Tail: prev}
		}
		return s.write(e)
	})
}

// StaleTail is AppendRaw's refusal of an event that does not link to the
// chain's current tail.
type StaleTail struct{ Event, Prev, Tail string }

func (e *StaleTail) Error() string {
	return fmt.Sprintf("ledger: event %s: prev %q does not link to tail %q", e.Event, e.Prev, e.Tail)
}

// Import merges chains from elsewhere (a pull): events already present (same
// hash at the same position) are skipped, the rest are appended with
// AppendRaw, which checks each hash and that it links to the local tail. A
// chain that diverges from the local one is refused before anything of that
// item is written past the point of divergence.
func (s *Store) Import(events []Event) (added int, err error) {
	have := map[string][]Event{}
	seen := map[string]int{}
	for _, e := range events {
		local, ok := have[e.Item]
		if !ok {
			if local, err = s.Read(e.Item); err != nil {
				return added, err
			}
			have[e.Item] = local
		}
		pos := seen[e.Item]
		seen[e.Item]++
		if pos < len(local) {
			if local[pos].Hash != e.Hash {
				return added, fmt.Errorf("ledger: %s diverges at event %d", e.Item, pos)
			}
			continue
		}
		if err := s.AppendRaw(e); err != nil {
			return added, err
		}
		added++
	}
	return added, nil
}

func (s *Store) write(e Event) error {
	line, err := Encode(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.path(e.Item), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("ledger: write %s: %w", e.Item, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if s.OnWrite != nil {
		s.OnWrite(e)
	}
	return nil
}

func (s *Store) withLock(fn func() error) error {
	f, err := os.OpenFile(filepath.Join(s.dir, "ledger.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("ledger: lock: %w", err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("ledger: lock: %w", err)
	}
	defer unlockFile(f)
	return fn()
}

// Head returns the hash of the last event on item's chain, "" when it has
// none.
func (s *Store) Head(item string) (string, error) {
	if !ValidItem(item) {
		return "", fmt.Errorf("ledger: invalid item id %q", item)
	}
	return s.tailHash(item)
}

// tailHash returns the hash of the last event on item's chain, "" when empty.
func (s *Store) tailHash(item string) (string, error) {
	f, err := os.Open(s.path(item))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	line, err := lastLine(f)
	if err != nil || len(line) == 0 {
		return "", err
	}
	var e Event
	if err := json.Unmarshal(line, &e); err != nil {
		return "", fmt.Errorf("ledger: %s tail: %w", item, err)
	}
	return e.Hash, nil
}

// lastLine returns the last newline-terminated line of f, reading backwards.
func lastLine(f *os.File) ([]byte, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	end := st.Size()
	const chunk = 4096
	var buf []byte
	for pos := end; pos > 0; {
		n := int64(chunk)
		if pos < n {
			n = pos
		}
		pos -= n
		b := make([]byte, n)
		if _, err := f.ReadAt(b, pos); err != nil && err != io.EOF {
			return nil, err
		}
		buf = append(b, buf...)
		trimmed := bytes.TrimRight(buf, "\n")
		if i := bytes.LastIndexByte(trimmed, '\n'); i >= 0 {
			return trimmed[i+1:], nil
		}
		if pos == 0 {
			return trimmed, nil
		}
	}
	return nil, nil
}

// Read returns item's whole chain in order.
func (s *Store) Read(item string) ([]Event, error) {
	events, _, err := s.ReadFrom(item, 0)
	return events, err
}

// ReadFrom returns the complete events on item's chain from byte offset on,
// and the offset after the last complete line. A partial trailing line is
// left for a later read.
func (s *Store) ReadFrom(item string, offset int64) ([]Event, int64, error) {
	if !ValidItem(item) {
		return nil, offset, fmt.Errorf("ledger: invalid item id %q", item)
	}
	f, err := os.Open(s.path(item))
	if errors.Is(err, os.ErrNotExist) {
		return nil, offset, nil
	}
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	r := bufio.NewReader(f)
	var out []Event
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			return out, offset, nil
		}
		if err != nil {
			return out, offset, err
		}
		offset += int64(len(line))
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return out, offset, fmt.Errorf("ledger: %s at byte %d: %w", item, offset-int64(len(line)), err)
		}
		out = append(out, e)
	}
}

// Size returns the byte length of item's chain file (0 when absent).
func (s *Store) Size(item string) (int64, error) {
	st, err := os.Stat(s.path(item))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// Items lists every item with a chain, sorted.
func (s *Store) Items() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "ledger"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".jsonl"); ok && !e.IsDir() && ValidItem(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Verify walks every chain and returns the first broken link (a *BrokenLink),
// or nil when every chain verifies.
func (s *Store) Verify() error {
	items, err := s.Items()
	if err != nil {
		return err
	}
	for _, item := range items {
		events, err := s.Read(item)
		if err != nil {
			return &BrokenLink{Item: item, Index: -1, Reason: err.Error()}
		}
		if err := VerifyChain(item, events); err != nil {
			return err
		}
	}
	return nil
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// PutBlob stores b content-addressed and returns its sha256 hex.
func (s *Store) PutBlob(b []byte) (string, error) {
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])
	path := filepath.Join(s.dir, "blobs", sha)
	if _, err := os.Stat(path); err == nil {
		return sha, nil
	}
	tmp, err := os.CreateTemp(filepath.Join(s.dir, "blobs"), ".tmp-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return sha, nil
}

// GetBlob returns the blob with the given sha256, checking its content.
func (s *Store) GetBlob(sha string) ([]byte, error) {
	if !shaPattern.MatchString(sha) {
		return nil, fmt.Errorf("ledger: invalid blob id %q", sha)
	}
	b, err := os.ReadFile(filepath.Join(s.dir, "blobs", sha))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("ledger: blob %s is %w", sha, ErrCorruptBlob)
	}
	return b, nil
}

// ErrCorruptBlob is wrapped by GetBlob's refusal of a stored body whose sha256 differs.
var ErrCorruptBlob = errors.New("corrupt")

func newEventID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
