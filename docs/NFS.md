# The invariant project - NFS Server (`internal/nfs`)

`invariant nfs` exports an Invariant file tree over a native NFSv3 server (`internal/nfs`) without requiring FUSE kernel support.

## Usage

```bash
invariant nfs [-listen :2049] [mount-flags]
```

- `-listen`: TCP listen address (default `:2049`).
- Supports all common mount flags (`-discovery`, `-root`, `-slot`, `-cache`, `-disk-cache`, `-cache-dir`, `-overflow-dir`, `-compress`, `-encrypt`, `-key-policy`, `-key`, `-auto-sync`).
- Shuts down cleanly on `SIGINT` or `SIGTERM`.

## Implementation Details

- Uses `github.com/willscott/go-nfs` wrapped with `nfshelper.NewNullAuthHandler` and `nfshelper.NewCachingHandler(..., 1024)`.
- Implements `billy.Filesystem`, `billy.Change`, and `billy.Symlink` backed by `internal/files` (`root` node `1`).
- **Reads**: Streamed directly via `files.Files.ReadFile`.
- **Writes**: On the first write or truncate, the file is staged in an unlinked temporary file (`os.CreateTemp("", "invariant-nfs-*")`), copying existing contents unless `O_TRUNC` is set, and flushed back to `files.Files.WriteFile` on `Close()`.
- `TempFile` is unsupported; `Chown`/`Lchown` and `Lock`/`Unlock` are no-ops.
