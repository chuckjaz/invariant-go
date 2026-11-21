# The invariant project - KeyValue Protocol

A key-value service provides a distributed, persistent mapping of string keys to octet stream values with MVCC transactions and version history. It is built on top of the storage and slots protocols for data durability and consistency.

When running over Tailscale/ITP, the server resolves caller identity via `WhoIs` (cached for `WhoIsTTL`, default `1m`) and attaches it to the request context.

## Values

### `:key`

A string representing the key to store or retrieve.

### `:value`

An octet stream representing the data associated with a key.

### `:txID`

An unsigned 64-bit transaction ID (`uint64`).

## Endpoints

### `GET /id`

Returns the hex-encoded ID of the key-value service (`200 OK`, `Content-Type: text/plain`).

### `GET /get?key=:key&tx=:txID`

Retrieve the value associated with the given `:key`.

#### Request

Requires the `key` query parameter (`400 Bad Request` if missing). Optional `tx` query parameter performs the read within an existing transaction; if omitted, an implicit checkpoint transaction is created.

#### Required response headers

| Header | Value |
| ------ | ----- |
| `Content-Type` | `application/octet-stream` |
| `X-Transaction-ID` | `:txID` |

#### Response

`200 OK` with the raw value bytes in the body, or `404 Not Found` if the key is not found.

### `POST /put?key=:key&tx=:txID`

Store a value for the given `:key`.

#### Request

Requires the `key` query parameter (`400 Bad Request` if missing). Optional `tx` query parameter performs the write within an existing transaction; if omitted, an implicit single-operation transaction is created and committed. The request body is the raw octet stream value.

#### Required response headers

| Header | Value |
| ------ | ----- |
| `X-Transaction-ID` | `:txID` |

#### Response

`200 OK` (empty body) on success, `400 Bad Request` on missing `key` or body read error, or `500 Internal Server Error` on storage error. `X-Transaction-ID` contains the monotonically increasing sequence/transaction ID.

### `POST /batch_get?tx=:txID`

Retrieve the values associated with a batch of keys. Optional `tx` query parameter specifies an existing transaction.

#### Request

JSON array of key strings:

```json
[
  "key1",
  "key2"
]
```

#### Response

`200 OK` (`Content-Type: multipart/form-data`) where each part corresponds to a found key (`Content-Disposition: form-data; name="<key>"`), includes an `X-Transaction-ID` header, and has the octet stream value as its body. Missing keys are omitted. Returns `400 Bad Request` on invalid JSON or `500 Internal Server Error` on failure.

### `POST /batch_put?tx=:txID`

Store values for a batch of keys. Optional `tx` query parameter specifies an existing transaction.

#### Request

A `multipart/form-data` payload where each part's `FormName()` (`name` in `Content-Disposition`) is the key and its body is the octet stream value.

#### Required response headers

| Header | Value |
| ------ | ----- |
| `X-Transaction-ID` | `:txID` |

#### Response

`200 OK` (empty body) on success, `400 Bad Request` on invalid multipart payload, or `500 Internal Server Error` on failure.

### `GET /history?key=:key&tx=:txID&min=:min&max=:max&limit=:limit`

Retrieve historical values associated with `:key` within a transaction ID range.

#### Query Parameters

- `key`: Required key string (`400 Bad Request` if missing).
- `tx`: Optional transaction ID.
- `min`: Minimum transaction ID (`uint64`, default `0`).
- `max`: Maximum transaction ID (`uint64`, default `^uint64(0)`).
- `limit`: Maximum number of versions to return (default `100`).

#### Required response headers

| Header | Value |
| ------ | ----- |
| `Content-Type` | `multipart/form-data` |
| `X-Has-More` | `true` or `false` |

#### Response

`200 OK` (`multipart/form-data`) where each part has `Content-Disposition: form-data; name="<key>"` and an `X-Transaction-ID` header. Returns `500 Internal Server Error` on failure.

### `POST /batch_history?tx=:txID&min=:min&max=:max&limit=:limit`

Retrieve historical values for a batch of keys within a transaction ID range.

#### Request

JSON array of key strings (`400 Bad Request` on invalid JSON). Supports optional `tx`, `min` (`0`), `max` (`^uint64(0)`), and `limit` (`100`) query parameters.

#### Response

`200 OK` (`Content-Type: multipart/form-data`) where each part's `name` is the key and includes an `X-Transaction-ID` header. The **first** part returned for each key also includes an `X-Has-More` (`true`/`false`) header. Keys with no matching versions are omitted. Returns `500 Internal Server Error` on failure.

## Transaction Endpoints

### `POST /tx/start?sequential=:bool`

Start a new transaction. Pass `sequential=true` to request a sequential transaction.

#### Response

`200 OK` (`Content-Type: application/json`) with `X-Transaction-ID: <txID>` header and JSON body:

```json
{"transaction_id": 42}
```

Returns `500 Internal Server Error` on failure.

### `POST /tx/commit?tx=:txID`

Commit an existing transaction (`tx` query parameter required, `400 Bad Request` if invalid). Returns `200 OK` on success, or `409 Conflict` on an MVCC conflict.

### `POST /tx/abort?tx=:txID`

Abort an existing transaction (`tx` query parameter required, `400 Bad Request` if invalid). Returns `200 OK` on success, or `500 Internal Server Error` on failure.

### `POST /tx/checkpoint`

Create an immediately committed checkpoint transaction for read-only snapshots.

#### Response

`200 OK` (`Content-Type: application/json`) with `X-Transaction-ID: <txID>` header and JSON body:

```json
{"transaction_id": 42}
```

Returns `500 Internal Server Error` on failure.
