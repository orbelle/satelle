---
name: satelle-chore-category
type: principle
tags: [type:principle]
applies_to: ["*"]
description: In this repo a chore changes nothing a user of satelle or satelled would notice and cuts no release; a story that changes what a user gets is a fix or feature. A create review rejects a misfiled chore.
---

# Chore ships nothing

`category: chore` is housekeeping no user of the `satelle` or `satelled` binary
would notice: cleanup, test or CI upkeep, repo hygiene. It cuts no release.
A draft that changes what a user gets is a `fix` or `feature`; a create review
rejects it filed as a chore and names the fitting category.
