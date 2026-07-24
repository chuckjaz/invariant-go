# Performance Optimization Tasks for `invariant upload`

- [x] **Task 1: Short-circuit MTime Cache Hits**
  - Skip remote `store.Has()` network checks in `cmd/invariant/upload.go` when a file's modification time (`mtime`) matches the local upload cache entry (`u.cache`).
- [x] **Task 2: Optimize Batching Timer Delays**
  - Reduce or eliminate artificial batching timer delays in `cmd/invariant/batching_storage.go` when queued items are below the 100-item threshold.
- [x] **Task 3: Single-Pass Upload Processing**
  - Refactor file upload processing in `cmd/invariant/upload.go` to eliminate double-pass file reading, splitting, and hashing.
- [x] **Task 4: Tune Concurrency and Worker Pool**
  - Replace the fixed 10,000 worker goroutine pool with a bounded pool aligned with system CPU/IO capacity to reduce lock contention and scheduler overhead.
- [ ] **Task 5: Parallelize Storage Node Queries in `BatchHas`**
  - Update `internal/storage/aggregate_client.go` to perform `BatchHas` requests across backend storage nodes in parallel rather than sequentially.
