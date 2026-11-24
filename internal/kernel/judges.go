package kernel

import (
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/bobmcallan/satelle/internal/gate"
	"github.com/bobmcallan/satelle/internal/ledger"
)

const UnknownModel = "unknown"

type JudgeRow struct {
	Gate              string `json:"gate"`
	Model             string `json:"model"`
	Runs              int    `json:"runs"`
	Accepts           int    `json:"accepts"`
	Rejects           int    `json:"rejects"`
	Errors            int    `json:"errors"`
	ConfirmedCatches  int    `json:"confirmed_catches"`
	OverturnedRejects int    `json:"overturned_rejects"`
	Measured          int    `json:"measured"`
	TokensIn          int    `json:"tokens_in"`
	TokensOut         int    `json:"tokens_out"`
	MedianDurationMS  int64  `json:"median_duration_ms"`
}

func (k *Kernel) JudgeReport(since, until time.Time) ([]JudgeRow, error) {
	all, err := k.Cache.Events("", gate.KindRun)
	if err != nil {
		return nil, err
	}
	var items []string
	for _, e := range all {
		if !slices.Contains(items, e.Item) {
			items = append(items, e.Item)
		}
	}
	byItem := make([][]ledger.Event, 0, len(items))
	for _, id := range items {
		events, err := k.Cache.Events(id, gate.KindRun)
		if err != nil {
			return nil, err
		}
		byItem = append(byItem, events)
	}
	return judgeRows(byItem, since, until), nil
}

type rejectClass int

const (
	unresolved rejectClass = iota
	confirmedCatch
	overturnedReject
)

func judgeRows(byItem [][]ledger.Event, since, until time.Time) []JudgeRow {
	type tally struct {
		row       JudgeRow
		durations []int64
	}
	byKey := map[[2]string]*tally{}
	inWindow := func(at time.Time) bool {
		return (since.IsZero() || !at.Before(since)) && (until.IsZero() || at.Before(until))
	}
	for _, events := range byItem {
		var runs []gate.Result
		for _, r := range GateRuns(events) {
			if isJudgedGateRun(r) {
				runs = append(runs, r)
			}
		}
		for i, r := range runs {
			if !inWindow(r.At) {
				continue
			}
			for _, v := range verdictsOfResult(r) {
				model := modelKey(v.modelID, v.model)
				key := [2]string{r.Gate, model}
				t, ok := byKey[key]
				if !ok {
					t = &tally{row: JudgeRow{Gate: r.Gate, Model: model}}
					byKey[key] = t
				}
				t.row.Runs++
				switch v.result {
				case gate.Accept:
					t.row.Accepts++
				case gate.Reject:
					t.row.Rejects++
					switch classifyReject(runs, i) {
					case confirmedCatch:
						t.row.ConfirmedCatches++
					case overturnedReject:
						t.row.OverturnedRejects++
					}
				default:
					t.row.Errors++
				}
				if v.measured {
					t.row.Measured++
					t.row.TokensIn += v.tokensIn
					t.row.TokensOut += v.tokensOut
					t.durations = append(t.durations, v.durationMS)
				}
			}
		}
	}
	out := make([]JudgeRow, 0, len(byKey))
	for _, t := range byKey {
		t.row.MedianDurationMS = median(t.durations)
		out = append(out, t.row)
	}
	slices.SortFunc(out, func(a, b JudgeRow) int {
		if c := strings.Compare(a.Gate, b.Gate); c != 0 {
			return c
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out
}

func classifyReject(runs []gate.Result, i int) rejectClass {
	r := runs[i]
	if len(r.Members) > 0 && r.Result == gate.Accept {
		return overturnedReject
	}
	for _, next := range runs[i+1:] {
		if next.Gate != r.Gate || next.Result != gate.Accept {
			continue
		}
		switch {
		case r.Coverage == "" || next.Coverage == "":
			return unresolved
		case next.Coverage == r.Coverage:
			return overturnedReject
		default:
			return confirmedCatch
		}
	}
	return unresolved
}

func median(ds []int64) int64 {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func isJudgedGateRun(r gate.Result) bool {
	return r.Binding != "" || len(r.Members) > 0
}

func normalizeModel(model string) string {
	if model == "" {
		return UnknownModel
	}
	return model
}

func ModelID(reported string, aliases map[string]string) string {
	id := modelSlug(reported)
	if id == "" {
		return ""
	}
	for _, from := range slices.Sorted(maps.Keys(aliases)) {
		if modelSlug(from) == id {
			return modelSlug(aliases[from])
		}
	}
	return id
}

func modelSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	return b.String()
}

func (k *Kernel) modelID(reported string) string {
	return ModelID(reported, k.Settings.Models.Aliases)
}

func modelKey(id, reported string) string {
	if id != "" {
		return id
	}
	return normalizeModel(reported)
}
