---
name: satelle-group-related-stories
type: principle
tags: [type:principle]
applies_to: ["*"]
description: When filing two or more related stories in one sitting, give them a shared epic:<theme>, this repo's sprint:<YYYY-MM>-<name> tag and an order:<N> each before finishing.
---

# Group related stories at filing time

Two or more related stories filed in one sitting share an `epic:<theme>`, the
current sprint tag and an `order:<N>` each, before you finish. A dependency is
`depends-on:`, never an order. Vocabulary: [[satelle-story-classification]].

This repo's sprint tag is `sprint:<YYYY-MM>-<name>`: the month the sprint
starts plus a short kebab-case name, usually the epic theme
(`sprint:2026-09-model-selection`). Never a bare month. Stories in older forms
are left as they are.
