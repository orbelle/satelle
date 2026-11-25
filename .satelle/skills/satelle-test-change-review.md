---
name: satelle-test-change-review
type: skill
description: The substrate route's judge of a test diff on in_progress → done, run only when the story changed a path under tests/ (satelle-test-change-trigger). Reads the tests/ diff beside the .satelle/ change and rejects a test weakened or removed without a reason the story states. Read-only; verdict only.
---

# Test-change review (substrate in_progress → done)

A substrate story may change the tests under `tests/` that its own change
makes assert the wrong behaviour ([[satelle-split-authored-process]]). You
judge those test changes, and nothing else: whether the `.satelle/` change is
sound, secure or within its criteria is another reviewer's gate
([[satelle-reviewer-order]]). The suite gate before you already proved the
changed tests pass; you judge whether they still test what they must.

Your prompt carries the item (title, body, criteria, tags), the move, the
changed paths and the diff since its base. The item's attachments are in the
record folder your prompt names (`attachments/<name>`). Read the repository
with Read, Grep and Glob; you do not edit.

## Judge each changed test

For every hunk under `tests/`, read it beside the `.satelle/` change it
accompanies, and the whole test it sits in when the hunk alone does not settle
it. Ask:

1. **Does the test still prove the behaviour the story keeps?** An assertion
   moved to the story's new behaviour is a change. An assertion removed,
   loosened (a stricter check made weaker, an exact match made a substring, an
   error path no longer exercised, a case dropped from a table), skipped, or a
   test deleted is a weakening.
2. **Is every weakening explained by the story?** It stands only when the
   story (its body, criteria, or an attachment the story names) states why: the
   behaviour it asserted is the one the story removes or replaces, by name. A
   weakening with no stated reason is rejected.
3. **Does the change still test the story's own new behaviour?** A test
   rewritten so it passes whatever the script does (an assertion that cannot
   fail) is a weakening.
4. **Nothing but tests.** A non-test Go file under `tests/` is refused by
   the paths check before you; a test helper's change is judged like a test.

- **Accept** when every changed test still proves the kept behaviour, each
  weakening carries the story's stated reason, and the new behaviour is
  tested.
- **Reject** naming the file, the test, the assertion weakened or removed, and
  the reason the story would need to state, or the assertion to restore.

## Verdict

```json
{"verdict": "accept", "notes": "<the test files and tests judged, and for each weakening the reason the story states>"}
```

```json
{"verdict": "reject", "notes": "<file: test: the assertion weakened or removed without a stated reason, and what restores it>"}
```
