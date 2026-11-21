# The invariant project - Journal (`internal/journal`)

`internal/journal` provides persistent append-only journaling primitives used by Invariant services (`inv-names`, `inv-slots`, `inv-discovery`, `inv-kv`).

## Local JSONL Journal (`internal/journal`)

`Store[K, V]` persists key-value state to a local directory using:

- An append-only JSONL journal file for mutations.
- Periodic compaction into `snapshot.json` controlled by `snapshotInterval` (e.g., `-snapshot-interval`, default `1h` on `inv-names`, `inv-slots`, and `inv-discovery`).

## Chained CAS Journal (`internal/journal/chained`)

`ChainedJournal[T]` provides a durable, CAS-backed write-ahead journal used by stateful services such as `inv-kv` and `inv-discovery`:

- Buffers writes to local disk with group commit.
- Asynchronously uploads journal segments as blocks to `storage-v1`, chaining each segment to its predecessor via `PreviousJournal` (`ChainedHeader`).
- Updates a mutable slot (`slots-v1`) to point to the latest journal head.
