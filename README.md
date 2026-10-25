# The invariant project

A content addressing storage system that can be used to store abitrary data. Data can be uploaded to the storage system and retrieved using a content link. The storage system is built on top of a distributed storage system that can be scaled arbitrarily.

The system is made up of multiple micro-services that can be run independently or together and replaced or updated arbitrarily.

### File System
A directory can be uploaded to the storage system and then mounted locally via FUSE or exported natively as an NFS server. The directory can then be interacted with as a regular file system. The file system can be mounted on mutliple machines and will automatically sync from one machine to another.

The file system can be layered, which allows for files in a dirctory to go to different storage devices or cloud storage providers. For example, a development directory is often made up of source files and built files. The source files are usually stored in some subdirectory of the project directory. The source files can cached locally, replicated to a network storage device, and always backed up to the cloud. The files could be stored only locally. This prevents files that can be rebuilt taking up space on the cloud storage provider.

### Key-Value store
A key-value store is built on top of the storage service. It provides a mechanism to store and retrieve arbitrary data. The key-value store is backed by an immutable journal, an immutable B-Tree index, and an in-memory cache. 

### Version Control Repository
A distributed version control and code review system ([protocol and command description](docs/Repository.md)) is built natively on top of Invariant's content-addressable storage, mutable slots, names service, and layered FUSE workspaces. Commits, file trees, and blobs are stored directly in CAS rather than a local `.git` directory, and branches and change sets are mounted as isolated workspaces. It supports branching, 3-way merges and rebasing, integrated code reviews, and bidirectional synchronization with Git repositories.

### Automatic backup and recovery
The system can be configured to automatically upload the file system to cloud storage providers such as AWS S3, Google Cloud Storage, or Azure Blob Storage. The system can be configured to compress and encrypt the data before uploading it to the cloud storage provider as well as only storing encyrpted copies of the files at rest.

If the local storage device fails, the system can be configured to automatically recover from the cloud storage provider incrementally. The files are then cached locally as they are read from the cloud storage provider.

### Replication
When storing files locally, either on the same machine, or via a connected storage device, the data can be distributed amoung multiple physical storage devices to improve redundancy and availability. These devices can be added and removed from the system arbitrarily which will redistribute the data among the remaining devices or populate a new device with replications of the data. Data will not be lost if a storage device fails unless the total number of storage devices falls below the replication factor. If the data is backed up to the cloud, the data will not be lost if the local storage device fails. The data will be read from the cloud storage provider not cached locally.

## Microservices

The file system and key-value are built on microservices. Here is a list of the services and their descriptions.

| Service | Command | Description |
|---------|---------|-----|
| Storage | `inv-storage` | The storage service is the primary service that is used to store and retrieve data. It is a content addressed storage system that is built on top of a distributed storage system that can be scaled arbitrarily. |
| Finder | `inv-finder` | The finder service is responsible for finding data among multiple storage devices. |
| Distribute | `inv-distribute` | The distribute service is responsible for distributing data among multiple storage devices. |
| Discovery | `inv-discovery` | The discovery service allows service detection, service registration, and service delegation. |
| Slots | `inv-slots` | The slots service is responsible for allocating and managing slots. Slots are updateable addresses that maintain the current address of the root of a file system or the key-value store journal. |
| Names | `inv-names` | The names service is a naming layer that acts like a local DNS. It also can use DNS directly. The names service allow mapping names to IDs. Examples of mappings are a device ID to a human readable name, or a slot ID to a service name. |
| Files | `inv-files` | A file services that is the backing service for the FUSE file system and the NFS service. |
| KeyValue | `inv-kv` | A key-value store service that allows storing and retrieving values for a given key. |

Both the File and KeyValue service are privleged services as both services initized with the root content-link for all the data reachable by the service. If the data is encrypted, for example, they have access to the root link key, transitviely, all the other keys for the data. The Files service should only be run on the system that mounts the file system or the NFS export. The other services are unprivileged. They do not have access to the unecrypted data.

## Executing services

Many of the services support dynamic port binding if you omit the `-port` flag or provide `-port 0`. This is useful for avoiding port conflicts when running multiple instances.

### Discovery Service (`inv-discovery`)
To run the discovery server ([protocol description](docs/Discovery.md)), which defaults to port `3003`:
```bash
# Run with in-memory discovery storage
go run ./cmd/inv-discovery -port 3003

# Run with upstream delegation to another discovery service
go run ./cmd/inv-discovery -port 3003 -upstream http://upstream:3003
```

### Names Service (`inv-names`)
The names service ([protocol description](docs/Names.md)) provides a mechanism to bind logical string names to 64-character IDs. It can be run in memory or backed by the file system.
```bash
# Run with in-memory names storage
go run ./cmd/inv-names -port 3005 -discovery http://localhost:3003

# Run with persistent snapshot/journal names storage
go run ./cmd/inv-names -port 3005 -dir /tmp/names -discovery http://localhost:3003

# Run with upstream delegation to another name service
go run ./cmd/inv-names -port 3005 -upstream http://upstream:3005 -discovery http://localhost:3003
```

### Storage Service (`inv-storage`)
The storage service ([protocol description](docs/Storage.md)) is responsible for storing and retrieving blobs of data.
```bash
# Run with in-memory storage naitvely
go run ./cmd/inv-storage -port 3000

# Run with AWS S3 backend
go run ./cmd/inv-storage -port 3000 -s3-bucket my-bucket -s3-prefix invariant-blocks/

# Run with persistent nested file system blocks and register with discovery & distribute services
go run ./cmd/inv-storage -port 3000 -dir /tmp/blocks -discovery http://localhost:3003 -distribute distribute-1 -notify notify-service-id
```
*(Note: The `-notify` flag points to IDs implementing the Notify protocol.)*

### Distribute Service (`inv-distribute`)
The distribute server ([protocol description](docs/Distribute.md)) coordinates block replication logic. It can pull available names/IDs from the discovery service.
```bash
# Run with default in-memory distribution logic
go run ./cmd/inv-distribute -port 3001

# Run with a specific replication factor and connect to discovery
go run ./cmd/inv-distribute -port 3001 -N 3 -discovery http://localhost:3003 -name distribute-1

# Run with a backup destination (resolved automatically via discovery) and rate limit (MB/hour)
# The destination is excluded from standard replication
go run ./cmd/inv-distribute -port 3001 -destination backup-storage-id -backup-rate 100
```

### Finder Service (`inv-finder`)
The finder server ([protocol description](docs/Finder.md)) manages Kademlia routing logic, interacting via the Peer protocol.
```bash
# Start a standalone finder
go run ./cmd/inv-finder -port 3002

# Connect the finder to the discovery service
go run ./cmd/inv-finder -port 3002 -discovery http://localhost:3003
```

### Slots Service (`inv-slots`)
A service to allocate and manage mutable slots. It can also notify other services.
```bash
go run ./cmd/inv-slots -port 3004 -discovery http://localhost:3003 -notify notify-service-id
```

### Invariant CLI Utility
The `invariant` utility is the main client and orchestrator for the system. It reads global configuration from `~/.invariant/config.yaml` and provides subcommands for cluster interaction:

- `provision`: Bootstrap the control plane and provision service tokens (`service.token`) and discovery addresses (`discovery.url`) for all services defined in a YAML configuration file.
  - Supports `--install` to automatically install the latest versions of any missing service commands via `go install` if `go` is available on `PATH`.
- `start`: Start services locally defined in a YAML configuration file.
- `invite`: Create, redeem, list, or revoke control-plane invitation codes (`create --role <user|service|admin>`, `redeem <inv_invite_...@itp://...>`, `list`, `revoke <id>`).
- `token`: List or revoke active control-plane access tokens (`list`, `revoke <id>`).
- `login`: Authenticate with the OAuth2 provider to obtain user credentials (`~/.invariant/oauth_token.json`).
- `slot`: Allocate a new slot from the slots service.
  - Supports `--protected` to generate a 256-bit elliptic curve (Ed25519) key pair, using the 32-byte public key as the slot ID and storing the private key in `~/.invariant/keys/`.
- `name`: Register a logical name to a slot.
- `lookup`: Look up a registered name to get its corresponding ID or address.
- `nfs`: Start the invariant file system as a completely native NFS Server.
  - Listen on a specific port (e.g., `--listen :2049`).
  - Supports `--compress`, `--encrypt`, `--key-policy`, and `--key` flags for configuring writing of new files to the mount.
- `mount`: Mount the invariant file system locally via FUSE (supports dynamic `.invariant-layer` reloading, name-to-address resolution, optimized read/write caching, and merging remote changes into local nested/dirty directories).
  - Supports `--compress`, `--encrypt`, `--key-policy`, and `--key` flags for configuring writing of new files to the mount.
- `upload`: Upload a local directory to invariant storage as a file tree, preserving file creation and modification times, and automatically splitting zip files.
  - Supports `--compress` and `--encrypt`.
  - Supports `--key-policy` (e.g. `Deterministic` (default), `RandomPerBlock`, `RandomAllKey`, `SuppliedAllKey`), with `--key` for supplying your own 32-byte hex key.
  - Supports `--slot <hex_id_or_name>` to automatically update a mutable slot (resolved by ID or name) to point to the new content tree on successful upload.
  - Supports `--prev <hex_id>` to supply the parent payload state if the local slot cache (`~/.invariant/slots/`) is empty.
  - Supports `--dry-run` to compute stats and short-circuit actual block uploads.
  - Supports `--stats` to print total bytes, uploaded blocks, and created directories to standard error.
- `workspace`: Handle layered workspaces that separate persistent core network files from transient local-only temporary environments.
  - `create <directory> <content_link|slot_name>`: Initializes a local directory referencing `.invariant-layer` metadata routing transient local data away from primary data scopes securely. The `<content_link|slot_name>` can be dynamically resolved via the names service. Supports `--protected`, `--layers`, and `--create-only`.
  - `mount <directory>`: Parses the `.invariant-workspace` and mounts the virtual composite layered file system locally via FUSE natively inheriting standard disk caching (`~/.cache/invariant`) and offline overflow. Runs as a background daemon by default, or supports `-foreground`.
  - `unmount <directory>`: Unmounts the current layered workspace.
  - `pull [directory]`: Parses the workspace and caches all required source layer blocks directly into the local disk storage natively utilizing concurrency limits and `~/.cache/invariant/overflow` validation fallbacks.
- `repository` (or `repo`): Manage Invariant version control repositories ([detailed documentation](docs/Repository.md)).
  - **Primary workflow**:
    - `change <name>`: Create a writable change branch workspace branched from upstream (supports `-private`, `-upstream`, and `-tag`).
    - `status`: Show working tree file modifications, additions, and deletions relative to the current `HEAD` commit.
    - `diff [<commit1>] [<commit2>]`: Produce unified diffs of workspace changes or between commits (supports `--stat`).
    - `sync`: Synchronize and rebase the active change workspace onto the latest upstream `HEAD` via 3-way merge (supports `--continue`, `--abort`, and `-tag`).
    - `log [<path>]`: Display the commit history log (supports `-tree` / `-graph` for full DAG history and `-n <count>`).
    - `commit`: Snapshot and commit workspace changes to the current branch slot (supports repeatable `-m <msg>`, `--amend`, `--squash <commit>`, and `-tag`).
    - `review`: Manage code reviews, inline comments, and approvals:
      - `request [<directory>]`: Request a code review on the active change branch and emit a review token.
      - `open <sha|token|name>`: Open a review workspace for inspection without changing its status (supports `-d <dir>` and `-writable`).
      - `start [<sha|token|name>]`: Transition a review to `in_progress` and open/mount its workspace (supports `-d <dir>`, `-writable`, and `-reviewer`).
      - `comment [<comment-file>]`: Add structured review comments via a JSON file or flags (`-token`, `-m`, `-file`, `-line`, `-end-line`, `-author`).
      - `comments [<identifier>]`: Display review comments formatted as markdown with code context or raw JSON (`-json`).
      - `approve [<identifier>]`: Approve a code review and retire the review workspace.
      - `reject [<identifier>]`: Reject a code review and retire the review workspace.
      - `abandon [<identifier>]`: Abandon a code review and retire the review workspace.
      - `update [<identifier>]`: Update an existing review record with the latest committed changes on the branch.
    - `submit [<directory>]`: Fast-forward/push changes from the change workspace to the target upstream branch (default `main`) and retire the workspace (supports `-target` and `-tag`).
    - `show [<commit>[:<path>]]`: Inspect commit metadata and diffs, or view a file's contents at a specific commit snapshot.
  - **Initialization and configuration**:
    - `create <name> [<content>]`: Create a new repository, register it with the Names Service, and initialize the `main` branch (supports `-content`, `-d`, `-tag`, `-create-only`, `-encrypt`, `-compress`, `-writable`, and `-readonly` / `-read-only`).
    - `open <name> [<directory>]`: Open an existing repository from the Names Service into a layered FUSE workspace (supports `-b` / `-branch`, `-d` / `-directory`, `-layers`, `-create-only`, `-foreground`, `-writable`, and `-tag`).
    - `config <get|set|unset|list>`: Manage repository-scoped or global user (`--global`) configuration settings.
  - **Branch management**:
    - `branch [list|delete <name>]`: List local workspaces, upstream branches, and discovered peer branches, or delete a branch.
    - `checkout <branch|peer-branch>`: Switch to or mount a local, upstream, or peer-published change branch (supports `-writable`).
    - `cherry-pick <branch|commit> [<end-commit>]`: Apply commits from another branch or commit range onto the current workspace (supports `-tag`).
    - `rebase [-i] [<upstream>]`: Interactively edit, reorder, squash, or drop commits in a change branch.
    - `tag [create <name> [<commit>]|list|delete <name>]`: Create, list, or delete immutable release tags.
  - **Workspace management**:
    - `clean`: Purge untracked and temporary files from the workspace (supports `-f`, `-d`, and `-x`).
    - `layer <add <repo_name> <mount_path> [--commit=<sha>]|list|remove <mount_path>>`: Manage pinned sub-repository dependency layers.
    - `restore [<path>]`: Discard uncommitted local edits and restore files from a commit snapshot (supports `-source <commit>`, defaults to `HEAD`).
    - `revert <commit>`: Apply the inverse patch of a commit and record a new revert commit (supports `-tag`).
    - `stash [push [-m <msg>]|pop|list|drop [<index>]]`: Shelve and restore uncommitted local changes to/from temporary CAS commits.
    - `mount <name> [<directory>]`: Mount an existing repository workspace.
    - `unmount [<directory>]`: Unmount a repository workspace and its nested workspaces.
  - **Diagnostics**:
    - `blame <file>`: Show line-by-line commit and author attribution for a file (supports `-commit <hash>`).
    - `grep <pattern> [<path>]`: Search for patterns across commit trees in CAS (supports `-i`, `-n`, and `-commit <hash>`).
    - `bisect <start [<bad> [<good>]]|bad [<commit>]|good [<commit>]|reset|run <cmd> [args...]>`: Perform binary search across commit history to isolate regressions.
  - **Import / Export**:
    - `git import [<git-dir>]`: Import commit history and file trees from a Git repository into Invariant CAS (supports `-repository` / `-repo`, `-branch`, `-directory` / `-d`, `-depth`, `-writable`, and `-tag`).
    - `git export <target-git-dir>`: Export Invariant commits and file trees to a Git repository (supports `-branch` and `-from <commit>`).
- `print`: Print a block's contents to standard output. Supports ContentLink JSON input directly or via pipe. Subpath traversal is also supported (e.g., `invariant print <content-link>/path/to/file`). Supports `-s` to treat the target hash code or name as a mutable slot.

```bash
# Provision service tokens and start services defined in services.yaml
go run ./cmd/invariant provision --install services.yaml
go run ./cmd/invariant start services.yaml
```

**Example `services.yaml`**:
```yaml
common:
  discovery:
    discovery: "http://localhost:3003"
    advertise: "http://localhost"
  distribute:
    distribute: "distribute-1"

services:
  - command: inv-discovery
    args:
        port: 3003

  - command: inv-names
    use: [discovery]
    args:
        dir: "*/names"

  - command: inv-distribute
    use: [discovery]
    args:
        name: "distribute-1"

  - command: inv-storage
    use: [discovery, distribute]
    args:
        dir: "*/storage-1"
        notify: "distribute-1"

  - command: inv-storage
    use: [discovery, distribute]
    args:
        dir: "*/storage-2"
        notify: "distribute-1"
```

The `services.yaml` configuration also supports an `environment` map. Keys will overwrite container-local or service environment variables. When a value is prefixed with `$key:`, it safely substitutes the content of the secure key file (`~/.invariant/keys/<filename>`) preventing the secret from appearing inside the config format itself.

### Docker Compose
For a more robust and industrial-strength deployment, you can use Docker and Docker Compose. This is the recommended alternative for running these services in production-like environments or cross-platform setups. 

**Example `docker-compose.yml`** (equivalent to the `services.yaml` above):
```yaml
version: '3.8'

x-discovery-args: &discovery-args
  -discovery: http://discovery:3003
  -advertise: http://0.0.0.0

services:
  discovery:
    build: .
    command: ["./bin/inv-discovery", "-port", "3003"]
    ports:
      - "3003:3003"

  names:
    build: .
    command: ["./bin/inv-names", "-port", "3005", "-dir", "/data/names"]
    volumes:
      - ./data/names:/data/names
    depends_on:
      - discovery
    environment:
      # Inject discovery args via command override conceptually, or rely on internal entrypoint scripts
      # For simplicity, assumed inline here
    command: ["./bin/inv-names", "-port", "3005", "-dir", "/data/names", "-discovery", "http://discovery:3003", "-advertise", "http://0.0.0.0"]

  distribute:
    build: .
    command: ["./bin/inv-distribute", "-port", "3001", "-name", "distribute-1", "-discovery", "http://discovery:3003", "-advertise", "http://0.0.0.0"]
    depends_on:
      - discovery

  storage-1:
    build: .
    command: ["./bin/inv-storage", "-port", "3000", "-dir", "/data/storage-1", "-discovery", "http://discovery:3003", "-advertise", "http://0.0.0.0", "-distribute", "distribute-1", "-notify", "distribute-1"]
    volumes:
      - ./data/storage-1:/data/storage-1
    depends_on:
      - discovery
      - distribute

  storage-2:
    build: .
    command: ["./bin/inv-storage", "-port", "3002", "-dir", "/data/storage-2", "-discovery", "http://discovery:3003", "-advertise", "http://0.0.0.0", "-distribute", "distribute-1", "-notify", "distribute-1"]
    volumes:
      - ./data/storage-2:/data/storage-2
    depends_on:
      - discovery
      - distribute
```

## Installing the CLI and Services
To compile and install the `invariant` CLI and microservice commands (`inv-discovery`, `inv-names`, `inv-slots`, `inv-storage`, `inv-finder`, `inv-distribute`, `inv-kv`, `inv-files`) using the Go toolchain:
```bash
# Install the CLI and all microservices at once
go install github.com/chuckjaz/invariant-go/cmd/...@latest

# Or install individual commands
go install github.com/chuckjaz/invariant-go/cmd/invariant@latest
go install github.com/chuckjaz/invariant-go/cmd/inv-discovery@latest
go install github.com/chuckjaz/invariant-go/cmd/inv-names@latest
go install github.com/chuckjaz/invariant-go/cmd/inv-slots@latest
go install github.com/chuckjaz/invariant-go/cmd/inv-storage@latest
go install github.com/chuckjaz/invariant-go/cmd/inv-finder@latest
go install github.com/chuckjaz/invariant-go/cmd/inv-distribute@latest
go install github.com/chuckjaz/invariant-go/cmd/inv-kv@latest
go install github.com/chuckjaz/invariant-go/cmd/inv-files@latest
```

Ensure your Go binary directory (`$(go env GOPATH)/bin` or `$GOBIN`) is on your `PATH`. You can also pass `--install` to `invariant provision` (e.g., `invariant provision --install services.yaml`) to automatically install any missing service commands referenced in `services.yaml` using `go install`.

## Building binaries
To build all microservices into the `bin/` directory, use the supplied build script:
```bash
./build
```

## Running tests
To run continuous tests, execute standard go test coverage:
```bash
go test -v ./...
```