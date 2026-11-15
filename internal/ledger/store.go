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

type Store struct {
	dir      string
	Now      func() time.Time
	OnWrite  func(Event)
	readOnly string
}

var ErrReadOnly = errors.New("ledger: read-only")

func (s *Store) SetReadOnly(reason string) { s.readOnly = reason }

func (s *Store) refuseWrite() error {
	if s.readOnly == "" {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrReadOnly, s.readOnly)
}

var itemPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func ValidItem(id string) bool { return itemPattern.MatchString(id) && len(id) <= 128 }

func Open(dir string) (*Store, error) {
	for _, d := range []string{filepath.Join(dir, "ledger"), filepath.Join(dir, "blobs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("ledger: %w", err)
		}
	}
	return &Store{dir: dir}, nil
}

func OpenExisting(dir string) (*Store, error) {
	st, err := os.Stat(filepath.Join(dir, "ledger"))
	if err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("ledger: %s is not a directory: %w", filepath.Join(dir, "ledger"), os.ErrNotExist)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) path(item string) string { return filepath.Join(s.dir, "ledger", item+".jsonl") }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) Path(item string) string { return s.path(item) }

func (s *Store) Append(item, kind, actor string, payload any) (Event, error) {
	var e Event
	if err := s.refuseWrite(); err != nil {
		return e, err
	}
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

func (s *Store) NewEvent(item, kind, actor string, payload any, prev string) (Event, error) {
	return NewEventWith(newEventID(), s.now().UTC(), item, kind, actor, payload, prev)
}

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

func (s *Store) AppendRaw(e Event) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
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

func (s *Store) Replace(item, head string, events []Event) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	if !ValidItem(item) {
		return fmt.Errorf("ledger: invalid item id %q", item)
	}
	if head == "" {
		return fmt.Errorf("ledger: replace %s: no head given; a replace is not a create", item)
	}
	if len(events) == 0 {
		return fmt.Errorf("ledger: replace %s: no events given", item)
	}
	if err := VerifyChain(item, events); err != nil {
		return fmt.Errorf("ledger: replace %s: %w", item, err)
	}
	var body bytes.Buffer
	for _, e := range events {
		line, err := Encode(e)
		if err != nil {
			return err
		}
		body.Write(line)
		body.WriteByte('\n')
	}
	return s.withLock(func() error {
		tail, err := s.tailHash(item)
		if err != nil {
			return err
		}
		if tail != head {
			return &StaleTail{Event: events[0].ID, Prev: head, Tail: tail}
		}
		if err := s.replaceFile(item, body.Bytes()); err != nil {
			return fmt.Errorf("ledger: replace %s: %w", item, err)
		}
		if s.OnWrite != nil {
			for _, e := range events {
				s.OnWrite(e)
			}
		}
		return nil
	})
}

func (s *Store) replaceFile(item string, body []byte) error {
	if err := s.refuseWrite(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path(item)), ".tmp-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(body)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.path(item))
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

type StaleTail struct{ Event, Prev, Tail string }

func (e *StaleTail) Error() string {
	return fmt.Sprintf("ledger: event %s: prev %q does not link to tail %q", e.Event, e.Prev, e.Tail)
}

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
	if err := s.refuseWrite(); err != nil {
		return err
	}
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

func (s *Store) Head(item string) (string, error) {
	if !ValidItem(item) {
		return "", fmt.Errorf("ledger: invalid item id %q", item)
	}
	return s.tailHash(item)
}

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

func (s *Store) Read(item string) ([]Event, error) {
	events, _, err := s.ReadFrom(item, 0)
	return events, err
}

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

func (s *Store) PutBlob(b []byte) (string, error) {
	if err := s.refuseWrite(); err != nil {
		return "", err
	}
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

var ErrCorruptBlob = errors.New("corrupt")

func newEventID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
