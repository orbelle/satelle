// Package substratereview gathers what `satelle substrate review` puts in front
// of its judge and renders what comes back: every substrate item with the
// origin satelle resolves for it, the story history that is evidence for it,
// and every constitution rule. It decides nothing — the disposition of an item
// and the finding against a rule are the dispatched reviewer's judgement
// (satelle-mechanism-vs-substrate); this package only enumerates and renders.
package substratereview

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/substrate"
	"github.com/bobmcallan/satelle/internal/wfroute"
)

// Origins, as satelle resolves them.
const (
	// OriginEmbedded: the binary's default, with no file on disk.
	OriginEmbedded = "embedded"
	// OriginSeeded: an on-disk copy of an embedded default, unedited.
	OriginSeeded = "seeded"
	// OriginEdited: an on-disk copy of an embedded default, changed.
	OriginEdited = "edited"
	// OriginRepo: authored in this repo, with no embedded counterpart.
	OriginRepo = "repo"
	// OriginTeam: resolved from the project tier, which owns the path.
	OriginTeam = "team"
	// OriginLocal: this machine's override (agents.local.toml, or the
	// machine-wide profile catalog).
	OriginLocal = "local"
)

// Item kinds.
const (
	KindPrinciple = "principle"
	KindSkill     = "skill"
	KindStep      = "step"
	KindGate      = "gate"
	KindAgent     = "agent"
	KindTask      = "task"
)

// Areas, in the order the review judges them: each is its own dispatch.
const (
	AreaPrinciples = "principles"
	AreaSkills     = "skills"
	AreaWorkflows  = "workflows"
	AreaAgents     = "agents"
	AreaTasks      = "tasks"
)

// Areas lists every area in judging order.
var Areas = []string{AreaPrinciples, AreaSkills, AreaWorkflows, AreaAgents, AreaTasks}

// AreaOf is the area an item of kind is judged in.
func AreaOf(kind string) string {
	switch kind {
	case KindPrinciple:
		return AreaPrinciples
	case KindSkill:
		return AreaSkills
	case KindStep, KindGate:
		return AreaWorkflows
	case KindAgent:
		return AreaAgents
	case KindTask:
		return AreaTasks
	}
	return kind
}

// Item is one substrate item under review.
type Item struct {
	// Key names the item: principles/<name>, skills/<name>,
	// workflows/<workflow>/<state>[/<gate>], agents/<seat>. --apply takes it.
	Key    string `json:"item"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Origin string `json:"origin"`
	// Source is where the item resolves from: a path, embedded:<…>, or the
	// agents layer file.
	Source string `json:"source"`
	// Seats are the agents.toml seats that run the item (a gate's reviewer or
	// panel, a step's performer, the agent itself).
	Seats    []string `json:"seats,omitempty"`
	Selector Selector `json:"-"`
}

// Agent is one effective agents.toml seat.
type Agent struct {
	Name string
	// Model is the model the binding names now ("" when it names none).
	Model string
	// Source is the Provenance label of the binding's fields: local, repo,
	// workspace, baseline, embedded, profile:<p>, global-role:<p> or
	// global-seat:<p>. A field set
	// by agents.local.toml makes the whole seat local.
	Source string
}

// InventoryInput is what the CLI resolves from the repo.
type InventoryInput struct {
	// Docs are the effective process documents (substrate.List).
	Docs []substrate.Row
	// TierRoot is the project tier root; a doc resolved from under it is team.
	// Empty with no tier.
	TierRoot string
	// Workflow names the routes' workflow and Routes are the derived routes,
	// one per category; a step or gate claimed by several categories is one
	// item.
	Workflow string
	Routes   []wfroute.Route
	// AgentsTeam is true when the project tier owns agents.toml.
	AgentsTeam bool
	Agents     []Agent
	// Tasks are the effective task documents, classified by
	// substrate.ClassifyTask.
	Tasks []substrate.Row
}

// OriginOf resolves a document's origin.
func OriginOf(r substrate.Row, tierRoot string) string {
	if tierRoot != "" && r.Source != "" && strings.HasPrefix(filepath.Clean(r.Source), filepath.Clean(tierRoot)+string(filepath.Separator)) {
		return OriginTeam
	}
	switch r.Provenance {
	case "default":
		if r.Embedded || strings.HasPrefix(r.Source, "embedded:") || r.Source == "" {
			return OriginEmbedded
		}
		return OriginSeeded
	case "edited":
		return OriginEdited
	}
	return OriginRepo
}

// agentOrigin maps a binding's Provenance label to an origin.
func agentOrigin(source string, team bool) string {
	switch {
	case strings.Contains(source, config.SourceLocal), config.IsCatalogSource(source):
		return OriginLocal
	case strings.Contains(source, config.SourceRepo):
		if team {
			return OriginTeam
		}
		return OriginRepo
	case strings.Contains(source, config.SourceWorkspace):
		return OriginTeam
	}
	return OriginEmbedded
}

// Inventory lists every substrate item: each principle, skill and task, each
// workflow step and gate of the derived routes, and each agent seat, sorted by
// key.
func Inventory(in InventoryInput) []Item {
	var items []Item
	routeOrigin, routeSource := OriginEmbedded, ""
	for _, d := range in.Docs {
		switch d.Kind {
		case "principles", "skills":
			kind := KindPrinciple
			sel := Selector{}
			if d.Kind == "skills" {
				kind, sel = KindSkill, Selector{Skill: d.Name}
			}
			items = append(items, Item{Key: d.Kind + "/" + d.Name, Kind: kind, Name: d.Name,
				Origin: OriginOf(d, in.TierRoot), Source: d.Source, Selector: sel})
		case "workflows":
			// The route's steps and gates are the items; they take the step
			// catalogue's origin, which is where they are authored.
			if d.Name == "step" {
				routeOrigin, routeSource = OriginOf(d, in.TierRoot), d.Source
			}
		}
	}
	seen := map[string]int{}
	add := func(it Item) {
		if n, ok := seen[it.Key]; ok {
			for _, s := range it.Seats {
				items[n].Seats = appendUnique(items[n].Seats, s)
			}
			return
		}
		seen[it.Key] = len(items)
		items = append(items, it)
	}
	wf := in.Workflow
	for _, r := range in.Routes {
		if r.Workflow != "" {
			wf = r.Workflow
		}
		for _, st := range r.Steps {
			step := Item{Key: "workflows/" + wf + "/" + st.Status, Kind: KindStep, Name: st.Status,
				Origin: routeOrigin, Source: routeSource, Selector: Selector{State: st.Status}}
			if st.Agent != "" {
				step.Seats = []string{st.Agent}
			}
			add(step)
			for _, g := range append(append([]wfroute.Reviewer{}, st.Reviewers...), st.Skipped...) {
				seats := g.Panel
				if len(seats) == 0 {
					seats = []string{"reviewer"}
					if g.Agent != "" {
						seats = []string{g.Agent}
					}
				}
				add(Item{Key: step.Key + "/" + g.Skill, Kind: KindGate, Name: g.Skill, Origin: routeOrigin,
					Source: routeSource, Seats: seats, Selector: Selector{State: st.Status, Skill: g.Skill}})
			}
		}
		for _, x := range r.Exits {
			for _, g := range x.Gates {
				add(Item{Key: "workflows/" + wf + "/" + x.Status + "/" + g, Kind: KindGate, Name: g, Origin: routeOrigin,
					Source: routeSource, Seats: []string{"reviewer"}, Selector: Selector{State: x.Status, Skill: g}})
			}
		}
	}
	for _, t := range in.Tasks {
		items = append(items, Item{Key: "tasks/" + t.Name, Kind: KindTask, Name: t.Name,
			Origin: OriginOf(t, in.TierRoot), Source: t.Source})
	}
	for _, a := range in.Agents {
		items = append(items, Item{Key: "agents/" + a.Name, Kind: KindAgent, Name: a.Name,
			Origin: agentOrigin(a.Source, in.AgentsTeam), Source: config.SourceFile(a.Source),
			Seats: []string{a.Name}, Selector: Selector{Agent: a.Name}})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}

// ParkStates are the park exits of the routes: the statuses a reject-budget
// park enters.
func ParkStates(routes []wfroute.Route) []string {
	var out []string
	for _, r := range routes {
		for _, x := range r.Exits {
			if x.Park {
				out = appendUnique(out, x.Status)
			}
		}
	}
	return out
}
