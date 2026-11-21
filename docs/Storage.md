# The invariant project - Storage Protocol

A storage server implements the storage protocol over HTTP.

The storage protocol allows storing and retrieving blobs based on the content hash of the blob.

The default algorithm for storage is SHA-256, but any algorithm can be used as determined by the storage service; however, the address is always 32 bytes (64 hex characters). If an algorithm other than SHA-256 is used, the bits should be evenly spread across 256 bits, such as hashing the hash result with SHA-256.

Server-specific authorization may be required for any of the requests.

## Version

Version 1 of the storage protocol uses the protocol token `storage-v1`.

## Values

### `:address`

The 32-byte hex-encoded content hash of the blob.

### `:id`

A server ID, which is a 32-byte hex-encoded value.

## Endpoints

### `GET /id`

Determine the `:id` of the server.

#### Response

`200 OK` (`Content-Type: text/plain`) with the `:id` of the server.

### `GET /:address`

Retrieve an octet stream of the data with hash code `:address`, if it is in the store.

#### Required response headers

| Header | Value |
| ------ | ----- |
| `Content-Type` | `application/octet-stream` |
| `Cache-Control` | `immutable` |
| `ETag` | `:address` |
| `Content-Length` | `:size` (when blob size is known) |

All other headers are as defined by HTTP/1.1. Returns `404 Not Found` if the blob is not in the store.

### `HEAD /:address`

Retrieve information about whether a blob is available.

#### Required response headers

| Header | Value |
| ------ | ----- |
| `Content-Type` | `application/octet-stream` |
| `ETag` | `:address` |
| `Content-Length` | `:size` |

Returns `200 OK` if the blob exists, or `404 Not Found` otherwise.

### `POST /`

Store a blob into the store. The server, if it accepts a blob, is required to support up to 1 MiB of data per blob. It may store larger blobs, but this should not be relied on.

#### Required response headers

| Header | Value |
| ------ | ----- |
| `Content-Type` | `text/plain` |

Returns `200 OK` with the `:address` of the stored content in the response body, or `400 Bad Request` on error.

### `PUT /:address`

Store a blob into the store with the given `:address`.

This is similar to `POST /`, but `:address` must match the hash code of the uploaded content. If content with the given `:address` is already present in the store, the server may accept it idempotently or disconnect the `PUT`.

#### Required response headers

| Header | Value |
| ------ | ----- |
| `Content-Type` | `text/plain` |

Returns `200 OK` with `:address` in the response body, or `400 Bad Request` if storing fails or the content hash does not match `:address`.

### `POST /fetch`

An optionally supported fetch request (requires a Discovery client configured on the storage server). This requests the storage service to retrieve and store a block from another storage service.

#### Request

JSON object matching `StorageFetchRequest`:

```ts
interface StorageFetchRequest {
    address: string;
    container: string;
}
```

#### Response

- `200 OK` (empty body) if the blob is already present locally or was successfully fetched and stored.
- `400 Bad Request` on invalid JSON or empty `address`/`container`.
- `404 Not Found` if the storage server has no discovery client configured.
- `502 Bad Gateway` if the `container` cannot be found in discovery or fails to return the blob.
- `500 Internal Server Error` if storing the fetched blob fails.

### `HEAD /fetch`

Responds with `200 OK` if `POST /fetch` is supported (discovery client configured) or `404 Not Found` otherwise.

### `POST /batch_has`

Check the existence of multiple blobs in a single request.

#### Request

JSON array of string addresses:

```ts
type BatchHasRequest = string[];
```

#### Required response headers

| Header | Value |
| ------ | ----- |
| `Content-Type` | `application/json` |

#### Response

`200 OK` with a JSON object containing a `missing` field listing addresses absent from the store:

```ts
interface BatchHasResponse {
    missing: string[];
}
```

Returns `400 Bad Request` on invalid JSON or `500 Internal Server Error` on failure.

### `POST /batch_store`

Store multiple blobs in a single request.

#### Request

A `multipart/form-data` payload. Each part represents a blob to store, where the `name` parameter of the part's `Content-Disposition` header (`FormName`) must be the `:address` (content hash) of the blob data in that part.

#### Response

`200 OK` on success, `400 Bad Request` if the request is not valid `multipart/form-data`, or `500 Internal Server Error` on storage failure.

## Storage Backends

The `inv-storage` service and `internal/storage` package support multiple storage backends:

- **In-Memory (`InMemoryStorage`)**: Ephemeral in-memory block map (default when no backend flags are given).
- **File System (`FileSystemStorage`, `-dir`)**: Persistent nested directory layout on local disk.
- **AWS S3 (`S3Storage`, `-s3-bucket`, `-s3-prefix`)**: Stores the server ID at `<prefix>/id` and blobs at `<prefix><addr[0:2]>/<addr[2:4]>/<addr[4:]>` (AWS region/credentials are loaded from standard AWS SDK config/environment).
- **Git Direct (`GitStorage`, `-git-dir`)**: Read-only backend that resolves `"SHA256:<address>"` to a Git SHA1 blob via the KeyValue service and reads blobs directly from a local Git repository.
- **GitHub Direct (`GitHubStorage` package)**: Read-only Go backend (`internal/storage/github`) that resolves `"SHA256:<address>"` via the KeyValue service and fetches raw blobs from the GitHub REST API (`/repos/{owner}/{repo}/git/blobs/{sha1}`).
