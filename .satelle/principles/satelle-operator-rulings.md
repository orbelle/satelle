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
  repo: "Does not count ... and the rule holds" (operator, 2026-11-08). Read
  against *Git stays read-only in every registered repository* under
  2026-11-24.

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
  satelle-mechanism-vs-substrate). Read against *Doctor is feedback, not
  edit* under 2026-11-21.
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
  2026-11-13; satelle-mechanism-vs-substrate). Narrowed by *Doctor is
  feedback, not edit* under 2026-11-21: doctor reports and does not escalate
  on its own.
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
  beside it, as a panel member or as a review of its own. The pull-request
  review task is dropped under 2026-11-21 (*No second judge on the same
  diff*). The plan and code reviewers stand as ruled on
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

## 2026-11-21

- **What satelle is: kernel first.** satelle provides "a cli that enables an
  agent to construct stories, with required satelle and user meta data
  (tags), workflows, with skills and gated steps according to user
  requirements, and enable an agent to implement stories, which update the
  repos code, by following the required workflow." "The contents and how the
  substrate works, is the user/agent responsibility." "satelle has no
  responsibility over the users/repos workflow." (operator, 2026-11-21).
- **The kernel runs anything.** "The kernel is NOT in control of
  skill/packs/etc. Kernel is not concerned about the content, only the
  function i.e. must work with satelle", "and that bar is low." The binary
  never refuses to load or run a skill for what it contains (operator,
  2026-11-21).
- **Doctor is feedback, not edit.** Doctor's functions "are assistance to the
  user/agent and hence is (should be) configurable in the substrate ... they
  remain, but fundamentally, offer feedback on the substrate not edit." A
  check a repo wants is configured in its own substrate, and doctor reports;
  the user and the agent decide (operator, 2026-11-21).
- **satelle starts with something.** The init template and the conventional
  tags stay: "satelle has to start somewhere ... an absolute blank satelle
  would do nothing." (operator, 2026-11-21).
- **The edit gate is function.** Once satelle is installed, the repo is
  locked down for the supported agent CLIs: an agent edits it only through a
  seat on a story. That may become an option later; the default is locked
  down. The stop check follows the repo's own workflow configuration
  (operator, 2026-11-21). Read against *The edit gate allows the seated
  target only* under 2026-11-24.
- **Unknown keys are not a blocker.** satelle has a default set of required
  keys and values; "additional keys are not a blocker ... It is however a
  doctor check" (operator, 2026-11-21; sto_9fdace64).
- **Skills are self-contained.** A skill follows the Claude skills practice:
  when it needs a script or asset, the skill is a directory that holds it.
  No script is shared between skills (operator, 2026-11-21; sto_cd3bfa52).
- **Packs are ordinary skills.** A pack installs as skill directories like any
  other skill and ships skills only, never workflows: "the agent installs (in
  the workflow) based upon the current workflow, pack hints and the user
  directions." "a published pack, can do anything." Local edits to an
  installed pack are expected, and satelle does none of the merge on an
  update. "doctor checks for new versions, user implements." The satelle
  workspace owns satelle's published packs. When the route that lands a pack
  has no security reviewer, the agent says so and points to a security
  reviewer pack (operator, 2026-11-21; epic sto_713c7bb3).
- **The orchestrator orchestrates.** "the orchestrator is orchestrating, and
  the coder / planner are just actioning. That appears acceptable, if not
  preferable." The dispatched planner and coder plan and edit; the driving
  session runs the build, the tests and code generation and records what it
  ran on the story (operator, 2026-11-21).
- **No second judge on the same diff.** "Yes, drop the pull request judge, if
  duplicating actions." The satelle app's approval is published from the
  code review the head already passed (operator, 2026-11-21; sto_2cd187fd).
- **Start small, prove and scale.** "As always, start small, prove and scale."
  (operator, 2026-11-21).
- **An item is a story.** Process text names the item a story; no other word
  is used for it (operator, 2026-11-21; sto_8ffb804d).

## 2026-11-22

- **A host app for coding agents gets a host-app adapter.** "amend the
  principles first": satelle may install and keep current its own interface
  inside a host app such as herdr, through a host-app adapter
  ([[satelle-agent-agnostic]], [[satelle-mechanism-vs-substrate]]), rather
  than shipping that interface outside the binary (operator, 2026-11-22;
  sto_4f988424, unblocking sto_ab933beb).
- **Remotes are machine-initiated.** "confirmed, machine-initiated." The
  machine that fires a run opens the connection to the node, gives it its work
  and reads its result; no node calls back to a server. "the remotes are like
  an isloated sub agent that performs a task." "connection should be SSH (or
  similar), or bake a public/private key into satelle, wich can transport and
  install realtime secrets, without storing". "need to always consider satelle
  as the kernal, everthing else is user configuration." (operator, 2026-11-22;
  epic sto_426138e8, [[satelle-agent-operates-remotes]]).
- **The agent or the operator decides when to release.** "The descision on
  releases is defined in many areas, and the operator or the agent makes the
  call. That is the only point to ensure is in the agent context. e.g an epic
  may have 1 or 3 releases depending on the no. of stories or impact of
  releases." (operator, 2026-11-22; [[satelle-agent-goals]]).

## 2026-11-24

- **A remote's secrets come from the repo's untracked .satelle/.env, locally.**
  "secrets is where we get stuck each time. With a local first approach, the
  secrets must be transported fropm the local repo, hence stored (not tracked)
  in the repo. THat is the local approach, later they can come from a supported
  secrets service like infisical, or satelle.dev." (operator, 2026-11-24;
  sto_b01dda27, [[satelle-agent-operates-remotes]]).
- **The edit gate allows the seated target only.** "seats and edit-gate invert
  from cross-repo refusal to seated-target-repo allowlist", and "Item.repo is
  set-once and read by satelle-server for 1:N GitHub bindings." A session
  edits only the worktree of the target repository of the item it is seated
  on; in N=1 that is its own repository, which is the cross-repo fence. The
  principles are amended before any slice is built (operator, 2026-11-24;
  sto_14fdf169, [[satelle-control-plane-architecture]]). A protected
  repository adds its own seat rule: see *A substrate is process scope, never a
  security boundary* under 2026-11-27.
- **Git stays read-only in every registered repository.** The binary reads
  each repository a control repository registers and writes none of them,
  their refs or their remotes, and "satelle stays read-only on git and holds
  no credentials"; a fetch is a step's action or a registry entry's configured
  fetch command (operator, 2026-11-24; sto_24387c93,
  [[satelle-control-plane-architecture]] §2).

## 2026-11-25

- **Published only from main.** "a release is published, and installed,
  only after its commit is merged to main and the merged result has passed
  its tests" (operator, 2026-11-25), after release run exe_000d375e published
  a version from a commit main did not hold. The order is: the approved heads
  are merged into one candidate, the suite runs on that candidate, the
  binaries are built as a draft, main moves to that commit, the draft is
  published, and only then is the build installed. A step that would publish
  or install before main holds the commit is out of order. The steps are the
  release skill's (`.satelle/skills/release/SKILL.md`;
  sto_7d759084, sto_29bb2101).
- **Security review judges the kernel's threat model, nothing more.**
  "Security is very basic, and again the kernel being attacked. Nothing more.
  Still just a kernel." (operator, 2026-11-25), after two security panels
  rejected plans for attacks by a process of the agent's own OS user rewriting
  satelle's runtime. The model is the one `satelle help edit-gate --session
  <session>` states. The security review rubric
  (`.satelle/skills/satelle-security-review/SKILL.md`) judges against it, and
  a finding outside it is noted, not a reason to reject (sto_c675d950).
- **A shipped skill and this repo's copy change in one story.** A delivery
  story may change both copies of a skill the binary ships: the template under
  `internal/kernel/template/` and this repo's copy of the same file under
  `.satelle/`, and no other `.satelle/` path (operator, 2026-11-25), after the
  fallback judge of sto_eef1d36a could not ship with its independence rule
  because a test requires the two copies to be equal
  ([[satelle-split-authored-process]]; sto_aabc85ad).

## 2026-11-27

- **A substrate is process scope, never a security boundary.** This replaces
  the 2026-11-24 ruling that one control repository is one trust domain, which
  was recorded only in sto_5afbd604. "one substrate repository,
  satelle-substrate, governs satelle, satelle-server and satelle-server-infra,
  whose existing substrate is merged in as its repo-specific part." "Each
  registered repository has a tier (standard or protected). satelle enforces a
  protected tier at level 1: no dispatched write grant, a seat only for a
  session on an item that targets it, changes only through a pull request the
  operator approves. Level 0 (identity, credentials, host, sandbox) is
  provisioned by the operator outside satelle and needs no substrate change."
  "Every agent may read every repository's ledger: awareness across
  repositories is intended." "satelle-substrate starts on a clean ledger;
  importing the old one is a later, optional step." (operator, 2026-11-27;
  epic sto_5afbd604, [[satelle-control-plane-architecture]] §1).
- **The control-plane repository is named satelle-factory.** "a better name,
  or more industry aligned name is satelle-factory" (operator, 2026-11-27): it
  replaces satelle-substrate in the ruling above; the stories of epics
  sto_5afbd604 and sto_540e3431 name it so.
- **The edit gate is a level-1 guard against honest mistakes.** It reads git as
  the command word of a segment, after an optional literal cd, in a line of
  plain literal words and the operators &&, ||, ; and |; any other token makes
  the line unread, and an unread line that would change a tree or push is
  refused. Git run by another program, git options or configuration that run
  programs, files a git command writes and a later command runs, and glob
  spellings of git are out of its scope: deliberate evasion is answered at
  level 0 ([[satelle-control-plane-architecture]]), and the security judges
  judge sto_1efcc7eb and sto_da6dddb9 against this scope (operator,
  2026-11-27).

## 2026-11-28

- **A done substrate story lands on a moved main when main never touched its
  files.** When every file the story changes is unchanged on main since its
  merge base, the lander merges origin/main into the story's branch, re-runs
  satelle validate on the merged tree, and pushes the merge to main as a
  fast-forward. When main changed any of the story's files, the landing stops
  and goes to the operator as before. The rule is interim: it bridges until
  the substrate lives in satelle-factory, apart from the product repositories,
  and retires with that move. `.satelle/skills/substrate.md` step 6 carries
  the procedure (operator, 2026-11-28, sto_28f13c51; after sto_b592fad9,
  sto_fe08d97e and sto_acd247f2 each stopped at landing because main had
  moved).
