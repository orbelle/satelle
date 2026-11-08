# Ledger store adapters

A ledger store is where satelle will replicate a ledger off the machine. Any
store that is not built in is an external command that implements the adapter
contract below; satelle links no vendor storage SDK. Configure it in
`satelle.toml`:

    [ledger]
    store = { command = "satelle-store-azure", args = ["--account", "...", "--container", "..."] }

A command path holding a `/` that is not absolute is relative to the working
tree; a bare name is looked up on `PATH`. `store = "satelle.dev"` and
`store = "local"` name satelle's built-in stores and implement no adapter
operations.

## The adapter contract (what an external store command implements)

satelle runs the command once per operation, with the configured args, then the operation and its operands; credentials come from the command's own environment (for Azure, the managed identity), never from satelle.

- `put-if-absent <key>`: reads the object body on stdin. Exit 0 = created. Exit 3 = an object with that key already exists, and it prints that object's body to stdout so satelle can compare. Anything else is an error.
- `get <key>`: writes the body to stdout. Exit 0 = found. Exit 4 = no such key.
- `list <prefix> [<after-key>]`: prints the keys under the prefix, one per line, sorted, starting after `<after-key>` when given. Exit 0, including when there are none.
- Errors: exit 1 for a transient failure (satelle may retry), and exit 2 for a permanent one (bad config, access denied). Either way the command prints one line to stderr, `error: <code>: <message>`, where `<code>` is one of `auth`, `not-found`, `conflict`, `unavailable`, `invalid`. satelle shows that line as the cause and never prints the command's environment.
- Keys are ASCII `[A-Za-z0-9._/-]`, at most 512 bytes; bodies are at most 64 MiB.
- satelle ships a conformance test the adapter author can run: `satelle ledger store-test` drives these operations against the configured store (it creates and reads keys under a `conformance/` prefix only).

Three rules complete it:

- An operation the command does not know is a permanent error: exit 2 with
  code `invalid` (`error: invalid: unknown operation <op>`).
- `list` sorts in byte order, as `LC_ALL=C sort` does, and compares keys as
  strings even when they look numeric: `1 < 1.0 < 102 < 12 < 1e2`. A prefix is
  a plain string prefix (`p/` lists `p/a`, not `pa`), and `<after-key>` is
  exclusive.
- `store-test` leaves its keys under `conformance/<run>/`, one fresh run prefix
  per run (`conformance/<UTC yyyymmddThhmmssZ>-<8 hex>/`), because the
  contract has no delete.

## How satelle runs the command

- The argv is `<command> <args...> <operation> <operands...>`, executed
  directly with no shell, in the working tree. Stdin carries the body for
  `put-if-absent` and is empty for the other operations.
- The environment is satelle's own, passed through with nothing added, minus
  satelle's credentials `SATELLE_TOKEN` and `SATELLE_NODE_TOKEN`.
- satelle checks every key operand (the key, the list prefix and the
  after-key) and the body against the limits before it runs the command, and
  refuses one outside them naming the limit.
- An operation is bounded to 5 minutes; a timeout counts as transient. Stdout
  above 64 MiB, an exit code the operation does not define, a `list` line that
  is not a key under the prefix, out of order, repeated or not after the
  after-key, and an exit 1 or 2 with no `error:` line are each a broken
  contract. The cause line is the last stderr line matching
  `^error: (auth|not-found|conflict|unavailable|invalid): .+$`; satelle shows
  only that line, never the rest of stderr or stdout.

## Proving an adapter

    satelle ledger store-test

runs eight checks against the configured command, one line each (`ok    <check>`
or `FAIL  <check>: <reason>`), and exits 0 only when every check passes; it
exits 1 naming the failed checks otherwise. The checks, in order:

1. list of a prefix with no keys is empty
2. get reports a missing key
3. put-if-absent creates a new key
4. put-if-absent refuses an existing key and returns its body
5. get returns a present key's body
6. list returns the keys under a prefix, sorted
7. list starts after <after-key>
8. an error prints error: <code>: <message>

The first two run before anything is written, so a store that has never been
written is exercised too. With no external store command configured (no
`[ledger] store`, or a built-in store named), `store-test` makes no store call
and exits 1.

The reference adapter `adapters/satelle-store-fs` in satelle's repository is a
POSIX sh implementation over a directory:

    [ledger]
    store = { command = "/path/to/satelle-store-fs", args = ["--root", "/path/to/dir"] }
