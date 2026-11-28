package capacity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const Cadence = 30 * time.Second

func fresh(takenAt, now time.Time) bool {
	age := now.Sub(takenAt)
	return age >= 0 && age < Cadence
}

func Reuse(path string, now time.Time, read func() Snapshot) (Snapshot, error) {
	if b, err := os.ReadFile(path); err == nil {
		var sn Snapshot
		if json.Unmarshal(b, &sn) == nil && fresh(sn.TakenAt, now) {
			return sn, nil
		}
	}
	sn := read()
	return sn, store(path, sn)
}

func store(path string, sn Snapshot) error {
	b, err := json.Marshal(sn)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}
