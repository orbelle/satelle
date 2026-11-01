// migrate.go — surgical format-migration for operator-owned agents.toml
// (epic:substrate-convergence order:7). harness= → command=; expand bare
// claude|grok presets to full templates; add missing role=.
// Comment-preserving line edits only — never a TOML marshal round-trip.
package config

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/bobmcallan/satelle/internal/agentcli"
)

// MigrateAgents rewrites format drift in an agents.toml body:
//   - harness = X  →  command = X  (skipped when command= already present)
//   - bare command = "claude"|"grok" → full multi-token template
//   - missing role= → role = "reviewer"|"agent" via ResolvedRole
//
// Returns the migrated content and a list of change notes. When nothing is
// stale, out equals content byte-for-byte and changes is empty (idempotent).
// A parse error returns err; callers must leave the on-disk file intact.
func MigrateAgents(content string) (out string, changes []string, err error) {
	ac, err := decodeAgents(content, true)
	if err != nil {
		return "", nil, err
	}

	lines := strings.Split(content, "\n")
	// Enumerate table headers from the TEXT so sectionRange matches real headers
	// (including legacy agents.NAME nested form).
	type sec struct {
		header  string // raw table name as in [header]
		logical string // name for ResolvedRole
		binding AgentBinding
	}
	var sections []sec
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if !strings.HasPrefix(t, "[") || !strings.HasSuffix(t, "]") {
			continue
		}
		// skip array tables [[…]]
		if strings.HasPrefix(t, "[[") {
			continue
		}
		header := strings.TrimSpace(t[1 : len(t)-1])
		logical, binding := bindingForHeader(ac, header)
		sections = append(sections, sec{header: header, logical: logical, binding: binding})
		_ = i
	}

	changed := false
	var notes []string
	harnessN, expandN, roleN := 0, 0, 0

	for _, s := range sections {
		// [defaults] and the retired [models] are not agent bindings — they carry no
		// command/role/principles of their own, so the per-binding migrations
		// below must not touch them (a bare header would otherwise fall
		// through bindingForHeader's default case and get a spurious
		// role="agent" injected).
		if s.header == "defaults" || s.header == "models" || s.header == "model_order" {
			continue
		}
		// A dotted sub-table ([coder.env], [coder.settings], [agents.coder.env])
		// extends its parent binding's table; it is never a binding itself, so
		// no per-binding migration may write into it.
		if !isBindingHeader(s.header) {
			continue
		}
		// harness → command
		if !hasKeyInSection(lines, s.header, "command") {
			if renameKeyInSection(lines, s.header, "harness", "command") {
				changed = true
				harnessN++
			}
		}
		// expand bare claude|grok command values to full templates
		role := ResolvedRole(s.logical, s.binding)
		if expandBareCommandInSection(lines, s.header, role) {
			changed = true
			expandN++
		}
		// add role=
		if !hasKeyInSection(lines, s.header, "role") {
			// UpsertKey works on a joined string; rejoin and re-split after each add.
			joined := strings.Join(lines, "\n")
			joined = UpsertKey(joined, s.header, "role", strconv.Quote(role))
			lines = strings.Split(joined, "\n")
			changed = true
			roleN++
		}
		// inject_principles → principles (when principles= absent)
		if !hasKeyInSection(lines, s.header, "principles") {
			if inj, ok := injectPrinciplesValue(lines, s.header); ok {
				joined := strings.Join(lines, "\n")
				val := `"session"`
				if !inj {
					val = `"none"`
				}
				joined = UpsertKey(joined, s.header, "principles", val)
				lines = strings.Split(joined, "\n")
				// Drop inject_principles line
				removeKeyInSection(lines, s.header, "inject_principles")
				changed = true
				notes = append(notes, "inject_principles->principles")
			}
		}
	}

	// Flatten [agents.NAME] headers → [NAME] (nested dual-read removed).
	flatN := 0
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[agents.") && strings.HasSuffix(t, "]") && !strings.HasPrefix(t, "[[") {
			name := strings.TrimSuffix(strings.TrimPrefix(t, "[agents."), "]")
			indent := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
			lines[i] = indent + "[" + name + "]"
			changed = true
			flatN++
		}
	}

	if !changed {
		return content, nil, nil
	}
	if harnessN > 0 {
		notes = append(notes, "harness->command")
	}
	if expandN > 0 {
		notes = append(notes, "expand bare preset")
	}
	if roleN > 0 {
		notes = append(notes, "added role=")
	}
	if flatN > 0 {
		notes = append(notes, "flatten [agents.NAME]")
	}
	return strings.Join(lines, "\n"), notes, nil
}

// injectPrinciplesValue reads inject_principles from a section if present.
func injectPrinciplesValue(lines []string, header string) (bool, bool) {
	start, end := sectionRange(lines, header)
	if start < 0 {
		return false, false
	}
	for i := start; i < end; i++ {
		ln := strings.TrimSpace(lines[i])
		if strings.HasPrefix(ln, "inject_principles") {
			if strings.Contains(ln, "false") {
				return false, true
			}
			if strings.Contains(ln, "true") {
				return true, true
			}
		}
	}
	return false, false
}

// removeKeyInSection blanks the first assignment of key in section (keeps line
// count stable for subsequent ranges; empty lines are fine).
func removeKeyInSection(lines []string, header, key string) {
	start, end := sectionRange(lines, header)
	if start < 0 {
		return
	}
	for i := start; i < end; i++ {
		if isKeyLine(lines[i], key) {
			lines[i] = ""
			return
		}
	}
}

// decodeAgents is the ONE agents-layer classifier (sty_01949949): every
// top-level table is sorted into `defaults` / `executor` / `reviewer` / a flat
// named binding. LoadAgents, LoadWorkspaceAgents, CheckAgentsTransport and
// MigrateAgents all call it, so a new section type is classified in one place.
//
// legacyNested is the single difference between its callers: MigrateAgents
// reads the retired nested [agents.<name>] container so it can flatten it;
// every live loader ignores that container (the dual-read is retired and
// `satelle init` migrates it), so an un-migrated file cannot silently load
// nested agents.
func decodeAgents(content string, legacyNested bool) (AgentsConfig, error) {
	var raw map[string]toml.Primitive
	md, err := toml.Decode(content, &raw)
	if err != nil {
		return AgentsConfig{}, fmt.Errorf("parse agents.toml: %w", err)
	}
	if err := refuseRemovedBudgetKeys(md); err != nil {
		return AgentsConfig{}, err
	}
	ac := AgentsConfig{Agents: map[string]AgentBinding{}}
	for key, prim := range raw {
		switch key {
		case "defaults":
			if err := md.PrimitiveDecode(prim, &ac.Defaults); err != nil {
				return AgentsConfig{}, fmt.Errorf("parse [defaults]: %w", err)
			}
		case "models":
			// Retired [models] ranking table: ignored so an older repo still
			// loads, and never read as an agent binding.
			continue
		case "model_order":
			if err := md.PrimitiveDecode(prim, &ac.ModelOrder); err != nil {
				return AgentsConfig{}, fmt.Errorf("parse [model_order]: %w", err)
			}
		case "executor":
			if err := refuseRetiredBindingTables(md, key); err != nil {
				return AgentsConfig{}, err
			}
			var b AgentBinding
			if err := md.PrimitiveDecode(prim, &b); err != nil {
				return AgentsConfig{}, fmt.Errorf("parse [executor]: %w", err)
			}
			ac.Executor = b
		case "reviewer":
			if err := refuseRetiredBindingTables(md, key); err != nil {
				return AgentsConfig{}, err
			}
			var b AgentBinding
			if err := md.PrimitiveDecode(prim, &b); err != nil {
				return AgentsConfig{}, fmt.Errorf("parse [reviewer]: %w", err)
			}
			ac.Reviewer = b
		case "agents":
			if !legacyNested {
				continue
			}
			// legacy nested container — migration input only
			var nested map[string]AgentBinding
			if err := md.PrimitiveDecode(prim, &nested); err != nil {
				return AgentsConfig{}, fmt.Errorf("parse [agents]: %w", err)
			}
			for n, b := range nested {
				if err := refuseRetiredBindingTables(md, key, n); err != nil {
					return AgentsConfig{}, err
				}
				ac.Agents[n] = b
			}
		default:
			if err := refuseRetiredBindingTables(md, key); err != nil {
				return AgentsConfig{}, err
			}
			var b AgentBinding
			if err := md.PrimitiveDecode(prim, &b); err != nil {
				return AgentsConfig{}, fmt.Errorf("parse [%s]: %w", key, err)
			}
			ac.Agents[key] = b
		}
	}
	return ac, nil
}

// refuseRetiredBindingTables refuses a binding whose table the file has
// declared a retired sub-table under (`cloud`, `launch`): satelle no longer
// manages cloud identity (sty_c00782a3). binding is the path of the binding
// decodeAgents has just classified — its own classification, never a list of
// seats — so a custom flat binding is covered and [defaults] never reaches it.
func refuseRetiredBindingTables(md toml.MetaData, binding ...string) error {
	for _, k := range md.Keys() {
		if len(k) <= len(binding) || !slices.Equal(k[:len(binding)], binding) {
			continue
		}
		if slices.Contains(globalAgentsRetiredKeys, k[len(binding)]) {
			return retiredTableError(strings.Join(k[:len(binding)+1], "."), ".satelle/workflows/"+AgentsConfigName)
		}
	}
	return nil
}

// refuseRemovedBudgetKeys refuses a context_budget or turn_budget on any
// binding or [defaults] (sty_58a7bdfe), naming the table and the heal. Keys are
// walked in document order, so the first one present is the one named.
func refuseRemovedBudgetKeys(md toml.MetaData) error {
	for _, k := range md.Keys() {
		if n := len(k); n >= 2 && IsRemovedBudgetKey(k[n-1]) {
			return RemovedBudgetKeyError(AgentsConfigName, "["+strings.Join(k[:n-1], ".")+"]", k[n-1])
		}
	}
	return nil
}

// isBindingHeader reports whether a table header names a binding table: an
// undotted name, or the legacy agents.NAME form with an undotted NAME. Any
// other dotted header is a sub-table (env, settings) of a binding — decodeAgents
// reads it as part of its parent, never as a binding of its own.
func isBindingHeader(header string) bool {
	return !strings.Contains(strings.TrimPrefix(header, "agents."), ".")
}

// bindingForHeader maps a table header to the logical agent name + binding.
func bindingForHeader(ac AgentsConfig, header string) (logical string, b AgentBinding) {
	switch {
	case header == "executor":
		return "executor", ac.Executor
	case header == "reviewer":
		return "reviewer", ac.Reviewer
	case strings.HasPrefix(header, "agents."):
		name := strings.TrimPrefix(header, "agents.")
		return name, ac.Agents[name]
	default:
		return header, ac.Agents[header]
	}
}

// hasKeyInSection reports whether section has an uncommented key= assignment.
func hasKeyInSection(lines []string, section, key string) bool {
	start, end := sectionRange(lines, section)
	if start < 0 {
		return false
	}
	for i := start; i < end; i++ {
		if isKeyLine(lines[i], key) {
			return true
		}
	}
	return false
}

// renameKeyInSection rewrites the key token of oldKey → newKey in section,
// preserving indent, equals, RHS, and any trailing comment. Returns true if a
// line was rewritten.
func renameKeyInSection(lines []string, section, oldKey, newKey string) bool {
	start, end := sectionRange(lines, section)
	if start < 0 {
		return false
	}
	for i := start; i < end; i++ {
		if !isKeyLine(lines[i], oldKey) {
			continue
		}
		// Preserve leading whitespace; replace only the key token.
		ln := lines[i]
		indent := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
		rest := strings.TrimSpace(ln)
		// rest starts with oldKey
		after := rest[len(oldKey):]
		lines[i] = indent + newKey + after
		return true
	}
	return false
}

// expandBareCommandInSection rewrites a bare command = "claude"|"grok"
// value to the full multi-token preset template (agentcli.NewRunner). Preserves
// indent and any trailing comment; rewrites only the quoted RHS. role (the
// binding's ResolvedRole) picks the grok template. Idempotent: a multi-token
// value is left untouched. Returns true if a line was rewritten.
func expandBareCommandInSection(lines []string, section, role string) bool {
	start, end := sectionRange(lines, section)
	if start < 0 {
		return false
	}
	for i := start; i < end; i++ {
		if !isKeyLine(lines[i], "command") {
			continue
		}
		ln := lines[i]
		indent := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
		rest := strings.TrimSpace(ln)
		// rest starts with "command"
		afterKey := rest[len("command"):]
		eqIdx := strings.Index(afterKey, "=")
		if eqIdx < 0 {
			return false
		}
		// beforeVal includes the '=' and any spaces after it that we re-emit
		// by reconstructing: key + afterKey[:eqIdx+1] + space + quoted + trailing
		prefix := indent + "command" + afterKey[:eqIdx+1]
		valAndTrail := afterKey[eqIdx+1:]
		// preserve leading spaces after '='
		sp := 0
		for sp < len(valAndTrail) && (valAndTrail[sp] == ' ' || valAndTrail[sp] == '\t') {
			sp++
		}
		spaces := valAndTrail[:sp]
		valPart := valAndTrail[sp:]
		if !strings.HasPrefix(valPart, `"`) {
			return false
		}
		// Find closing unescaped quote.
		endq := 1
		for endq < len(valPart) {
			if valPart[endq] == '"' && valPart[endq-1] != '\\' {
				break
			}
			endq++
		}
		if endq >= len(valPart) {
			return false
		}
		unq, err := strconv.Unquote(valPart[:endq+1])
		if err != nil {
			return false
		}
		fields := strings.Fields(unq)
		if len(fields) != 1 {
			return false // already multi-token (or empty)
		}
		tok := strings.ToLower(fields[0])
		if tok != agentcli.CLIClaude && tok != agentcli.CLIGrok {
			return false
		}
		r, err := agentcli.NewRunner(tok)
		if err != nil {
			return false
		}
		full := r.Command()
		trailing := valPart[endq+1:] // space + comment, if any
		lines[i] = prefix + spaces + strconv.Quote(full) + trailing
		return true
	}
	return false
}
