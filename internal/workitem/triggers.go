package workitem

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Local task triggers. A task declares when it runs (Item.Triggers, the
// `triggers:` line of its file); satelle evaluates the declaration and runs one
// execution of the task when it fires. The grammar is the mechanism: every
// trigger, interval, status and threshold is the repo's configuration, and a
// status name is matched as written, never interpreted.

// Trigger kinds: the source an execution records for the trigger that fired it.
const (
	// TriggerSchedule fires when the trigger has made no execution within its
	// interval.
	TriggerSchedule = "schedule"
	// TriggerCount fires when at least a threshold of stories sit at a status.
	TriggerCount = "count"
)

// Input names a trigger (local or a remote wake) fills, when the task declares
// them: the trigger's name and its source.
const (
	InputTrigger       = "trigger"
	InputTriggerSource = "trigger_source"
)

// TriggerTagPrefix marks an execution a local trigger made:
// trigger:<task id>:<trigger name>.
const TriggerTagPrefix = "trigger:"

// TriggerDecl is one declared trigger of a task. Every is set for a schedule;
// Status and AtLeast for a count.
type TriggerDecl struct {
	Name    string        `json:"name"`
	Kind    string        `json:"kind"`
	Every   time.Duration `json:"every,omitempty"`
	Status  string        `json:"status,omitempty"`
	AtLeast int           `json:"at_least,omitempty"`
}

// Tag is the tag of the executions the trigger makes for task taskID.
func (d TriggerDecl) Tag(taskID string) string {
	return TriggerTagPrefix + taskID + ":" + d.Name
}

// countRE is a count expression: <status> >= <n>. The status grammar keeps the
// entry free of `,`, `=` and whitespace, so the list stays lossless.
var countRE = regexp.MustCompile(`^([A-Za-z0-9_-]+)>=([0-9]+)$`)

// ParseTriggers reads the `triggers:` frontmatter value: a comma list of
// `name=schedule:<go duration>` or `name=count:<status>>=<n>` entries. Names
// follow the input-name grammar. An empty line declares nothing.
func ParseTriggers(line string) ([]TriggerDecl, error) {
	if strings.TrimSpace(line) == "" {
		return nil, nil
	}
	var out []TriggerDecl
	seen := map[string]bool{}
	for _, entry := range strings.Split(line, ",") {
		entry = strings.TrimSpace(entry)
		name, spec, ok := strings.Cut(entry, "=")
		kind, expr, kok := strings.Cut(spec, ":")
		if !ok || !kok {
			return nil, fmt.Errorf("trigger %q must be name=schedule:<duration> or name=count:<status>>=<n>", entry)
		}
		d := TriggerDecl{Name: name, Kind: kind}
		if !inputNameRE.MatchString(d.Name) {
			return nil, fmt.Errorf("trigger name %q must match [a-z][a-z0-9_]*", d.Name)
		}
		switch kind {
		case TriggerSchedule:
			every, err := time.ParseDuration(expr)
			if err != nil || every <= 0 {
				return nil, fmt.Errorf("trigger %q has schedule %q: must be a positive duration such as 24h", d.Name, expr)
			}
			d.Every = every
		case TriggerCount:
			m := countRE.FindStringSubmatch(expr)
			if m == nil {
				return nil, fmt.Errorf("trigger %q has count %q: must be <status>>=<n>", d.Name, expr)
			}
			n, err := strconv.Atoi(m[2])
			if err != nil || n < 1 {
				return nil, fmt.Errorf("trigger %q has threshold %q: must be at least 1", d.Name, m[2])
			}
			d.Status, d.AtLeast = m[1], n
		default:
			return nil, fmt.Errorf("trigger %q has kind %q: must be schedule or count", d.Name, kind)
		}
		if seen[d.Name] {
			return nil, fmt.Errorf("trigger %q is declared twice", d.Name)
		}
		seen[d.Name] = true
		out = append(out, d)
	}
	return out, nil
}

// FormatTriggers is the inverse of ParseTriggers (canonical form).
func FormatTriggers(decls []TriggerDecl) string {
	parts := make([]string, 0, len(decls))
	for _, d := range decls {
		expr := d.Every.String()
		if d.Kind == TriggerCount {
			expr = d.Status + ">=" + strconv.Itoa(d.AtLeast)
		}
		parts = append(parts, d.Name+"="+d.Kind+":"+expr)
	}
	return strings.Join(parts, ", ")
}

// RunCategory is the category of an execution satelle makes itself (a trigger's
// or a remote wake's). The create's structure check asks every kind for one; it
// does not select the execution's workflow, which is resolved by its kind.
const RunCategory = "chore"

// EmptyCategory is the category of a story with no product content, taken
// through a workflow only to prove it or a remote. It has no route table of its
// own, so it runs the "*" spine; stats and estimate comparables leave it out.
const EmptyCategory = "empty"

// MapInputs keeps the values a task declares: a name the task does not declare
// is dropped, and a declared one with no value is left unset, so the create
// refuses it only if the task requires it.
func MapInputs(decls []InputDecl, have map[string]string) map[string]string {
	out := map[string]string{}
	for _, decl := range decls {
		if v := have[decl.Name]; v != "" {
			out[decl.Name] = v
		}
	}
	return out
}
