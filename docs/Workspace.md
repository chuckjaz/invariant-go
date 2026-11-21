# The invariant project - Workspace

An Invariant workspace is a layered directory that can be mounted locally and separates persistent source files from transient local-only or generated files. Source files are persisted in shared storage, while produced files (such as build artifacts) can be stored only locally and recreated at any time.

## `.invariant-workspace` Schema

A workspace directory contains a `.invariant-workspace` JSON file (`WorkspaceInfo`):

```ts
interface WorkspaceInfo {
    content: ContentLink;
    parent_snapshot?: string;
    parent_slot?: string;
}
```

## Using a Workspace

### Creating a Workspace (`invariant workspace create`)

```bash
invariant workspace create [-layers <l1,l2>] [-create-only] [-protected] <directory> <content>
```

- `<directory>`: Target directory to initialize.
- `<content>`: File tree 64-character hex address or a slot/content name resolved via `names-v1`. If it points to a file tree, a slot is created and substituted for the file tree address.
- `-layers`: Comma-separated list of additional layer names.
- `-create-only`: Initialize `.invariant-workspace` without mounting.
- `-protected`: Create an Ed25519 (`ecc`) protected slot.

### Mounting a Workspace (`invariant workspace mount`)

```bash
invariant workspace mount [mount-flags] [-foreground] [-systemd] <directory>
```

Mounts the workspace via FUSE (spawning a background daemon by default unless `-foreground` is passed). Supports the common mount flags:

- `-discovery <url>`: Discovery service URL.
- `-root <link>`: Root content link override.
- `-slot <id-or-name>`: Slot ID or name.
- `-cache <MB>`: In-memory cache size in MB (default `128`).
- `-disk-cache <MB>`: Disk cache size in MB (default `1024`).
- `-cache-dir <path>`: Disk cache directory (default `~/.cache/invariant`).
- `-overflow-dir <path>`: Overflow cache directory (default `~/.cache/invariant/overflow`).
- `-compress`: Compress newly written files.
- `-encrypt`: Encrypt newly written files.
- `-key-policy <policy>`: Encryption key policy (default `Deterministic`).
- `-key <hex>`: Hex encryption key for `SuppliedAllKey`.
- `-auto-sync <duration>`: Auto-sync interval (default `1m`).
- `-foreground`: Run FUSE mount in the foreground.

### Unmounting a Workspace (`invariant workspace unmount`)

```bash
invariant workspace unmount [-systemd] <directory>
```

Unmounts the FUSE filesystem via `fusermount -u` and waits for the background mount process to exit.

### Pre-populating Cache (`invariant workspace pull`)

```bash
invariant workspace pull [mount-flags] [directory]
```

Walks up from `directory` (default `.`) to find `.invariant-workspace` or `.invariant-layer[s]`, and concurrently (16 workers) downloads all files from the source layer into the local disk cache. Prompts for confirmation if the total size exceeds `-disk-cache`.

### Branching, Merging, and Rebasing Workspaces

- `invariant workspace branch [parent-directory] <branch-directory>`: Creates a new workspace slot branched from the parent workspace's current snapshot and records `parent_snapshot` and `parent_slot`.
- `invariant workspace merge [parent-directory] <branch-directory>`: Performs a 3-way `MergeTrees` from the branch back into the parent slot via CAS (exits with status `2` if merge conflicts occur).
- `invariant workspace rebase <parent-directory> [branch-directory]`: Performs a 3-way `MergeTrees` of upstream parent changes onto the branch slot and updates `parent_snapshot`.

## Layer Resolution Implementation

When `create` initializes `.invariant-layer`:

1. If the base tree contains `.invariant-share` (JSON array of layers), those layers are added first.
2. Any layers specified via `-layers` are added next (resolved first as `.invariant-<name>` in the base tree, otherwise via `names-v1`).
3. The source layer is added with `excludes` rules loaded from `.invariant-ignore` and `.gitignore`.
4. If any exclude rules exist, a writable `"local"` slot layer (`rootLink: "temporary"`, `storageDestination: "local"`) is appended for ignored/generated files.

Example `.invariant-layer`:

```json
[
    {
        "rootLink": { "address": "<slot>", "slot": true },
        "excludes": ["bin/"]
    },
    {
        "rootLink": "temporary",
        "storageDestination": "local"
    }
]
```
