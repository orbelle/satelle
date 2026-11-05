package cli

// sty_ddbe2669 AC4: a linked worktree outside the main tree carries its own
// satelle.toml, and that copy is not the process of record. After chdir into
// the worktree, the hook entry points and app.Open apply the main tree's
// engagement, gate, vars, exemptions, no-implement rule, and substrate lock.
// Nothing here is handed a process Config built by the test.

const mainProcessToml = `[engagement]
parallel = "epic"

[gate]
allow_outside_tree_edits = false
edit_exempt_paths = ["kept/"]
edit_exempt_globs = ["*.keep"]
no_implement_models = ["main-blocked*"]
no_implement_message = "main model rule"
command_allow = { push = ["release"] }

[vars]
PUBLIC = "from-committed"
`

const mainAgentsToml = `[reviewer]
role = "reviewer"
model = "main-overlay-mark"
env = { MARKER = "${PROCESS_MARK}" }
`

const decoyRouteDone = `[meta]
name = "done"
type = "workflow"
scope = "project"
description = "feature lane"

[feature]
obligations = ["raised", "planned"]
`

const decoyRouteStep = `[meta]
name = "step"
type = "workflow"
scope = "project"
description = "plan gate on the reviewer binding"

[raised]
status = "backlog"
start = true

[planned]
status = "plan"
reviewers = ["intent"]
reviewer_agent = "reviewer"
requires = ["raised"]
`

const decoyProcessToml = `data_dir = "decoy/process"

[substrate_roots]
workflows = "decoy-wf"
skills = "decoy-skills"
principles = "decoy-principles"
documents = "decoy-documents"

[engagement]
parallel = "none"

[review]
gate_create = true

[gate]
allow_outside_tree_edits = true
edit_exempt_paths = ["decoy-kept/"]
edit_exempt_globs = ["*.decoy"]
no_implement_models = ["decoy-blocked*"]
no_implement_message = "decoy model rule"
lock_substrate_paths = []
command_allow = { push = ["never"] }

[vars]
PROCESS_MARK = "decoy-overlay-value"
PUBLIC = "decoy-committed"
`

const decoyAgentsToml = `[reviewer]
role = "reviewer"
model = "decoy-agent-model"
`
