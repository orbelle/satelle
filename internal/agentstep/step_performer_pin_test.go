package agentstep

import (
	"testing"

	"github.com/bobmcallan/satelle/internal/config"
	"github.com/bobmcallan/satelle/internal/workitem"
)

// pinEngine is an engine over a route with a named-binding step ("named") and an
// executor-allocated one ("exec"), both declaring placement_tag, so a child's pin
// reaches them. The [executor] binding is the given command.
func pinEngine(t *testing.T, namedCommand, executorCommand string) *Engine {
	t.Helper()
	wf := spineWF("", "", "",
		`named|architect|rubric||||placement_tag = "placement:"`,
		`exec|executor|rubric||||placement_tag = "placement:"`,
		"unpinnable|architect|rubric",
		"done")
	g, _ := newEngine(t, "ok", fakeDocs{workflow: wf, skillBody: "rubric", skillFound: true})
	g.SetNamedAgents(func(name string) (config.AgentBinding, bool) {
		if name == "architect" {
			return config.AgentBinding{Command: namedCommand, Tools: "Read"}, true
		}
		return config.AgentBinding{}, false
	})
	g.SetOneShotAgents(func(name string) (config.AgentBinding, bool) {
		if name == "executor" {
			return config.AgentBinding{Command: executorCommand}, true
		}
		return config.AgentBinding{}, false
	})
	return g
}

func pinned(tags ...string) workitem.Item {
	return workitem.Item{ID: "sty_child", Status: "backlog", Tags: tags}
}
