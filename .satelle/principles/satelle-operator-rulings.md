---
name: satelle-operator-rulings
type: principle
applies_to: ["*"]
description: The operator's standing rulings for satelle v1. Every session and dispatched agent works to them; change them only through a reviewed operator directive.
---

# Operator rulings (satelle v1)

Recorded 2026-11-06; later rulings sit under the date they were made. These hold
until the operator changes them; a change is itself reviewed (see *Directives
are reviewed*).

- **Process lives in configuration, never in Go.** The binary is a stateless,
  generic kernel: items, workflows, gates as skills, agents with grants, a
  hash-chained ledger. A plan or diff that puts a step, state, skill, category
  or tag decision into Go is rejected, with no exemption (binary-scope gate,
  first and fail-fast).
- **Quality over speed.** A useful reviewer beats a cheap one, within limits.
  Reviewer cycles and their results are the main quality signal and can also be
  the main blocker: record them, compare judges, and change a judge on evidence.
- **Attachments are the record.** Plans, notes and evidence live as attachments
  in the item's ledger. Agents read them from the item when they need them; they
  sync to satelle-server and travel to remotes with the item.
- **Directives are reviewed.** The operator's own directives that change
  process, configuration, scope or a ruling are judged against these rulings and
  the constitution; push back with reasons where they conflict. Questions and
  status checks are not.
- **Local first.** satelle runs solo and local; team (with satelle.dev) is the
  mode on top. A team is a team of one or many; both work alike for now, and a
  multi-user team will need its own requirements and code when wanted.
- **Every interface is gRPC unless HTTP is absolutely required.** satelle ↔
  satelle-server and the CLI ↔ satelled alike. HTTP stays only for browser
  pages and assets, OAuth sign-in, /healthz, MCP (streamable HTTP by spec) and
  webhook delivery.
- **Apply the rulings; don't ask what they already answer.** When a question is
  settled by these rulings (no redundant code, align to the new direction, gRPC,
  breaking changes pushed), decide it and record the decision. Raise only what
  the rulings leave open.
- **Steps may run in the cloud; landing is local.** A step can run in a
  claude.ai/code session to take load off the machine and run stories in
  parallel; its work comes back as a pushed branch to the local gates. Merge,
  test and push are always local.
- **Development splits in two: DEV, then CI/CD.** DEV takes a story from the
  backlog to an approved pull request; CI/CD is a task over approved items that
  merges, tests and releases. The agent merges and releases. There are two
  environments, dev (local) and prod; there is no staging, so local testing is
  the staging and matters. The session that took the story to approved then
  fires the release queue (`satelle queue fire release --session <session>`)
  and drives the run until its items are done; approved is not the end of its
  drive.
- **GitHub holds the code and shows gate status; it runs no Linux tests.** Tests run
  locally first and then on sprites as part of CI/CD; their results are
  published to GitHub as commit statuses by a skill. Restated on 2026-11-19:
  see *macOS and Windows tests run on GitHub, on demand* under that date. A satelle GitHub app (alternative PR reviewer/approver) comes
  through satelle-server to team subscriptions.
- **Breaking changes are pushed; old versions are defunct.** v1 is a clean
  rewrite, in satelle and satelle-server alike: no backward compatibility in the
  wire (no REST beside gRPC), storage or configuration. The users are the
  operator and a few testers; satelle provides the way across (`satelle
  upgrade --session <session>`), client and server ship together, and the previous install is
  backed up before a swap.
- **A clean, efficient codebase.** No redundant artifacts, functions, files,
  skills, scripts or tests: what nothing reaches is deleted, not kept "in
  case". The one exception is the story ledger of the operator's other repos,
  which `satelle upgrade --session <session>` migrates into v1; satelle, satelle-server,
  satelle-delivery and satelle-server-infra start clean (their old backlog is
  irrelevant to the rewrite; satelle-server-infra is migrated by the
  satelle-server session with a backlog of 0).
- **satelle-server is satelle's primary tester.** Every satelle release is
  handed to the satelle-server session to exercise in its real work; what it
  finds (command, status, message, version) goes to the front of satelle's
  order.
- **Dogfood.** satelle is built with satelle: every change is a story driven
  through this repo's workflow on the rebuild branch, released locally, then
  used.
- **Order is a tag.** A container's children carry `order:N`; the operator or
  an agent resuming takes the lowest open one unless told otherwise.
- **Never work around a block.** A gate, hook or fence that refuses is either
  right or a gap; raise a story for the gap and record any manual step taken.

## 2026-11-07

- **Testing, not coding, is the bottleneck.** Effort and review go to proving a
  change: tests, gates and evidence. Writing code is the cheap part
  (operator, 2026-11-07).
- **The outer loop.** The human is involved at the start (the story and its
  criteria) and at confirming the result; the agent drives everything in
  between through the gates (operator, 2026-11-07).
- **Only approved stories are released.** A release takes items at `approved`,
  and nothing short of it ships (operator, 2026-11-07).
- **satelle-cicd is reference only.** `.satelle/remotes/satelle-cicd` is the
  pre-1.0 remote kept for reference; nothing dispatches it, and its text is not
  part of the process (operator, 2026-11-07).
- **The user-facing GitHub app is satelle (5379260); orbelle-reviewer is
  internal** (operator, 2026-11-07).

## 2026-11-08

- **Doctor is read-only on git.** "doctor should not perform actions in/on git.
  git is readonly for doctor, any further action the agent/user should be
  advised." Doctor reads the repo and reports; any change it finds needed is
  advice to the agent or the user, never an action it takes (operator,
  2026-11-08).
- **The binary is read-only on the user's repo, its refs and its remotes.**
  Git it runs in a throwaway scratch repo it creates for one run (a remote
  node's workspace; the docker check gate's node) is not a git action on the
  repo: "Does not count ... and the rule holds" (operator, 2026-11-08).

## 2026-11-13

- **At least one judge of another family.** Restated on 2026-11-17: see
  *Never reviewed solely by the author's family* under that date.
- **satelle manages itself.** "satelle should self manage with exceptions
  posted to the user." What satelle can prove it fixes unattended (the sync
  timer's pass runs doctor's answer-free fixes); only what it cannot fix
  reaches the operator (operator, 2026-11-13; satelle-sync-architecture §4).
- **Blocked, not held.** "'Held' status should be removed. It is not common
  language. 'Blocked' is correct and provides there is reason, and unblocking
  is required." A capped plan goes to blocked with its reason and the command
  that unblocks it (operator, 2026-11-13).
- **A blocked plan is a positive outcome.** "When the plan is blocked, that is
  actually a positive outcome, as many times, the plan was passed. The
  proposed stories are not always in the right direction, or achievable." A
  plan that cannot pass review stops a story before any code is written; it is
  reported, not worked around (operator, 2026-11-13).
- **satelle ships its system processes.** "there are reasons that satelle
  implements 'system' processes, satelle maintenance and substrate alignment,
  ensuring agents understand how to use satelle and optimal skill rules (eg.
  reference over content)." The init template's substrate is shipped into each
  repo and overridable there (operator, 2026-11-13;
  satelle-mechanism-vs-substrate).
- **All panel members must agree, for now.** "Panel - lets maintain all must
  agree (for now) but ... open up the discussion." Panels keep combine = "all";
  consultation inside the reviewer circle before the cold gate is the
  discussion (operator, 2026-11-13).
- **Every review has the plan's round cap.** "all review processes should have
  the same process as planner." Code and PR review block after the same number
  of rejected rounds in a row as plan review (operator, 2026-11-13).
- **The repo's workflow resolves issues in tracked content.** "If satelle finds
  issues, the repo workflow/path to resolve is used. eg. .satelle is git
  tracked, hence that is an update to the repo. The repo is protected by the
  users method, hence satelle should advise the user (warning) and ask how to
  resolve. If not resolved the warning will turn into an error." (operator,
  2026-11-13; satelle-mechanism-vs-substrate).
- **A mirror-only queue chain adopts the store's.** Asked whether the sync
  principle or the queue adoption gives, the operator amended the principle
  (operator, 2026-11-13; satelle-sync-architecture §4).
- **Only team-workspace bindings are tracked.** A repo's satelle.dev binding is
  committed only when the project is a team workspace's; a personal binding
  stays on the machine (operator, 2026-11-13; sto_9931cd73).

## 2026-11-14

- **A session drives its items to a terminal state.** "The agent drives the
  satelle story to done (terminal)... The agent should not stall, at any
  point. Watchers should only occur if awaiting an external party, like user
  PR approval." A min-size-0 queue is fired by a session, at any time: "that
  is NOT correct, can fire any time." The session whose story reaches
  approved fires the release queue
  (`satelle queue fire release --session <session>`) and drives the run to
  done; a turn may end short of a terminal state only at a park state or a
  state that declares `waits`; otherwise the Stop hook blocks the stop once,
  naming the next move, and lets the stop straight after through, so a
  session is never trapped (operator, 2026-11-14; sto_2ac90ab6 AC2).

## 2026-11-15

- **Default colours for conventional tags are acceptable in code.** "a
  default color coding on default tags (likely) is acceptable in code. Likely
  tags: priority, surface, reporter, source. Note: the agent should have these
  in the context, and guide the user." This is the one exception to the tag
  half of *Process lives in configuration, never in Go*: the binary may name
  those four tag keys and give them default chip colours, which a repo's
  `.satelle/ui.toml` overrides; no other tag may be named in Go
  (operator, 2026-11-15; sto_856572ef; [[satelle-conventional-tags]]).

## 2026-11-17

- **Never reviewed solely by the author's family.** "a step cannot be
  reviewed solely by the same model family" and "the pr-reviewer does NOT have
  to be an alt. model. It does however need to be a cold review. eg. claude
  -p" (operator, 2026-11-17). Each step of the work (its plan, its code, its
  pull request) has at least one review by a judge of another model family
  than its author. A cold judge of the author's family may review the step
  beside it, as a panel member or as a review of its own, such as the
  pull-request review task. The plan and code reviewers stand as ruled on
  2026-11-13: "the plan reviewer(s) and code reviewer(s) there should be at
  least 1 non-matching llm. eg. claude vs gpt vs grok models." and "Can be
  more than 1 reviewer, however, at least 1 is a different model." (operator,
  2026-11-13 and 2026-11-17; satelle-agent-alt-reviewer).

## 2026-11-19

- **macOS and Windows tests run on GitHub, on demand.** "The current practice
  is to action integration tests locally, and hence as I'm linux, github is
  the solution, just need to manage the time/cost for the runners." (operator,
  2026-11-19). The operator's machines are Linux, so a change that must hold
  on darwin or windows is tested on GitHub's macos-latest and windows-latest
  runners (`.github/workflows/os-test.yml`), only when asked: the label
  `os-test` on a pull request, or a dispatch. Linux tests stay local and on
  remotes. Runner time is kept down by the on-demand trigger, one run per ref,
  bounded job time and caching (sto_6d13f4f9).

## 2026-11-20

- **Rulings on the drift findings of sto_8fc5edb1.** "drop the bindSync auto-sync,
  keep the other two" (operator, 2026-11-20):
  - `satelle project bind` does not sync: [[satelle-sync-architecture]] §4
    holds, and the sync timer's pass is the one owner of convergence
    (sto_c2905da7).
  - Doctor composing the `satelle story create --session <session>` command that raises a
    substrate story for the shipped files a repo lacks
    (`substrateStoryCommand`, internal/cli/substrate_report.go) is accepted:
    it is advice text the user may run or not, not a process step in Go.
  - A kernel test naming a skill `satelle init --session <session>` ships
    (`TestInitShipsTheUpgradeSkill`, internal/kernel/kernel_test.go) is
    accepted: a test proving what init ships names it.
- **One concern, one reviewer; reviewers in priority order.** "A security
  reviewer as part of the plan and code is required." "adding a new reviewer
  with specific scope/skill is preferred over just adding more requirements to
  a single reviewer." "The sequential processing is valid, in a fail fast
  process, that also indicates a priority order. I'm happy for that to remain,
  but expand to the agent ordering the reviewers accordingly." (operator,
  2026-11-20). A new review concern is a new gate with its own skill and
  judge, never more requirements on another reviewer's skill. Each reviewer
  judges cold and isolated. A transition's reviewers run in sequence and fail
  fast; the sequence is a priority order, and an agent adding or moving a
  reviewer places it by that order (sto_70f455db). The session coordinates
  the verdicts.
