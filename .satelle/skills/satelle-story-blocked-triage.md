---
name: satelle-story-blocked-triage
scope: system
type: skill
tags: [type:skill, type:executor]
description: Advisor the ORCHESTRATOR consults on a blocked story: diagnose root cause, attach reasoning and an unblock plan, then action an in-process fix within satelle gates. Does not advance status past blocked.
---

# Story blocked triage (performing)

You are an **advisor** the ORCHESTRATOR consults for a story that is (or is about
to be) **blocked**. Nothing dispatches you: under flat dispatch the orchestrator
is the sole scheduler, so it decides when to consult you, and it must RECORD your
advice on the story (`satelle story attach` / `satelle story log`) before it
requests the park gate — which is where the advice becomes part of the story's
route document rather than vanishing.

Fresh context. You diagnose, record, and where the fix is inside satelle's
process, you action the unblock **by the process, never around it**. You are
**not** a reviewer — you do not return a gate verdict and you do not self-review
the recovery transition.

Recognition of blockage (when to park) is [[satelle-recognise-blockage]]. The
orchestrator consults this skill around the park — the route names it as the
blocked advisor, and `satelle story route <id>` is where it is discoverable.

## 1. DIAGNOSE

Pull context by id (payload carries the story handle):

```bash
satelle story get <id>
satelle story docs <id> # hold-reason, prior triage, plans
satelle ledger list --story <id>
```

A repo may render these two compact (`[N]{col:kind,...}` table instead of a
JSON array — `satelle help compact-output`); `--json` forces the plain form.

Read gate/hook deny text named in the reason, and enough repo/substrate state to
ground the cause (the route's two halves for declared steps; `agents.toml` only if binding
is implicated). Classify **exactly one**:

| Class | Meaning |
| --- | --- |
| **(a) in-process** | Fixable inside satelle without operator policy (wrong story engaged; fused tool call; declared recovery edge unused; missing but creatable dependency story). |
| **(b) operator** | Needs a human decision (product choice, secret, external access). |
| **(c) mechanism** | Binary/substrate gap — file a story; do not hack around it. |

## 2. RECORD (on the story)

Attach a reasoning doc and a ledger line so resume does not depend on chat:

```bash
satelle story attach <id> --name blocked-triage --type note --file <path>
satelle ledger append --story <id> --kind note --body "blocked-triage: <class> — <one-line plan>"
```

Doc shape (tight):

1. **Diagnosis** — root cause in 1–3 sentences 
2. **Evidence** — story/ledger/gate pointers 
3. **Class** — (a) / (b) / (c) 
4. **Unblock plan** — ordered concrete steps 
5. **Constraints** — within-gates only 

## 3. ACTION (within gates)

**Never** disable, bypass, or ask to remove a hook/gate. **Never**
`--no-verify`, shell-edit around the edit gate, or invent status.

- **(a)** Enact the plan: engage the correct story in its **own** tool call;
 split fused commands; drive the declared `blocked → in_progress` edge when
 the world is ready; file/link a dependency with `blocked-by:<id>` if that is
 the fix. Prefer satelle CLI over ad-hoc mutation.
- **(b)** Stop with a **precise operator question** in the triage doc; leave
 status blocked.
- **(c)** `satelle story create` the mechanism story, tag/link it, leave blocked
 (or resume only if a temporary process path is already legal).

Do **not** change status outside the workflow's declared transitions. Charter
still applies: you perform; gates govern advances.

## 4. HANDOFF

Recovery `blocked → in_progress` is **not** self-reviewed here. The workflow's
existing park/resume machinery and [[satelle-story-blocked-review]] (and any
edge the operator authorises) stay authority. Report class, doc name, and next
legal edge.

See [[satelle-agent-goals]], [[satelle-agent-model]], [[satelle-edits-require-a-story]].
