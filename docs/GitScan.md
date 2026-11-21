# The invariant project - Git Scanner (`internal/gitscan`)

`invariant scan-repo` (`internal/gitscan`) indexes Git blob SHA-1 hashes to Invariant SHA-256 content hashes in the Key-Value service (`kv-v1`). This allows `inv-storage` (`-git-dir`), `GitHubStorage`, and `invariant repository git import` / `git export` to resolve Git objects without re-uploading or re-hashing unchanged blobs.

## Usage

```bash
# Scan a local Git repository
invariant scan-repo -local /path/to/repo -commit HEAD [-depth -1] [-discovery http://localhost:3003]

# Scan a remote GitHub repository
invariant scan-repo -owner <org> -repo <name> -commit <sha-or-ref> [-token <github-token>] [-depth 1]
```

### Flags

| Flag | Default | Description |
| ---- | ------- | ----------- |
| `-commit` | `""` | Starting commit hash or ref (required). |
| `-local` | `""` | Path to local Git repository (mutually exclusive with `-owner`/`-repo`). |
| `-owner` | `""` | GitHub repository owner. |
| `-repo` | `""` | GitHub repository name. |
| `-token` | `""` | GitHub API bearer token (for remote scans). |
| `-depth` | `1` | Commit traversal depth (`-1` for unlimited). |
| `-kv` | `""` | Key-Value service URL (resolved via discovery if omitted). |
| `-discovery` | `""` | Discovery service URL. |
| `-concurrency` | `20` | Worker concurrency for hashing/fetching blobs. |

## KV Key Schema

`gitscan` stores bidirectional mappings and tree-skip markers in batches of `200`:

- `SHA1:<git-sha1>` -> `<invariant-sha256>`
- `SHA256:<invariant-sha256>` -> `<git-sha1>`
- `tree:sha1:<git-tree-sha1>` -> `"scanned"` (used to skip already-indexed subtrees)
