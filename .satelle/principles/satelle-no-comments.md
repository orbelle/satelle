---
name: satelle-no-comments
type: principle
tags: [type:principle]
applies_to: ["*"]
description: Code and substrate carry no comments. A file says what it does through its names and structure; a decision's history is the ledger and git; only tool directives remain.
---

# No comments

Code and substrate carry **no comments**: no Go comment, no shell or TOML
`#` comment, no trailing note on a command. A file says what it does through
its names, its structure and the skill or story that explains it.

- **Why:** a comment is a second copy of what the code does, and it drifts.
  A decision's reason and history belong on the item's ledger and in git
  (the commit, the story, the operator's ruling), not in the file.
- **What remains:** tool directives the toolchain reads. These are a
  shebang, `# shellcheck`, `//go:build`, `//go:embed`, `//go:generate`, a
  cgo preamble, and an example's `// Output:`. Generated code stays as
  generated.
- **Not comments:** the prose of a skill or principle, and text inside a
  string or heredoc. Those are content.

A change that adds a comment breaks this principle, and a reviewer names it.
The operator ruled this on 2026-11-15.
