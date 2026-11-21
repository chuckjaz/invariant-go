# The invariant project - Names Protocol

The names protocol allows registering and retrieving service IDs and block addresses by logical name. A service ID can be sent to the discovery service to locate the service, and a block address can be sent to a storage service to retrieve the block.

Both IDs and addresses are 32-byte (64-character) hex strings.

Names can also be retrieved from a DNS TXT record with the form `invariant:<id|address>;<tokens>`, where `<tokens>` is a comma-separated list of protocol version tokens. A synthetic version token of `block-v1` is used for block addresses. Server protocol tokens (such as `storage-v1`) indicate that the value is a service ID implementing those protocols.

## Version

Version 1 of the names protocol uses the protocol token `names-v1`.

## Values

### `:name`

The logical name of a service, slot, or block.

### `:id`

A 32-byte hex-encoded service ID or value.

### `:value`

The ID of a service or the address of a block.

### `:tokens`

A comma-separated list of protocol version tokens for the service or block (e.g., `block-v1` for a block, or `storage-v1` for a storage service).

## Endpoints

### `GET /id`

Returns the ID of the names service (`200 OK`, `Content-Type: text/plain`), or `501 Not Implemented` if the backend does not expose an identity.

### `GET /lookup/:id`

Reverse-look up all logical names currently mapped to the given `:id`.

#### Response

`200 OK` (`Content-Type: application/json`) returning a JSON array of name strings (`string[]`), `400 Bad Request` if `:id` is empty, or `500 Internal Server Error` on failure.

### `GET /:name`

Retrieve the ID or address and protocol tokens associated with `:name`.

#### Required response headers

| Header | Value |
| ------ | ----- |
| `Content-Type` | `application/json` |
| `ETag` | `:value` |

#### Response

`200 OK` with a JSON object matching `NameResponse`:

```ts
interface NameResponse {
    value: string;
    tokens: string[];
}
```

Returns `404 Not Found` if the name does not exist, or `500 Internal Server Error` on failure.

### `PUT /:name?value=:id&tokens=:tokens`

Store or update the ID or address (`value`, required) and optional comma-separated `tokens` for `:name`.

#### Required response headers

| Header | Value |
| ------ | ----- |
| `ETag` | `:value` |

Returns `200 OK` on success, `400 Bad Request` if `value` is missing, or `500 Internal Server Error` on failure.

### `DELETE /:name`

Delete `:name` from the names service.

#### Required request headers

| Header | Value |
| ------ | ----- |
| `If-Match` | `:value` |

The `If-Match` header must match the current ID or address associated with `:name`. If it does not match or is missing, the request is rejected with `412 Precondition Failed`. Returns `404 Not Found` if the name does not exist.

#### Required response headers

| Header | Value |
| ------ | ----- |
| `ETag` | `:value` |
