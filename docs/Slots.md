# The invariant project - Slots Protocol

A slots service maintains a mapping from a slot `:id` to its current `:address`. A slot `:id` is a 32-byte hex-encoded value, and `:address` is a string representing the SHA-256 hash of a content block. Slots can be queried for their current `:address` and updated atomically using compare-and-swap (`previousAddress`).

## Values

### `:id`

A 32-byte hex-encoded value.

### `:address`

A string representing the SHA-256 hash of a content block.

### `:policy`

A protection policy for the slot. Currently supported: `ecc` for an Ed25519 256-bit elliptic curve key pair (where `:id` is the hex-encoded public key).

## Endpoints

### `GET /id`

Returns the ID of the slots service (`200 OK`, `Content-Type: text/plain`).

### `GET /:id`

Returns the `:address` for the given `:id` (`200 OK`, `Content-Type: text/plain`).

Returns `400 Bad Request` if `:id` is empty, `404 Not Found` if the slot does not exist, or `500 Internal Server Error` on failure.

### `PUT /:id`

Atomically updates the `:address` for the given `:id`.

#### Request

JSON object matching `SlotUpdate`:

```ts
interface SlotUpdate {
    address: string;
    previousAddress: string;
}
```

The request is rejected with `409 Conflict` if the slot's current `:address` does not equal `previousAddress`. Clients should use `GET /:id` to fetch the current `:address` before attempting an update, merge any concurrent changes, and retry.

When a slot was created with a `:policy` (e.g., `ecc`), requests to update the slot must include an `Authorization` header containing the hex-encoded signature of the raw JSON request body signed with the slot's private key.

#### Response

`200 OK` (empty body) on success, `400 Bad Request` on empty `:id` or invalid JSON, `401 Unauthorized` on missing or invalid signature, `404 Not Found` if the slot does not exist, `409 Conflict` on `previousAddress` mismatch, or `500 Internal Server Error` on failure.

### `POST /:id?protected=:policy`

Create a new slot with the given `:id` and optional `protected` query parameter (`:policy`).

#### Request

JSON object matching `SlotRegistration`:

```ts
interface SlotRegistration {
    address: string;
}
```

#### Response

`200 OK` (empty body) on success, `400 Bad Request` on empty `:id` or invalid JSON, `409 Conflict` if the slot already exists, or `500 Internal Server Error` on failure.
