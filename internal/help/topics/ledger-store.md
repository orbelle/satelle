# Ledger store adapters

A ledger store is where a repo's ledger replicates off the machine.
`satelle ledger push`, `satelle ledger pull` and `satelle ledger status` are
the one replication surface, with the same rules for every store.

## Choosing the store

Configure it in `satelle.toml`:

    [ledger]
    store = "satelle.dev"     # satelle.dev, built in
    store = "local"           # no replication
    store = { command = "satelle-store-azure", args = ["--account", "...", "--container", "..."] }

- `"satelle.dev"` replicates to the project this repo is bound to
  (`satelle project bind`), through satelle-server's Chain and Attachment
  services. It needs a sign-in (`satelle login`) and a binding; unbound,
  push, pull and status exit 1 naming both ways out.
- `"local"` replicates nothing: push, pull and status say
  `ledger store is local` and make no store or network call.
- A table names an external command that implements the adapter contract
  below; satelle links no vendor storage SDK. A command path holding a `/`
  that is not absolute is relative to the working tree; a bare name is looked
  up on `PATH`. `args` is optional: an adapter can take its settings from its
  own environment instead.

With no `[ledger] store` set, the store is `satelle.dev` while signed in
(`SATELLE_TOKEN`, or a stored `satelle login`) and `local` otherwise. Any
other value is refused when the configuration loads, naming `[ledger] store`.

The local runtime stays the working copy: only push, pull and status
resolve `[ledger] store` or contact the store (store-test reads it only to
check a configured command). No hook, gate, story or ledger read waits on it,
so they work with the store unreachable.

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
  satelle's credentials `SATELLE_TOKEN` and `SATELLE_NODE_TOKEN`. An adapter
  can therefore read its own settings (an account, a container) from its
  environment, and `args` may be omitted:
  `store = { command = "satelle-store-azure" }`.
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
written is exercised too. store-test checks only a configured store
command: it never resolves the default, so with no `[ledger] store` (even
while signed in) or a built-in store named, it makes no store or server call
and exits 1, because only a store command implements the adapter operations.

The reference adapter `adapters/satelle-store-fs` in satelle's repository is a
POSIX sh implementation over a directory:

    [ledger]
    store = { command = "/path/to/satelle-store-fs", args = ["--root", "/path/to/dir"] }

## Push, pull and status

    satelle ledger push [<item>...]
    satelle ledger pull [<item>...]
    satelle ledger status [--json]

replicate every item's chain, or only the named items', to and from the store.
An ephemeral container's ledger survives it: push before it exits, pull into
the next fresh runtime. Each verb first names the store (`ledger store:
satelle.dev project <id> at <server>` or `ledger store: command <path>`).

The rules are the same for every store:

- Push appends each local chain past what the store holds, every blob the new
  events reference first. Events the store already holds with the same hashes
  are already there; a different hash at a sequence is a divergence,
  `diverged: <item> at event <seq>: ...; not pushed past it`, that item's push
  stops, other items continue, and push exits 1. A store chain that already
  extends the local one needs nothing. A rerun is safe: every append is
  conditional.
- Pull compares the store's event at the last sequence both chains hold,
  whatever their lengths, and finds the fork point when it differs. A chain
  that forked, in a longer, an equal-length or a shorter store chain, is
  refused, `diverged: <item> at event <seq>: ...; nothing appended`, nothing is
  appended to it, and pull exits 1. A store chain that is a prefix of the
  local one, for a pull of every item or of one, is not a conflict: pull
  imports nothing, exits 0, and the next push writes the rest. New events are
  verified with the local chain before any is appended; one that does not
  verify refuses its item. A named item the store does not hold is refused.
  An item that arrives without a workflow (created by satelle-server) is
  placed by this repo's rules, on the store first.
- Status prints one row per item, `ITEM LOCAL STORE STATE`, the event counts
  and `in sync`, `ahead`, `behind`, `diverged at <seq>`, `local only` or
  `store only`. It writes nothing and exits 0.

satelle.dev appends through `Chain.Append`, compare-and-append on the head,
and lists chains with `Chain.Heads`. Blobs travel as attachments of their item
(`AttachmentService`): an attachment under its own name, a performer output as
`performer-output-<event id>`. Push also stores the item summaries the server
lists.

`satelle remote fire`, a remote placement (`remote:<id>`), a remote node and
`satelle remote status` move their run's item through satelle-server whatever
`[ledger] store` names, because the node runs there, by the same replicator
and rules: fire and a placement push only that item's chain, and refuse with
exit 1 when it diverged; a node and `remote status` pull only that item. No
hook, gate or read contacts the store.

A store command's object layout:

- `chains/<item>/<seq>.json`: one object per event, `<seq>` the event's
  0-based position on its chain as 12 digits (`chains/sto_1/000000000000.json`),
  the body the event exactly as satelle writes it, including its hash. The key
  carries no hash, so a conditional create tests exactly "no event at this
  sequence".
- `blobs/<sha256>`: one object per blob a chain references, an attachment's
  body or a performer run's stored output, written before the event that
  references it.

Push writes each event by `put-if-absent`. An exit 3 at a sequence compares
the hash in the returned body: the same hash is already there and the push
continues past it; a different hash is a divergence, `diverged: <item> at event
<seq>: ...; not pushed past it`, that item's push stops, other items continue,
and push exits 1. A rerun is safe: every write is conditional.

Pull lists `chains/`, compares the store's event at the last sequence both
chains hold, whatever their lengths, and finds the fork point when it
differs. A chain that forked, in a longer, an equal-length or a shorter store
chain, is refused, `diverged: <item> at event <seq>: ...; nothing appended`,
nothing is appended to it, and pull exits 1. A store chain that is a prefix of
the local one is not a conflict: pull appends nothing and the next push writes
the rest. New events are fetched in order and verified with the local chain
before any is appended; one that does not verify refuses its item.

Blobs are tolerant and recorded. Chains always replicate; a referenced blob
that is missing is a gap, named on one line with its item, event and sha256:

    gap: <item> event <seq> <kind> <event-id> blob <sha256> (<what>): <why>

Push names a blob neither side holds (`not in this runtime and not in the
store`); pull names one the store lacks (`not in the store`) or holds with
other bytes (`the store's body hashes to <sha256>; not stored`). A blob that
arrives later is checked against its sha256 before it fills the gap
(`filled: <item> event <seq> blob <sha256>`). Gaps never change push's or
pull's exit code. `satelle ledger verify` exits 3 while any gap remains (every
chain intact, a referenced blob missing or corrupt), so a pipeline can gate on
it; it exits 1 for a broken chain and 0 when everything is held.
