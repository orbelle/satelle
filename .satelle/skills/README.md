# skills

Each `<name>.md` file or `<name>/SKILL.md` directory here is a skill. A
`type: skill` file is the rubric the agent a workflow binds follows; a
`type: check` file's first `sh` or `check` fenced block is the script a gate
runs. A directory skill holds its own `scripts/` and `assets/` beside
`SKILL.md`, is self-contained, and may not share scripts with another skill.

Frontmatter is `name:`, `type:` and `description:`, plus `after:`, `framing:`
and `commands:` where a skill uses them; `satelle help configuration --session <session>`
says what each means.

`satelle validate --session <session>` refuses duplicate skills defined at both
`<name>.md` and `<name>/SKILL.md`, missing `scripts/` or `assets/` paths in a
skill directory, and paths outside that skill's own directory, and lists any
skill that no workflow, doctor check, `[hook] inject` entry or `pack.toml`
names. This
README, subdirectories without `SKILL.md` and the files under `testdata/` are
not skills.
