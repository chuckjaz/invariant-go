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
