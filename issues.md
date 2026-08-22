# Invariant Codebase Issues

This file tracks issues and defects identified during code reviews of the Invariant codebase, along with their status, severity, and resolution commits.

## Phase 1: Foundation, Content Addressing & Storage Engines

| Issue ID | Severity | Status | Subsystem | Description |
| :--- | :--- | :--- | :--- | :--- |
| **ISSUE-P1-01** | High | Fixed | `internal/content` | `blockListReader` in-flight fetch error swallowing and silent chunk dropping |
| **ISSUE-P1-02** | High | Open | `internal/storage` | Path traversal vulnerability via unvalidated address paths in `FileSystemStorage` |
| **ISSUE-P1-03** | Medium | Open | `internal/discovery` | `DiscoveryServer` queries with `count <= 0` default to `1` instead of returning all matching services |
| **ISSUE-P1-04** | Medium | Open | `internal/discovery` | Timing flake in `TestFileSystemDiscovery` under race detector due to `time.Sleep` snapshotting |
| **ISSUE-P1-05** | Low | Open | `internal/finder` | `reflect.DeepEqual` usage in `finder/server_test.go` violating `AGENTS.md` rules |
| **ISSUE-P1-06** | Medium | Open | `internal/storage` | Unbuffered `io.Pipe()` in `CachingStorage` block promotion causes synchronous read blocking |
| **ISSUE-P1-07** | Low | Open | `internal/notify` | Zero test coverage for `internal/notify` client |
| **ISSUE-P1-08** | Low | Open | `docs` / `content` | Missing `zstd` algorithm specification in `docs/Content.md` |

---

## Detailed Issue Descriptions

### [ISSUE-P1-01] `blockListReader` In-Flight Fetch Error Swallowing
- **Location**: `internal/content/reader.go`
- **Severity**: High
- **Description**: In `blockListReader.loadBlock(targetIdx)`, when a block fetch is already in flight on another goroutine (`r.inFlight[targetIdx]`), waiting readers wait on `<-ch`. Once `ch` closes, `loadBlock` calls `r.updateLRU(targetIdx)` and unconditionally returns `nil`. If the in-flight fetch failed, `r.cache[targetIdx]` is nil, causing `Read()` to advance `r.currentPos` past the missing block and silently drop file contents instead of returning an error.
- **Resolution**: Check `r.cache[targetIdx]` after `<-ch` resolves. Store the error returned by the in-flight fetch or retry loading if the cache entry was not populated.

### [ISSUE-P1-02] Path Traversal in `FileSystemStorage` via Unvalidated Addresses
- **Location**: `internal/storage/fs_storage.go`, `internal/storage/server.go`
- **Severity**: High
- **Description**: `addressToPath(address)` constructs filesystem paths using `filepath.Join(s.baseDir, address[0:2], address[2:4], address[4:])`. If an untrusted request provides `GET /../../../../etc/passwd` or `PUT /../../secret`, `filepath.Join` resolves the path outside `baseDir`.
- **Resolution**: Validate that `address` is a valid hexadecimal string and enforce that all resolved paths remain strictly within `baseDir`.

### [ISSUE-P1-03] `DiscoveryServer` Limits `count <= 0` Queries to 1 Result
- **Location**: `internal/discovery/server.go`, `internal/discovery/client.go`
- **Severity**: Medium
- **Description**: `DiscoveryServer.handleFind` parses `count` and defaults to `count = 1` whenever `count <= 0` or omitted. In `FileSystemDiscovery` and `InMemoryDiscovery`, `count <= 0` signifies unlimited matching results. Consequently, remote HTTP clients querying with `count = 0` only ever receive 1 service description.
- **Resolution**: Update `handleFind` to treat `count=0` or omitted count as unlimited (`count = 0`).

### [ISSUE-P1-04] Timing Flake in `TestFileSystemDiscovery` under Race Detector
- **Location**: `internal/discovery/fs_discovery_test.go`, `internal/journal/journal.go`
- **Severity**: Medium
- **Description**: `TestFileSystemDiscovery` sleeps for 100ms waiting for the background snapshot timer in `journal.Store` to trigger before calling `Close()`. Under `-race`, 100ms is not enough time, causing assertion failure `expected snapshot.json to be created`.
- **Resolution**: Add explicit snapshot flushing on `Store.Close()` or provide an explicit `Sync()`/`Snapshot()` method on `journal.Store`.

### [ISSUE-P1-05] `reflect.DeepEqual` Usage in `finder/server_test.go`
- **Location**: `internal/finder/server_test.go`
- **Severity**: Low
- **Description**: `finder/server_test.go` imports `"reflect"` and uses `reflect.DeepEqual`, which violates the `AGENTS.md` coding standard against reflection.
- **Resolution**: Replace `reflect.DeepEqual` with `slices.Equal` or explicit comparison.

### [ISSUE-P1-06] Unbuffered `io.Pipe()` Head-of-Line Blocking in `CachingStorage` Promotion
- **Location**: `internal/storage/caching_storage.go`
- **Severity**: Medium
- **Description**: When `CachingStorage.Get` promotes a block from remote destination to local storage, it pipes the stream through an unbuffered `io.Pipe()`. If local storage writes are slow or blocked on locks, consumer `Read()` calls block synchronously.
- **Resolution**: Decouple the local caching write from the foreground reader using an asynchronous or buffered transfer.

### [ISSUE-P1-07] Missing Unit Tests for `internal/notify`
- **Location**: `internal/notify/client.go`
- **Severity**: Low
- **Description**: `internal/notify` has 0% direct statement coverage.
- **Resolution**: Add `internal/notify/client_test.go` to test successful notification, server errors, and client options.

### [ISSUE-P1-08] Missing `zstd` Specification in `docs/Content.md`
- **Location**: `docs/Content.md`
- **Severity**: Low
- **Description**: `docs/Content.md` documents only `inflate` and `gzip` decompression algorithms, omitting `zstd` which was added in `internal/content`.
- **Resolution**: Update `docs/Content.md` to include `zstd` in the `DecompressTransform` specification.
