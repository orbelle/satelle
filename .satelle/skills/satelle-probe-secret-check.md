---
name: satelle-probe-secret-check
type: check
description: The check of the secret-probe workflow. Proves a docker-local node was given its declared secret SATELLE_PROBE_SECRET: records the sha256 of the value the step reads and of the value [setup] read, that no process's argv carries it, and the names (never the values) of the step's environment. Never prints the value. Deterministic, no agent.
---

# Probe secret

The secret-probe workflow gives a docker-local node an item it drives end to
end with no performer; its states and gates are `satelle workflow show
secret-probe --session <session>`. The remote `docker-secret-probe` declares
the secret `SATELLE_PROBE_SECRET`, which `satelle remote fire` reads from the
firing shell and hands to the node over its stdin. Its `[setup]` writes the
sha256 of the value it read to `$HOME/.satelle-secret-probe-setup.sha256`.

This check passes only when the step reads the value, setup read the same
value, and no process's argv carries it. Its output is the gate's notes: the
two sha256 lines, the argv scan and the environment's names. It prints no
value; compare `step sha256:` with the sha256 of the value you fired with.

```sh
set -u
[ -n "${SATELLE_PROBE_SECRET:-}" ] || { echo "SATELLE_PROBE_SECRET is not set in the step"; exit 1; }
step=$(printf %s "$SATELLE_PROBE_SECRET" | sha256sum | cut -d' ' -f1)
setup=$(cat "$HOME/.satelle-secret-probe-setup.sha256" 2>/dev/null || true)
n=0
for f in /proc/[0-9]*/cmdline; do
  c=$( { tr '\0' ' ' < "$f"; } 2>/dev/null ) || continue
  n=$((n + 1))
  case "$c" in
    *"$SATELLE_PROBE_SECRET"*) echo "argv carries the secret"; exit 1 ;;
  esac
done
echo "step sha256:$step"
echo "setup sha256:$setup"
echo "argv clean: $n processes"
echo "env names: $(awk 'BEGIN { for (k in ENVIRON) print k }' | sort | tr '\n' ' ')"
[ -n "$setup" ] && [ "$setup" = "$step" ] || { echo "setup and the step read different values"; exit 1; }
```
