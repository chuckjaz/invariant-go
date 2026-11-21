# The invariant project - Build Cache

The Invariant Build Cache (`inv-go-build-cache`) is a distributed caching adapter for the Go toolchain, implementing the `GOCACHEPROG` protocol introduced in Go 1.24.

It allows Go compiler and linker build artifacts to be cached in local memory, persisted to local disk, and distributed transparently across cluster nodes via Invariant Key-Value and Content Storage services.

## Usage and CLI Flags

Run Go builds with `GOCACHEPROG`:

```bash
GOCACHEPROG="inv-go-build-cache" go build ./...
```

`inv-go-build-cache` loads global configuration from `~/.invariant/config.yaml` (and `INVARIANT_*` environment variables) and accepts the following flags:

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `-cache-dir` | `.invariant/build-cache` | Local disk cache directory (and standalone storage fallback). |
| `-discovery` | `""` | Discovery service URL (overrides `config.yaml`). |
| `-tag` | `generated` | Storage write tag (`any` clears the tag). |
| `-compress` | `false` | Enable compression for cached artifacts. |
| `-compress-algo` | `""` | Compression algorithm (`gzip`, `inflate`, `zstd`; defaults to `gzip` when `-compress` is set). |
| `-encrypt` | `false` | Enable encryption for cached artifacts. |
| `-encrypt-algo` | `""` | Encryption algorithm (`aes-256-cbc`; defaults to `aes-256-cbc` when `-encrypt` is set). |
| `-key-policy` | `Deterministic` | Encryption key policy (`Deterministic`, `RandomPerBlock`, `RandomAllKey`, `SuppliedAllKey`). |
| `-key` | `""` | Hex-encoded 32-byte key for `SuppliedAllKey`. |
| `-log` | `true` | Enable diagnostic logging. |

If discovery or remote `kv-v1`/`storage-v1` services are unavailable, `inv-go-build-cache` falls back to a local `FileSystemStorage` in `<cache-dir>/storage` and an in-memory KV store.

## Architecture

1. **In-Memory LRU Cache**: Fast lookup for hot action and output IDs (default capacity `100000`).
2. **Local Disk Storage**: Direct disk path (`<cache-dir>/<hex(ActionID)>`) for instant compiler reuse.
3. **Invariant Key-Value Store**: Distributed index mapping `go-build-cache:<hex(ActionID)>` to JSON `ActionEntry` (`output_id`, `content_link`, `size`, `time`).
4. **Invariant Storage Service**: Content-addressed block storage with optional compression and encryption.

On `put`, the artifact is written synchronously to local disk so the compiler can proceed immediately, while CAS + KV uploads run asynchronously and are flushed on `close`.

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
{"ID":1,"Command":"get","ActionID":"<base64-encoded-action-id>"}
```

**Response (Hit)**:
```json
{
  "ID": 1,
  "OutputID": "<base64-encoded-output-id>",
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
{"ID":2,"Command":"put","ActionID":"<base64-action-id>","OutputID":"<base64-output-id>","BodySize":1024}
```

Followed immediately by a second line containing the JSON-encoded binary payload.

**Response**:
```json
{"ID":2,"DiskPath":"/path/to/cache/file"}
```

### `close` Command

Flushes all background uploads (`wg.Wait()`) and exits cleanly.

**Request**:
```json
{"ID":3,"Command":"close"}
```

**Response**:
```json
{"ID":3}
```
