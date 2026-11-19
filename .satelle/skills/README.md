# skills

Each `*.md` file here is a skill. A `type: skill` file is the rubric the agent
a workflow binds follows; a `type: check` file's first `sh` or `check` fenced
block is the script a gate runs.

Frontmatter is `name:`, `type:` and `description:`, plus `after:`, `framing:`
and `commands:` where a skill uses them; `satelle help configuration --session <session>`
says what each means.

`satelle validate --session <session>` lists a skill that no workflow, doctor
check or `[hook] inject` entry names. This README and the files under
`testdata/` are not skills.
