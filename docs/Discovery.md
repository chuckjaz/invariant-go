# The invariant project - Discovery Protocol

The Discovery Protocol is an HTTP-based protocol for discovering Invariant services and managing cluster identity, control-plane registration, service keys, and invitations.

As services are brought up, they register their ID, address, supported protocols, and optional tags with the Discovery service. Clients can query the Discovery service to find a service by its ID or search for services by protocol and tag. Clients should attempt to connect to a service using the protocols it supports in the order reported by the discovery service.

The discovery service periodically validates that registered services are still available. If a service is not available, the discovery service removes it from the list of registered services. A service is considered available if it responds to a `GET /id` request with the same ID as registered (note: `itp://` addresses are skipped by the TCP health tracker).

## Version

Version 1 of the discovery protocol uses the protocol token `discovery-v1`. When the control plane is enabled, the discovery server also advertises `itp-v1`.

## Values

### `:id`

A service or token ID (for services, a 32-byte hex-encoded value).

### `:protocol`

A protocol token string (e.g., `storage-v1`, `discovery-v1`, `itp-v1`).

### `:count`

An integer specifying the maximum number of services to return.

### `:key`

A public key or service key string.

## Authentication Middleware

When authentication is required (default `-allow-anonymous=false`, or when static keys or OAuth2 are configured), all endpoints except `GET /id` and `POST /auth/invite/redeem` require an `Authorization` header (`Bearer <token>` or raw `<token>`). Tokens are checked in order against:

1. Static keys (service caller)
2. Redeemed access tokens (with WireGuard peer public-key pinning when available; mismatch returns `401 Unauthorized`)
3. Service keys (service caller)
4. OAuth2 bearer tokens (user caller)

If authentication is required and no valid credential is provided, the server responds with `401 Unauthorized`. Admin-only endpoints (`POST /auth/service-key`, `GET /auth/service-keys`, `DELETE /auth/service-key/:key`, `POST /auth/invite`, `GET /auth/invites`, `DELETE /auth/invite/:id`, `GET /auth/tokens`, `DELETE /auth/token/:id`) return `403 Forbidden` for non-admin callers.

## Schemas

```ts
interface NodeIdentity {
    name?: string;
    email?: string;
}

interface ServiceDescription {
    id: string;
    address: string;
    protocols: string[];
    tags?: string[];
    public_key?: string;
    identity?: NodeIdentity;
}

interface ServiceRegistration {
    id: string;
    address: string;
    protocols: string[];
    tags?: string[];
    public_key?: string;
    identity?: NodeIdentity;
}
```

## Core Endpoints

### `GET /id`

Returns the ID of the discovery service.

#### Response

`200 OK` (`Content-Type: text/plain`) with the hex-encoded ID of the discovery service.

### `GET /protocols`

Returns the protocols supported by this discovery server.

#### Response

`200 OK` (`Content-Type: application/json`) returning a JSON array of protocol strings (e.g., `["discovery-v1"]` or `["discovery-v1", "itp-v1"]`).

### `GET /:id`

Returns the `ServiceDescription` for the given ID.

#### Response

`200 OK` (`Content-Type: application/json`) with the `ServiceDescription`, or `404 Not Found` if the ID is not registered.

### `GET /?protocol=:protocol&tag=:tag&count=:count`

Returns a list of service descriptions filtered by protocol and/or tag. If both `protocol` and `tag` are specified, services must match both.

#### Query Parameters

| Parameter | Description |
| --------- | ----------- |
| `protocol` | The protocol to search for (optional). |
| `tag` | The tag to filter by (optional). |
| `count` | Maximum number of services to return (optional; defaults to `1`, clamped to `>= 0`). |

#### Response

`200 OK` (`Content-Type: application/json`) returning a JSON array of `ServiceDescription` objects (`[]` when none match), or `500 Internal Server Error` on failure.

### `PUT /:id`

Register a service with the discovery service. User callers (`CallerKindUser`) are rejected with `403 Forbidden`. If the caller authenticated via an access token pinned to a `public_key`, a mismatched `public_key` in the request is rejected with `403 Forbidden` (unless the caller is an admin), and an empty `public_key` is populated from the token record.

#### Request

JSON body matching `ServiceRegistration`.

#### Response

`200 OK` with an empty body on success, `400 Bad Request` on invalid JSON, `403 Forbidden` on authorization/key-pinning failure, or `500 Internal Server Error` on registration failure.

## Control Plane (`itp-v1`) & WhoIs Endpoints

If the control plane is not enabled on the discovery server, these endpoints return `501 Not Implemented`.

### `POST /auth/client`

Register a WireGuard/ITP client public key and identity with the control plane.

#### Request

```ts
interface RegisterClientRequest {
    public_key: string;
    identity: NodeIdentity;
}
```

For user/admin callers, `identity.name` and `identity.email` are populated from the authenticated caller identity. Token-pinned callers must match their pinned `public_key` (unless admin).

#### Response

`200 OK` on success, `400 Bad Request` on invalid JSON or blank `public_key`, `403 Forbidden` on key-pinning mismatch, or `500 Internal Server Error` on failure.

### `GET /auth/verify?key=:key`

Verify whether a public key is authorized on the control plane.

#### Response

`200 OK` (`Content-Type: application/json`):

```ts
interface VerifyKeyResponse {
    authorized: boolean;
    identity?: NodeIdentity;
}
```

Returns `400 Bad Request` if `key` is missing.

### `GET /auth/keys`

List all authorized client public keys on the control plane.

#### Response

`200 OK` (`Content-Type: application/json`) returning `string[]` (`[]` if none).

### `GET /whois?key=:key`

Look up the `NodeIdentity` associated with an authorized public key.

#### Response

`200 OK` (`Content-Type: application/json`) returning `NodeIdentity`, `400 Bad Request` if `key` is missing, or `404 Not Found` if the key is not authorized or has no identity.

## Service Key Endpoints

If service key management is not enabled, these endpoints return `501 Not Implemented`.

### `POST /auth/service-key` (Admin)

Create a new service key.

#### Request

```ts
interface CreateServiceKeyRequest {
    name: string;
    expires_in?: string; // Go duration string, e.g. "720h"
}
```

#### Response

`201 Created` (`Content-Type: application/json`) returning `ServiceKeyRecord`:

```ts
interface ServiceKeyRecord {
    id: string;
    key: string;
    name: string;
    created_by: NodeIdentity;
    created_at: string;
    expires_at?: string;
    revoked?: boolean;
    revoked_at?: string;
    revoked_by?: NodeIdentity;
}
```

Returns `400 Bad Request` on invalid JSON, empty `name`, or invalid `expires_in` duration; `403 Forbidden` if not admin.

### `GET /auth/service-keys` (Admin)

List all service keys (with secret key values masked). Returns `200 OK` (`Content-Type: application/json`) `ServiceKeyRecord[]`.

### `GET /auth/verify-service-key?key=:key`

Verify a service key. Returns `400 Bad Request` if `key` is missing, or `200 OK` (`Content-Type: application/json`):

```ts
interface VerifyServiceKeyResponse {
    valid: boolean;
    record?: ServiceKeyRecord;
}
```

### `DELETE /auth/service-key/:key` (Admin)

Revoke a service key. Returns `200 OK` on success, `400 Bad Request` if `key` is empty, `403 Forbidden` if not admin, or `404 Not Found` if unknown.

## Invitation & Access Token Endpoints

If invitation management is not enabled, these endpoints return `501 Not Implemented`.

### `POST /auth/invite` (Admin)

Create a one-time invitation code.

#### Request

```ts
interface CreateInvitationRequest {
    name: string;
    email?: string;
    role?: "user" | "service" | "admin"; // defaults to "user"
    ttl?: string; // Go duration string
}
```

If `name` is blank, it defaults to `email`.

#### Response

`201 Created` (`Content-Type: application/json`) returning `InvitationRecord`:

```ts
interface InvitationRecord {
    id: string;
    code: string;
    control_plane_url?: string;
    name: string;
    email?: string;
    role: "user" | "service" | "admin";
    created_by: NodeIdentity;
    created_at: string;
    expires_at: string;
    used?: boolean;
    used_at?: string;
    used_by_key?: string;
    revoked?: boolean;
    revoked_at?: string;
    revoked_by?: NodeIdentity;
}
```

### `POST /auth/invite/redeem`

Redeem an invitation code for an access token. Rate-limited per peer key or IP (5 failed attempts per minute returns `429 Too Many Requests`).

#### Request

```ts
interface RedeemInvitationRequest {
    code: string;
    public_key?: string;
    oauth_token?: string;
    identity: NodeIdentity;
}
```

#### Response

`200 OK` (`Content-Type: application/json`) returning `AccessTokenRecord`:

```ts
interface AccessTokenRecord {
    id: string;
    token: string;
    control_plane_url?: string;
    name: string;
    email?: string;
    role: "user" | "service" | "admin";
    public_key?: string;
    invitation_id?: string;
    created_by: NodeIdentity;
    created_at: string;
    expires_at?: string;
    revoked?: boolean;
    revoked_at?: string;
    revoked_by?: NodeIdentity;
}
```

Errors: `400 Bad Request` (invalid JSON / empty `code`), `401 Unauthorized` (OAuth required or invalid), `403 Forbidden` (peer key mismatch, used, expired, revoked, or email mismatch), `404 Not Found` (invitation not found), `429 Too Many Requests` (rate limit exceeded).

### `GET /auth/invites` (Admin)

List all invitations (with codes masked). Returns `200 OK` (`Content-Type: application/json`) `InvitationRecord[]`.

### `DELETE /auth/invite/:id` (Admin)

Revoke an invitation by ID. Returns `200 OK`, `400 Bad Request` (empty `id`), `403 Forbidden` (non-admin), or `404 Not Found`.

### `GET /auth/tokens` (Admin)

List all access tokens (with token values masked). Returns `200 OK` (`Content-Type: application/json`) `AccessTokenRecord[]`.

### `GET /auth/verify-token?token=:token`

Verify an access token. Returns `400 Bad Request` if `token` is missing, or `200 OK` (`Content-Type: application/json`):

```ts
interface VerifyAccessTokenResponse {
    valid: boolean;
    record?: AccessTokenRecord; // masked
}
```

### `DELETE /auth/token/:id` (Admin)

Revoke an access token by ID. Returns `200 OK`, `400 Bad Request` (empty `id`), `403 Forbidden` (non-admin), or `404 Not Found`.
