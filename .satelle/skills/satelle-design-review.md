---
name: satelle-design-review
scope: project
type: skill
tags: [type:skill, type:reviewer]
description: Surface-scoped UI design-system gate (applies_to surface:ui). Isolated read-only reviewer judging that a UI slice follows THIS repo's visual language in internal/web/static/app.css — tokens, radii/badge/chip language, both-theme legibility, no emoji/icon fonts/raster icons, ◐ as the only mark. Judges, never mutates. A re-presented edge follows [[satelle-reviewer-verdict]].
---

# Design-system review (surface:ui scoped gate)

## Primary objective

Decide whether a **UI-touching** story's presented slice follows **this repository's**
design system as encoded in `internal/web/static/app.css` (and related templates
under `internal/web/`). Answer only: may the story proceed past this gate?

You receive `{story, from, to}` on stdin. **Read-only** (Read/Grep/Glob). Do not
edit, format, commit, or invent a redesign. Fair gate: judge the slice against
the rules below; do not add perfectionist requirements the story never stated.

`internal/web/static/app.css` is the source of truth.

A slice with no UI change accepts as design-exempt.

## How to judge

Inspect presented UI changes (CSS, HTML templates, static assets, web Go that
emits markup). Check:

1. **Tokens over ad-hoc literals.** New colors, radii, control heights, and
   content width should use CSS variables from `:root` / `[data-theme="dark"]`
   (`--ink`, `--muted`, `--accent`, `--chip`, `--panel`, `--content-w`,
   `--content-max`, `--control-height`, `--fail`, …) rather than inventing a
   parallel palette. Hard-coded hex in a tiny local exception is only acceptable
   when no token fits and the story AC requires it — name it in notes if
   accepting with caveat; reject a new free-form palette.

2. **Layout width model (this repo).** Content width is
   `width: var(--content-w)` with `max-width: var(--content-max)` where
   `--content-w: 80%` and `--content-max: 1600px`. Reject a
   slice that reintroduces conflicting width schemes without reconciling to
   these tokens.

3. **Badge / chip language.** Status chips and badges should follow existing
   chip/badge patterns (`.chip`, color-mix tints on accent/fail where the CSS
   already does so). Reject one-off badge systems that ignore chip tokens.

4. **Both themes.** UI that sets only light or only dark colors without using
   theme tokens must remain legible under both `default` and `[data-theme="dark"]`.
   Prefer token-backed colors.

5. **Typography.** Space Grotesk (self-hosted) + system fallbacks for UI; mono
   via `var(--mono)`. Do not pull remote font CDNs.

6. **Iconography.** No emoji, icon fonts, or raster icon packs as product
   chrome. The brand mark is the **◐** (half-circle) / SMIL mark language already
   in the app.

7. **Wordmark.** The wordmark period (`span.dot`) is current; a separate
   decorative accent on the footer product name is retired — reject it.

### Accept / reject

- **Accept** when the presented UI slice follows the rules above (or the story
  has no UI surface in the tree).
- **Reject** when a rule is clearly violated — name the file/pattern and the
  rule (e.g. “retired accent dot in page.go footer”).

## Verdict

```json
{"verdict": "accept", "notes": "", "reviewed": "<exact words this verdict rests on>"}
```

