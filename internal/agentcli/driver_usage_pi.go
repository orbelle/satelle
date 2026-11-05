package agentcli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// piSessionDirName is pi's per-repo session directory name under
// ~/.pi/agent/sessions/: the absolute cwd with its leading separator removed and
// every "/", "\" and ":" turned into "-", wrapped in "--…--" (pi's
// docs/session-format.md; confirmed against the real directory
// ~/.pi/agent/sessions/--home-bobmcallan-Development-satelle--/, sty_ca1ca935).
func piSessionDirName(repoRoot string) string {
	p := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(repoRoot)), "/")
	return "--" + strings.NewReplacer("/", "-", `\`, "-", ":", "-").Replace(p) + "--"
}

// piUnpricedCostReason is the one reason both pi readers (cumulative and windowed)
// give for a zero usage.cost.total beside real token counts.
const piUnpricedCostReason = "pi: usage.cost.total is zero beside real token counts (model unpriced by pi)"

// piUsageRow is one assistant message that made a model call: its own usage and
// the time pi wrote it.
type piUsageRow struct {
	at                                   time.Time // zero when the row carries no parseable timestamp
	model                                string
	input, output, cacheRead, cacheWrite int
	cost                                 float64
}

// piSessionRecord is every usage-bearing assistant row of one pi session, in file
// order, plus the model the session last named.
type piSessionRecord struct {
	rows      []piUsageRow
	lastModel string
	// created is when pi created the session: the {"type":"session"} header's
	// timestamp, else the file name's UTC prefix. Zero when neither parses.
	created time.Time
}

// piFileNameTime parses the UTC prefix of a pi session file name
// (<2006-01-02T15-04-05-000Z>_<sessionId>.jsonl).
func piFileNameTime(path string) time.Time {
	prefix, _, ok := strings.Cut(filepath.Base(path), "_")
	if !ok {
		return time.Time{}
	}
	t, err := time.Parse("2006-01-02T15-04-05-000Z", prefix)
	if err != nil {
		return time.Time{}
	}
	return t
}

// readPiSession is the one pi session-record iterator: the cumulative reader and
// the windowed reader both fold its rows, so the two cannot drift apart. A non-empty
// reason says why the record could not be read, pi-named.
//
// The record is located by session id: repoRoot's tree, then each hint root's, then
// every tree (locateSessionRecord).
func readPiSession(sessionID, repoRoot string, hints ...string) (piSessionRecord, string) {
	var rec piSessionRecord
	path, reason := locateSessionRecord(HarnessPi, sessionID, filepath.Join(piHomeDir(), "sessions"),
		piSessionDirName, "*_"+sessionID+".jsonl", append([]string{repoRoot}, hints...))
	if reason != "" {
		return rec, reason
	}
	f, err := os.Open(path)
	if err != nil {
		return rec, fmt.Sprintf("pi: session record unreadable: %v", err)
	}
	defer f.Close()
	rec.created = piFileNameTime(path)
	headerSeen := false

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var row struct {
			Type      string `json:"type"`
			ModelID   string `json:"modelId"`
			Timestamp string `json:"timestamp"`
			Message   struct {
				Role  string `json:"role"`
				Model string `json:"model"`
				Usage *struct {
					Input      *int `json:"input"`
					Output     *int `json:"output"`
					CacheRead  int  `json:"cacheRead"`
					CacheWrite int  `json:"cacheWrite"`
					Cost       *struct {
						Total *float64 `json:"total"`
					} `json:"cost"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		if row.Type == "session" && !headerSeen {
			headerSeen = true
			if t, err := time.Parse(time.RFC3339Nano, row.Timestamp); err == nil {
				rec.created = t
			}
		}
		if row.Type == "model_change" && row.ModelID != "" {
			rec.lastModel = row.ModelID
		}
		u := row.Message.Usage
		if row.Type != "message" || row.Message.Role != "assistant" || u == nil || u.Input == nil || u.Output == nil {
			continue
		}
		if *u.Input+*u.Output+u.CacheRead+u.CacheWrite == 0 {
			// pi records a failed request as an assistant row with all-zero usage: it
			// made no measured call.
			continue
		}
		r := piUsageRow{input: *u.Input, output: *u.Output, cacheRead: u.CacheRead, cacheWrite: u.CacheWrite}
		if u.Cost != nil && u.Cost.Total != nil {
			r.cost = *u.Cost.Total
		}
		if row.Message.Model != "" {
			rec.lastModel = row.Message.Model
		}
		r.model = rec.lastModel
		if t, err := time.Parse(time.RFC3339Nano, row.Timestamp); err == nil {
			r.at = t
		}
		rec.rows = append(rec.rows, r)
	}
	if err := scanner.Err(); err != nil {
		return piSessionRecord{}, fmt.Sprintf("pi: session record unreadable: %v", err)
	}
	return rec, ""
}

// piWindowRows is the rows of rec timestamped inside [from, to] (inclusive at
// both ends) and how many rows carry no parseable timestamp, so cannot be placed.
// It is the one window filter: piWindowUsage and the per-run reader both fold it.
func piWindowRows(rec piSessionRecord, from, to time.Time) (rows []piUsageRow, untimed int) {
	for _, r := range rec.rows {
		switch {
		case r.at.IsZero():
			untimed++
		case r.at.Before(from) || r.at.After(to):
		default:
			rows = append(rows, r)
		}
	}
	return rows, untimed
}
