# The invariant project - Build Cache

The Invariant Build Cache is a distributed caching adapter for the Go toolchain, implementing the `GOCACHEPROG` protocol introduced in Go 1.24.

It allows Go compiler and linker build artifacts to be cached in local memory, persisted to local disk, and distributed transparently across cluster nodes via Invariant Key-Value and Content Storage services.

## Architecture

The build cache handler runs as a child process of the `go` command (`GOCACHEPROG=inv-go-build-cache go build ...`):

1. **In-Memory LRU Cache**: Fast lookup for hot action and output IDs.
2. **Local Disk Storage**: Direct disk path return for instant compiler reuse.
3. **Invariant Key-Value Store**: Distributed index of Action IDs mapped to ContentLinks.
4. **Invariant Storage Service**: Content-addressed block storage with optional compression (gzip, inflate, zstd) and encryption (AES-256-CBC).

## Protocol Specification

Communication occurs over standard input and standard output using newline-delimited JSON messages.

### Handshake

Upon launch, the program outputs a handshake message with `ID: 0` listing supported commands:

```json
{"ID":0,"KnownCommands":["get","put","close"]}
```

### `get` Command

**Request**:
```json
{"ID":1,"Command":"get","ActionID":"<hex-encoded-action-id>"}
```

**Response (Hit)**:
```json
{
  "ID": 1,
  "OutputID": "<hex-encoded-output-id>",
  "Size": 1024,
  "Time": "2026-08-21T18:00:00Z",
  "DiskPath": "/path/to/cache/file"
}
```

**Response (Miss)**:
```json
{"ID":1,"Miss":true}
```

### `put` Command

**Request Header**:
```json
{"ID":2,"Command":"put","ActionID":"<hex-action-id>","OutputID":"<hex-output-id>","BodySize":1024}
```

Followed immediately by a second line containing the JSON-encoded binary payload.

**Response**:
```json
{"ID":2,"DiskPath":"/path/to/cache/file"}
```

### `close` Command

Flushes all background uploads and exits cleanly.

**Request**:
```json
{"ID":3,"Command":"close"}
```

**Response**:
```json
{"ID":3}
```
