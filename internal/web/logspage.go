package web

import (
	"net/http"
	"os"
	"strings"
	"time"
)

// logsTailMax is the newest-first window the logs page renders.
const logsTailMax = 500

type logRow struct {
	Time       time.Time
	Level      string
	Text       string
	Structured bool
}

type logsPageData struct {
	Path   string
	Filter string
	Rows   []logRow
	TopBar topBar
}

func (s *MirrorServer) logsPage(w http.ResponseWriter, r *http.Request) {
	filter := logsFilter(r)
	rows, err := readTail(s.LogPath, logsTailMax)
	if err != nil && !os.IsNotExist(err) {
		httpError(w, err)
		return
	}
	mirrorRender(w, "logs", "/", logsPageData{
		Path:   s.LogPath,
		Filter: filter,
		Rows:   filterLogRows(rows, filter),
		TopBar: newTopBar(r.Context(), s.Store, "logs"),
	})
}

func (s *MirrorServer) logsFragment(w http.ResponseWriter, r *http.Request) {
	filter := logsFilter(r)
	rows, err := readTail(s.LogPath, logsTailMax)
	if err != nil && !os.IsNotExist(err) {
		httpError(w, err)
		return
	}
	mirrorRender(w, "logsLive", "/", logsPageData{
		Path:   s.LogPath,
		Filter: filter,
		Rows:   filterLogRows(rows, filter),
	})
}

func logsFilter(r *http.Request) string {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("level"))) {
	case "info", "warn", "error":
		return strings.ToLower(r.URL.Query().Get("level"))
	default:
		return ""
	}
}

func filterLogRows(rows []logRow, filter string) []logRow {
	if filter == "" {
		return rows
	}
	want := strings.ToUpper(filter)
	out := make([]logRow, 0, len(rows))
	for _, row := range rows {
		if !row.Structured || row.Level == want {
			out = append(out, row)
		}
	}
	return out
}

func readTail(path string, max int) ([]logRow, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := splitLogLines(string(raw))
	if max > 0 && len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	rows := make([]logRow, 0, len(lines))
	for i := len(lines) - 1; i >= 0; i-- {
		rows = append(rows, parseLogLine(lines[i]))
	}
	return rows, nil
}

func splitLogLines(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	parts := strings.Split(raw, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseLogLine(raw string) logRow {
	fields := strings.Split(raw, "\t")
	if len(fields) >= 6 && isLogLevel(fields[1]) {
		if ts, err := time.Parse(time.RFC3339, fields[0]); err == nil {
			text := strings.Join(fields[2:], " ")
			return logRow{Time: ts, Level: fields[1], Text: text, Structured: true}
		}
	}
	return logRow{Text: raw}
}

func isLogLevel(s string) bool {
	return s == "INFO" || s == "WARN" || s == "ERROR"
}
