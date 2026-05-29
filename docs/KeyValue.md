# The invariant project - KeyValue protocol

A key-value service provides a distributed, persistent mapping of string keys to octet stream values. It is built on top of the storage and slots protocols to ensure data durability and consistency.

## Values

### `key`

A string representing the key to store or retrieve.

### `value`

An octet stream representing the data associated with a key.

## Endpoints

## `GET /get?key=:key`

Retrieve the value associated with the given `:key`.

### Request

The request requires the `key` query parameter.

### Required response headers

| Header        | Value                     |
| ------------- | ------------------------- |
| Content-Type  | application/octet-stream  |
| X-Sequence    | `:sequence`               |

### Response

The body of the response is the value associated with the `:key`. If the key is not found, the server responds with a 404 Not Found status.

## `POST /put?key=:key`

Store a value for the given `:key`.

### Request

The request requires the `key` query parameter. The body of the request is the octet stream to store as the value.

### Required response headers

| Header        | Value                     |
| ------------- | ------------------------- |
| X-Sequence    | `:sequence`               |

### Response

The body of the response is empty. The `X-Sequence` header contains the sequence number of the update, which monotonically increases for each successful put operation.

## `POST /batch_get`

Retrieve the values associated with a batch of keys.

### Request

The request body should be a JSON array containing the keys to retrieve:

```json
[
  "key1",
  "key2"
]
```

### Response

The response is a `multipart/form-data` payload where each part corresponds to a found key. The part's name is the key, and the part's body is the octet stream value. Each part also includes an `X-Sequence` header indicating the sequence number of that specific key's value. Keys that are not found are omitted from the response.

## `POST /batch_put`

Store values for a batch of keys.

### Request

The request should be a `multipart/form-data` payload. Each part's name represents the key, and the part's body is the octet stream to store as the value.

### Required response headers

| Header        | Value                     |
| ------------- | ------------------------- |
| X-Sequence    | `:sequence`               |

### Response

The body of the response is empty. The `X-Sequence` header contains the highest sequence number resulting from the batch update.

## `GET /history?key=:key&min=:min&max=:max&limit=:limit`

Retrieve the historical values associated with the given `:key` within a sequence range.

### Request

The request requires the `key` query parameter. Optional parameters include `min` (minimum sequence, default 0), `max` (maximum sequence, default infinity), and `limit` (max number of records to return, default 100).

### Required response headers

| Header        | Value                     |
| ------------- | ------------------------- |
| Content-Type  | multipart/form-data       |
| X-Has-More    | `true` or `false`         |

### Response

The response is a `multipart/form-data` payload where each part corresponds to a historical version of the key. Each part includes an `X-Sequence` header. The top-level `X-Has-More` header indicates if there might be more versions available beyond the returned page.

## `POST /batch_history?min=:min&max=:max&limit=:limit`

Retrieve the historical values associated with a batch of keys within a sequence range.

### Request

The request body should be a JSON array containing the keys to retrieve. Optional query parameters include `min`, `max`, and `limit`.

### Response

The response is a `multipart/form-data` payload where each part corresponds to a historical version of a requested key. The part's name is the key. Each part includes an `X-Sequence` header. Additionally, the **first** part returned for each key will include an `X-Has-More` header indicating if there might be more versions available for that specific key. Keys that are not found or have no versions in the range are omitted.
