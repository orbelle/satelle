package config

// dispatchPointersRules are the phrases each skill must carry together to apply
// satelle-dispatch-pointers (sty_73a2dc68 AC2): the plan skill makes the planner
// name what each dispatch's receiver reads for itself; the two reviews reject a
// plan or slice that embeds readable content in a dispatch, whatever its size.
var dispatchPointersRules = map[string][]string{
	"plan": {
		"[[satelle-dispatch-pointers]]",
		"For every dispatch the plan designs",
		"what it reads for itself",
		"never content it can fetch",
	},
	"satelle-story-plan-review": {
		"[[satelle-dispatch-pointers]]",
		"for every dispatch the plan designs",
		"what the receiving seat reads for itself",
		"embeds readable content in a dispatch",
		"is rejected whatever its size",
	},
	"satelle-code-ac-review": {
		"[[satelle-dispatch-pointers]]",
		"embeds readable content in a dispatch",
		"is rejected whatever its size",
		"a dispatch embeds readable content",
	},
}
