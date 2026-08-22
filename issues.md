# Invariant Codebase Issues

This file tracks issues and defects identified during code reviews of the Invariant codebase, along with their status, severity, and resolution commits.

## Phase 1: Foundation, Content Addressing & Storage Engines

| Issue ID | Severity | Status | Subsystem | Description |
| :--- | :--- | :--- | :--- | :--- |
| **ISSUE-P1-01** | High | Fixed | `internal/content` | `blockListReader` in-flight fetch error swallowing and silent chunk dropping |
| **ISSUE-P1-02** | High | Fixed | `internal/storage` | Path traversal vulnerability via unvalidated address paths in `FileSystemStorage` |
| **ISSUE-P1-03** | Medium | Fixed | `internal/discovery` | `DiscoveryServer` queries with `count <= 0` default to `1` instead of returning all matching services |
| **ISSUE-P1-04** | Medium | Fixed | `internal/discovery` | Timing flake in `TestFileSystemDiscovery` under race detector due to `time.Sleep` snapshotting |
| **ISSUE-P1-05** | Low | Fixed | `internal/finder` | `reflect.DeepEqual` usage in `finder/server_test.go` violating `AGENTS.md` rules |
| **ISSUE-P1-06** | Medium | Fixed | `internal/storage` | Unbuffered `io.Pipe()` in `CachingStorage` block promotion causes synchronous read blocking |
| **ISSUE-P1-07** | Low | Fixed | `internal/notify` | Zero test coverage for `internal/notify` client |
| **ISSUE-P1-08** | Low | Fixed | `docs` / `content` | Missing `zstd` algorithm specification in `docs/Content.md` |

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

---

## Phase 2: Mutable State, Naming, Journaling & Transactional KV Store

| Issue ID | Severity | Status | Subsystem | Description |
| :--- | :--- | :--- | :--- | :--- |
| **ISSUE-P2-01** | Low | Fixed | `internal/names` | `reflect.DeepEqual` and `"reflect"` import in `dns_client_test.go` violating `AGENTS.md` |
| **ISSUE-P2-02** | Medium | Fixed | `internal/kv` | Missing `X-Transaction-ID` header on `POST /tx/start` and `POST /tx/checkpoint` responses |
| **ISSUE-P2-03** | Medium | Fixed | `internal/kv` | `LoadLocalJournals` in `internal/kv/journal.go` reads non-journal files like `id` |
| **ISSUE-P2-04** | Low | Fixed | `internal/identity` | Zero test coverage for `internal/identity` client package |
| **ISSUE-P2-05** | Low | Fixed | `internal/slots` & `internal/names` | Missing `Snapshot() error` method on `FileSystemSlots` and `FileSystemNames` |

---

### [ISSUE-P2-01] Reflection Usage in `dns_client_test.go`
- **Location**: `internal/names/dns_client_test.go`
- **Severity**: Low
- **Description**: `dns_client_test.go` imports `"reflect"` and calls `reflect.DeepEqual`, violating `AGENTS.md` rules against reflection.
- **Resolution**: Replace with `slices.Equal` and direct struct field comparison.

### [ISSUE-P2-02] Missing `X-Transaction-ID` Header in `POST /tx/start` and `POST /tx/checkpoint`
- **Location**: `internal/kv/server.go`
- **Severity**: Medium
- **Description**: `docs/KeyValue.md` requires `POST /tx/start` and `POST /tx/checkpoint` to return the transaction ID in the `X-Transaction-ID` response header. Currently only returned in JSON body.
- **Resolution**: Set `X-Transaction-ID` header on both transaction endpoints.

### [ISSUE-P2-03] `LoadLocalJournals` Scanning Non-Journal Files
- **Location**: `internal/kv/journal.go`
- **Severity**: Medium
- **Description**: `LoadLocalJournals` iterates over all directory entries in `j.baseDir` without verifying the filename pattern `journal-*.jsonl`.
- **Resolution**: Add prefix and suffix validation to only parse valid journal files.

### [ISSUE-P2-04] Zero Unit Test Coverage for `internal/identity`
- **Location**: `internal/identity/`
- **Severity**: Low
- **Description**: Package `internal/identity` has 0 test files and 0% unit test coverage.
- **Resolution**: Add `internal/identity/client_test.go` covering HTTP ID retrieval, URL scheme handling, and error conditions.

### [ISSUE-P2-05] Missing `Snapshot() error` on `FileSystemSlots` and `FileSystemNames`
- **Location**: `internal/slots/fs_slots.go`, `internal/names/fs_names.go`
- **Severity**: Low
- **Description**: `FileSystemSlots` and `FileSystemNames` wrap `journal.Store` but lacked explicit `Snapshot() error` delegation for clean synchronous snapshot triggers.
- **Resolution**: Add `Snapshot() error` delegating to `store.Snapshot()`.

---

## Phase 3: File System Abstractions, Workspace & File Trees

| Issue ID | Severity | Status | Subsystem | Description |
| :--- | :--- | :--- | :--- | :--- |
| **ISSUE-P3-01** | Medium | Fixed | `internal/filetree` | Missing duplicate entry name validation in `Directory.Validate()` |
| **ISSUE-P3-02** | Low | Fixed | `internal/files` | Context cancellation ignored in `handleSync` using `context.Background()` instead of `r.Context()` |

---

### [ISSUE-P3-01] Missing Duplicate Entry Name Validation in `Directory.Validate()`
- **Location**: `internal/filetree/filetree.go`
- **Severity**: Medium
- **Description**: `Directory.Validate()` validates individual entries but fails to check whether multiple entries in the same directory share the same name.
- **Resolution**: Track seen names in `Directory.Validate()` and return an error on duplicates. Add unit test in `filetree_test.go`.

### [ISSUE-P3-02] Context Cancellation Ignored in `handleSync`
- **Location**: `internal/files/server.go`
- **Severity**: Low
- **Description**: In `handleSync`, `s.files.Sync(context.Background(), nodeID, wait)` ignores the incoming HTTP request context, preventing cancellation propagation.
- **Resolution**: Pass `r.Context()` to `s.files.Sync`.

---

## Phase 4: Mount Drivers, FUSE, NFS & Build Cache Integration

| Issue ID | Severity | Status | Subsystem | Description |
| :--- | :--- | :--- | :--- | :--- |
| **ISSUE-P4-01** | Medium | Fixed | `internal/buildcache` | Incomplete/empty `ContentLink` written to KV store when storage upload fails in `handlePut` |
| **ISSUE-P4-02** | Low | Fixed | `internal/fuse` | Minimal unit test coverage for `internal/fuse` filesystem operations |

---

### [ISSUE-P4-01] BuildCache Background Worker Poisons KV Store on Storage Upload Failures
- **Location**: `internal/buildcache/cache.go`
- **Severity**: Medium
- **Description**: In `Handler.handlePut`, if `content.Write` fails to upload to storage, the background worker proceeds to store an entry with an empty `ContentLink` in the KV store.
- **Resolution**: Check the return error of `content.Write` and skip writing to `h.cfg.KVStore` if the storage upload failed.

### [ISSUE-P4-02] Minimal Unit Test Coverage for `internal/fuse` File Operations
- **Location**: `internal/fuse/fuse_test.go`
- **Severity**: Low
- **Description**: `internal/fuse/fuse_test.go` only tests root node creation and initial `Getattr`, leaving core filesystem operations untested.
- **Resolution**: Add unit tests for `Create`, `Write`, `Read`, `Mkdir`, `Symlink`, `Readlink`, `Rename`, `Setattr`, and `Unlink`.

---

## Phase 5: CLI, Daemons, Cluster & Distribution

| Issue ID | Severity | Status | Subsystem | Description |
| :--- | :--- | :--- | :--- | :--- |
| **ISSUE-P5-01** | Low | Fixed | `internal/config` | Zero test coverage for `internal/config` directory resolution and YAML loader |
| **ISSUE-P5-02** | Low | Fixed | `internal/httputil` | Zero test coverage for `internal/httputil` diagnostic client transport wrapper |

---

### [ISSUE-P5-01] Zero Test Coverage for `internal/config`
- **Location**: `internal/config/`
- **Severity**: Low
- **Description**: `internal/config` manages config and cache directories and global configuration loading, but lacked automated test coverage.
- **Resolution**: Add unit tests in `internal/config/config_test.go`.

### [ISSUE-P5-02] Zero Test Coverage for `internal/httputil`
- **Location**: `internal/httputil/`
- **Severity**: Low
- **Description**: `internal/httputil/client.go` wraps HTTP transports with diagnostic logging for long requests, but lacked unit tests.
- **Resolution**: Add unit tests in `internal/httputil/client_test.go`.




