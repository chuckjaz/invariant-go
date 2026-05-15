# The invariant project - Storage protocol

A storage server must implement the storage protocol. The protocol is a set of HTML Requests.

The storage protocol allows storing and retrieving blobs based on the content hash of the blob.

The default algorithm for storage is SHA-256 but any algorithm can be used as determined by the storage service, however, the address is always 32 bytes. If an algorithm other than SHA-256, the bits should be evenly spread across 256 bits such as hashing the hash result with SHA-256.

Server specific authorization my be required for any of the requests.

# Version

The version 1 of the storage protocol with the protocol token of storage-v1.

# Values

## `:address`

The content hash of the content.

## `:id`

A server ID which is a 32 byte hex encoded value.

# `GET /id`

Determine the `:id` of the server.

# `GET /:address`

Retrieve an octent stream of the data with hash code `:address`, if it is in the store.

### Required response headers

| Header        | Value                     |
| ------------- | ------------------------- |
| Content-Type  | application/octet-stream  |
| cache-control | immutable                 |
| ETag          | `:address`                |

All other headers are as defined by HTML 1.1

## `HEAD /:address`

Retrieve information about whether a blob is available.

### Required response headers

| Header         | Value                     |
| -------------- | ------------------------- |
| Content-Type   | application/octet-stream  |
| ETag           | `:address`                |
| content-length | `:size`                   |

## `POST /`

Store a blob into the store. The server, if it accepts a blob, is required to support up to 1 Mib of data per blob. It may store larger blobs but this should not be relied on.

### Required response headers

| Header         | Value                     |
| -------------- | ------------------------- |
| Content-Type   | text/plain                |

The body of the response is the `:address` of the content.

## `PUT /:address`

Store a blob into the store with the given `:address`.

This is similar to POST but the `:address` must match the hash code of the uploaded content.

If content with the given `:address` is already present in the store the server may disconnect the PUT.

### Required response headers

| Header         | Value                     |
| -------------- | ------------------------- |
| Content-Type   | text/plain                |

The body of the response is the URL path part of the content.

## `POST /fetch`

An optionally supported fetch request. This is a request for the storage service to retrieve and store a block from another storage service.

### Required response headers

| Header         | Value                     |
| -------------- | ------------------------- |
| Content-Type   | application/json          |


### Request

The request is a JSON object with TypeScript type of,

```ts
interface StorageFetchRequest {
    address: string;
    container: string;
}
```

## `HEAD /fetch`

Responds with status 200 if `POST /fetch` is supported or 404 otherwise.

## `POST /batch_has`

An optionally supported request to check the existence of multiple blobs in a single request.

### Request

The request body is a JSON array of string addresses.

```ts
type BatchHasRequest = string[];
```

### Required response headers

| Header         | Value                     |
| -------------- | ------------------------- |
| Content-Type   | application/json          |

### Response

The response is a JSON object with a `missing` field containing an array of string addresses that are missing from the store.

```ts
interface BatchHasResponse {
    missing: string[];
}
```

## `POST /batch_store`

An optionally supported request to store multiple blobs in a single request.

### Request

The request is a `multipart/form-data` payload. Each part represents a blob to store. The `name` parameter of the `Content-Disposition` header for each part must be the `:address` (content hash) of the blob data contained in that part.

### Required response headers

Responds with status 200 OK on success. No specific headers are required.