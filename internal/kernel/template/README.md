# .satelle

This repo's process is run by satelle, a CLI. The process is the configuration
in this directory: workflows, the gates on their transitions, the skills gates
and performers follow, and the agents that do the work. It is tracked in git
and is yours to edit.

## If you are an agent working in this repo

If your session start gave you no satelle context (a session id and your
seats), your harness is not wired to satelle here. Then:

- `satelle doctor` is the check. It reports what is wrong here, each issue with
  the command that fixes it, and changes nothing.
- `satelle status` shows the configuration, the wiring of each harness and the
  item counts.
- `satelle help` is the manual. Start with `satelle help satelle`, then
  `satelle help workflows` and `satelle help edit-gate`.
- `satelle agents install <harness>` wires a harness (`claude`, `pi`, `grok` or
  `cursor`), so its sessions are told about satelle and their edits pass
  through the edit gate. Commit the file it writes so a clone is wired too.

If the `satelle` command is missing, install it first:

    curl -fsSL https://github.com/bobmcallan/satelle/releases/latest/download/install.sh | sh

## Working with it

    satelle story list                       # the work items
    satelle story route <id>                 # where an item is, and its gate results
    satelle story set <id> --status <state>  # present a transition; its gates judge it
    satelle workflow show                    # the configured workflows
