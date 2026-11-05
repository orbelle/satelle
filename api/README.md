# satelle hosted API

Conformance specification for the satelle hosted server interface. The CLI
publishes this; servers in any language implement it with their own types
(verified by contract tests on the server side). No shared Go module exists
— the [checkout-sync proto](checkout_sync.proto) is the canonical
definition of the gRPC Sync service. satelle-server copies that proto
verbatim; it is not implemented in this repo. (The REST OpenAPI spec and the
message-board proto were removed in the rebuild; every remaining REST call is
to be replaced by gRPC — see REBUILD.md, Wire changes.)

## Auth conventions

**Scheme:** Bearer token (`Authorization: Bearer <access_token>`)

Tokens are obtained via OAuth 2.1 + PKCE S256:
1. Browser redirect to `GET /oauth/authorize` with `response_type=code`,
   `client_id`, `redirect_uri`, `state`, `code_challenge`, `code_challenge_method=S256`.
2. On approval the server redirects back with `code` and `state`.
3. `POST /oauth/token` with `grant_type=authorization_code`, `code`,
   `redirect_uri`, `client_id`, `code_verifier` (form-encoded).

**Refresh:** On a 401, clients should attempt a `refresh_token` grant
(`grant_type=refresh_token` + `refresh_token` + `client_id`) and retry the
original request once.

## gRPC checkout-sync

Canonical contract: [`checkout_sync.proto`](checkout_sync.proto) (`service Sync`
with `Apply`, `Snapshot`, and `Refresh`). satelle-server copies this proto
verbatim and implements it. This repo publishes the contract and does not
implement the server. The CLI consumes Apply/Snapshot/Refresh from
`internal/hosted` (generated stubs in `internal/hosted/syncpb` so `internal`
does not import `api/`).

OAuth 2.1 + PKCE **stays on HTTP** `GET /oauth/authorize` and
`POST /oauth/token` (the section above) for browser login and for REST
`doAuthed`. **No gRPC login** RPC is specified.

`Apply` and `Snapshot` carry gRPC metadata key `authorization` (lowercase;
metadata keys are case-insensitive) with value `Bearer <access_token>`.
`Sync.Refresh` must **not** require a valid bearer — the refresh token in
the request is the credential. A server that demands a live access token
on Refresh can never recover from `UNAUTHENTICATED`.

`UNAUTHENTICATED` is the gRPC analogue of HTTP 401: the client calls
`Sync.Refresh` on the **same connection**, persists the rotated pair, then
retries the RPC **once** — the same retry-once policy as REST `doAuthed`.

Unary Sync messages (`Apply`, `Snapshot`, `Refresh`) are capped at **64 MiB**
(67108864 bytes) in each direction. The CLI sets this as a per-call option
(`MaxCallRecvMsgSize` / `MaxCallSendMsgSize`); grpc-go's default recv cap is
4 MiB, which real project snapshots already exceed (satelle ~24 MiB). A server
with a lower recv cap will reject large `Apply` batches. `Snapshot` supports
optional paging and since-cursors (`page_size` 0 keeps the unpaged
single-message contract); the field meaning lives in `checkout_sync.proto`.

## Endpoints

### `GET /api/v1/me`

Returns the authenticated principal.

**Response 200:** `Principal`

| Field          | Type   | Description                |
|----------------|--------|----------------------------|
| `id`           | string | Unique principal identifier |
| `email`        | string |                            |
| `display_name` | string |                            |
| `role`         | string | e.g. `admin`, `member`     |

### `GET /api/v1/projects`

Returns all projects the caller is a member of.

**Response 200:** array of `Project`

| Field  | Type   | Description                    |
|--------|--------|--------------------------------|
| `id`   | string | Unique project identifier       |
| `slug` | string | URL-safe slug (unique per server) |
| `name` | string |                                |
| `role` | string | Caller's role (may be omitted) |

### `POST /api/v1/projects`

Creates a new project. The authenticated principal becomes owner.

**Request body:** `CreateProjectRequest`

| Field  | Type   | Description                          |
|--------|--------|--------------------------------------|
| `slug` | string | URL-safe slug (must be unique)      |
| `name` | string |                                      |

**Response 201:** `Project`  
**Response 409:** Slug already exists.

### `GET /oauth/authorize`

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

### `POST /oauth/token`

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

## Error envelope

All API endpoints (except OAuth) use a consistent JSON error body:

```json
{"error": "not_found", "message": "project does not exist"}
```

| Field    | Type   | Description                |
|----------|--------|----------------------------|
| `error`  | string | Machine-readable error code |
| `message`| string | Human-readable detail       |

## HTTP status codes

| Code | Meaning                        |
|------|--------------------------------|
| 200  | Success                        |
| 201  | Created                        |
| 400  | Invalid request / OAuth error  |
| 401  | Authentication required        |
| 403  | Forbidden (insufficient role) |
| 404  | Not found                      |
| 409  | Conflict (slug already exists) |

## Reference types

The Go reference wire types live in `types.go`. They are structurally
identical to the CLI's internal client types but independently owned — no
cross-imports between `api/` and `internal/`.
