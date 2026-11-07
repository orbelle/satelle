# satelle wire

The contract between satelle (CLI, satelled, remote node) and satelle-server.
**Every call to satelle-server is gRPC**, defined by the protos here; the only
exceptions are OAuth's browser authorize, discovery and token endpoints
(below). satelle owns these protos; satelle-server copies them.

| proto | package | services |
|---|---|---|
| [`satelle/chain/v2/chain.proto`](satelle/chain/v2/chain.proto) | `satelle.chain.v2` | `Chain` (Append, Read, Heads, Subscribe) |
| [`satelle/v1/satelle.proto`](satelle/v1/satelle.proto) | `satelle.v1` | `AccountService`, `ProjectService`, `WorkspaceService`, `LocationService`, `ItemService`, `AttachmentService`, `RemoteService`, `NodeService`, `RelayService`, `GitHubService` |

Generate the Go code with `buf generate` (from the repo root; `protoc-gen-go`
and `protoc-gen-go-grpc` on `PATH`). It lands in `internal/wire/chainpb` and
`internal/wire/satellepb`; the client is `internal/hosted`, and
`internal/wire/fakeserver` is an in-memory server for tests.

## Conventions

- **Transport.** `https://host[:port]` dials TLS on 443 unless a port is
  given; `http://host[:port]` and a bare loopback `host:port` dial without TLS
  (tests).
- **Authentication** is the gRPC metadata `authorization: Bearer <token>`: a
  user's OAuth access token, a session token (`SATELLE_TOKEN`) or a remote
  node's token.
- **Refresh.** On `UNAUTHENTICATED` the client refreshes a stored login once
  through the OAuth token endpoint (`grant_type=refresh_token`), persists the
  rotated pair and retries the call once. A session or node token is never
  refreshed. A message `unknown_subject: <text>` means the token is valid but
  its subject has no account; it is final.
- **Errors** are gRPC status codes: `UNAUTHENTICATED`, `PERMISSION_DENIED`,
  `NOT_FOUND`, `ALREADY_EXISTS` (project slug), `INVALID_ARGUMENT`,
  `FAILED_PRECONDITION` (a stale chain head, with a `StaleHead` detail).
- **Size.** Unary messages are capped at 64 MiB each way.

## The chain

Each work item is an append-only chain of events
`{id, item_id, kind, actor, at, payload, prev_hash, hash}`. The hash is the
canonical hash documented in `chain.proto` (Go reference: `internal/ledger`
`Canonical`/`HashOf`). `Append` is compare-and-append on `prev_hash`; a stale
head stores nothing and answers `FAILED_PRECONDITION` with a `StaleHead`
detail (the server's head and the events the client is missing). Pull walks
`Heads` and reads the items it is behind with `Read`.

Sync carries only items (their chains and `ItemService.PutItems` summaries) and
attachment bodies (`AttachmentService`, keyed by project, item and name, for
each `attachment.added` event). Configuration and documents are not synced.

## GitHub

`GitHubService` acts on the repository a project is bound to by the satelle
GitHub app; the request names the project, never the repository, and the
server never parses a PR title or appends to a chain. It decides nothing: the
review's event and body are the client's, passed through.

- `PublishReview{project, pull_number, commit_sha, event, body}` publishes a
  review as the app and answers `{review_id, html_url, repository}`. The CLI
  is `satelle github review --pr N --sha S --event E [--body-file F|-]
  [--json]`; satelle's own `pr-review` gate publishes its verdict this way
  (its `after` check, `.satelle/skills/satelle-pr-review-publish.md`) and
  records what was published as a `github.review` event on the item.
- `PublishStatus` and `ListPullRequestEvents` have no CLI yet.
- `FAILED_PRECONDITION` when the project has no GitHub binding.

## OAuth (HTTP, exempt)

Discovery reads `/.well-known/oauth-protected-resource/api` on the server, then
the authorization server's `/.well-known/oauth-authorization-server` (or
`/.well-known/openid-configuration`). Login is OAuth 2.1 + PKCE S256 with a
loopback redirect.

#### `GET /oauth/authorize`

Browser redirect to initiate OAuth 2.1 + PKCE S256. No bearer token required.

**Query parameters:**

| Parameter              | Type   | Description                         |
|------------------------|--------|-------------------------------------|
| `response_type`        | string | Must be `code`                      |
| `client_id`            | string | Public client identifier             |
| `redirect_uri`         | string | Callback URI                        |
| `state`                | string | CSRF token (echoed back)             |
| `code_challenge`       | string | PKCE S256 challenge                 |
| `code_challenge_method`| string | Must be `S256`                      |

**Response 302:** Redirect to `redirect_uri` with `code` + `state` (approval)
or `error` + `error_description` (denial).

#### `POST /oauth/token`

Exchanges an authorization code or refreshes tokens. No bearer token required.

**Request:** `application/x-www-form-urlencoded`

| Parameter       | Type   | Description                           |
|-----------------|--------|---------------------------------------|
| `grant_type`    | string | `authorization_code` or `refresh_token` |
| `client_id`     | string | Public client identifier               |
| `code`          | string | Auth code (authorization_code grant)  |
| `redirect_uri`  | string | Redirect URI (authorization_code grant) |
| `code_verifier` | string | PKCE S256 verifier (authorization_code grant) |
| `refresh_token` | string | Refresh token (refresh_token grant)   |

**Response 200:** `TokenResponse`

| Field            | Type   | Description              |
|------------------|--------|--------------------------|
| `access_token`   | string | Bearer access token     |
| `refresh_token`  | string | Refresh token          |
| `token_type`     | string | e.g. `Bearer`           |
| `expires_in`     | int64  | Lifetime in seconds     |
| `scope`          | string | Granted scope(s)        |

**Response 400:** `OAuthError`

| Field                | Type   | Description                  |
|----------------------|--------|------------------------------|
| `error`              | string | OAuth error code             |
| `error_description`  | string | Human-readable detail        |
