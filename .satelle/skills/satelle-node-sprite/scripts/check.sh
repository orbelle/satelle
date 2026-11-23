#!/bin/sh
command -v sprite >/dev/null 2>&1 || { echo "sprite-check: the sprite CLI is not on PATH: install it and sign in with sprite login"; exit 1; }
out=$(sprite org list 2>&1) || { printf '%s\n' "$out"; echo "sprite-check: sprite org list failed: sign in with sprite login"; exit 1; }
printf '%s\n' "$out" | grep -q '^Currently selected org: ..*' || { printf '%s\n' "$out"; echo "sprite-check: the sprite CLI is not signed in: run sprite login"; exit 1; }
echo "sprite-check: $(printf '%s\n' "$out" | grep '^Currently selected org: ')"
