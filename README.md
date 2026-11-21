# The invariant project

A content-addressed storage system that can be used to store arbitrary data. Data can be uploaded to the storage system and retrieved using a [ContentLink](docs/Content.md). The storage system is built on top of a distributed set of microservices that can be scaled arbitrarily.

The system is made up of multiple microservices that can be run independently or together and replaced or updated arbitrarily.

### File System
A directory can be uploaded to the storage system and then mounted locally via FUSE or exported natively as an [NFSv3 server](docs/NFS.md). The directory can then be interacted with as a regular file system. The file system can be mounted on multiple machines and will automatically sync from one machine to another.

The file system can be layered ([Workspace](docs/Workspace.md)), which allows files in a directory to go to different storage devices or cloud storage providers. For example, a development directory is often made up of source files and built files. The source files can be cached locally, replicated to a network storage device, and always backed up to the cloud, while generated files are stored only locally.

### Key-Value Store
A [key-value store](docs/KeyValue.md) is built on top of the storage service. It provides a mechanism to store and retrieve arbitrary data backed by an immutable [journal](docs/Journal.md), an immutable B-Tree index, and an in-memory cache.

### Version Control Repository
A distributed version control and code review system ([protocol and command description](docs/Repository.md)) is built on top of Invariant's content-addressable storage, mutable slots, names service, and layered FUSE workspaces. Commits, file trees, and blobs are stored directly in CAS rather than a local `.git` directory, and branches and change sets are mounted as isolated workspaces. It supports branching, 3-way merges and rebasing, integrated code reviews, and bidirectional synchronization with Git repositories.

### Automatic Backup and Recovery
The system can be configured to automatically upload the file system to cloud storage providers such as AWS S3, Google Cloud Storage, or Azure Blob Storage. The system can be configured to compress and encrypt the data before uploading it to the cloud storage provider as well as only storing encrypted copies of the files at rest.

If the local storage device fails, the system can be configured to automatically recover from the cloud storage provider incrementally. The files are then cached locally as they are read from the cloud storage provider.

### Replication
When storing files locally, either on the same machine or via a connected storage device, the data can be distributed among multiple physical storage devices to improve redundancy and availability. These devices can be added and removed from the system arbitrarily, which will redistribute the data among the remaining devices or populate a new device with replications of the data. Data will not be lost if a storage device fails unless the total number of storage devices falls below the replication factor.

## Microservices and Tools

The file system, key-value store, and build cache are built on microservices:

| Service | Command | Description |
| ------- | ------- | ----------- |
| Storage | `inv-storage` | Stores and retrieves content-addressed blobs ([Storage Protocol](docs/Storage.md)). Supports in-memory, filesystem, S3, Git, and GitHub backends. |
| Finder | `inv-finder` | Locates blocks among storage services using Kademlia routing ([Finder Protocol](docs/Finder.md)). |
| Distribute | `inv-distribute` | Replicates blocks across storage services ([Distribute Protocol](docs/Distribute.md)). |
| Discovery | `inv-discovery` | Service detection, registration, control plane (`itp-v1`), invitations, and service keys ([Discovery Protocol](docs/Discovery.md)). |
| Slots | `inv-slots` | Allocates and manages mutable slots for filesystem roots and KV journals ([Slots Protocol](docs/Slots.md)). |
| Names | `inv-names` | Maps logical names to service IDs or block addresses, with DNS TXT fallback ([Names Protocol](docs/Names.md)). |
| Files | `inv-files` | Filesystem service backing FUSE and NFS ([Files Protocol](docs/Files.md), [FileTree](docs/FileTree.md)). |
| KeyValue | `inv-kv` | Transactional MVCC key-value store ([KeyValue Protocol](docs/KeyValue.md)). |
| Build Cache | `inv-go-build-cache` | Go 1.24 `GOCACHEPROG` distributed build cache adapter ([Build Cache](docs/BuildCache.md)). |

Both `inv-files` and `inv-kv` are privileged services because they are initialized with the root content link for the data they serve. The other services are unprivileged and do not have access to unencrypted data.

## Documentation Index

- **Protocols & Services**: [Discovery](docs/Discovery.md), [Storage](docs/Storage.md), [Names](docs/Names.md), [Slots](docs/Slots.md), [Distribute](docs/Distribute.md), [Finder](docs/Finder.md), [Notify](docs/Notify.md), [KeyValue](docs/KeyValue.md), [Files](docs/Files.md)
- **Data Models & Subsystems**: [Content](docs/Content.md), [FileTree](docs/FileTree.md), [Workspace](docs/Workspace.md), [Repository](docs/Repository.md), [BuildCache](docs/BuildCache.md), [NFS](docs/NFS.md), [GitScan](docs/GitScan.md), [Journal](docs/Journal.md)
- **Infrastructure & Operations**: [ITP](docs/ITP.md), [Config (`config.yaml` / `services.yaml`)](docs/Config.md), [Trace](docs/Trace.md), [Tags](docs/Tags.md), [Systemd Examples](docs/examples/systemd/README.md)

## Executing Services

By default, all `inv-*` microservices listen over [ITP (`itp://`)](docs/ITP.md) (`-listen-ip=false`). To bind a local TCP port (`http://...`) instead—as shown in the local development examples below—pass `-listen-ip`. Omitting `-port` or passing `-port 0` binds a dynamic port on `inv-storage`, `inv-names`, `inv-slots`, `inv-distribute`, `inv-files`, and `inv-kv` (`inv-discovery` defaults to `3003`, and `inv-finder` defaults to `3004`).

Common flags shared by microservices include `-listen-ip`, `-port`, `-key-file`, `-derp-map`, `-service-key` (`INVARIANT_SERVICE_KEY`), `-trace`, `-log`, and (except `inv-discovery`) `-discovery`, `-invite` (`INVARIANT_INVITE`), `-provision`, `-token-file`, and (except `inv-discovery` and `inv-files`) `-tags`/`-tag` and `-advertise`.

### Discovery Service (`inv-discovery`)
To run the discovery server ([protocol description](docs/Discovery.md)), which defaults to port `3003`:
```bash
# Run with in-memory discovery storage over TCP
go run ./cmd/inv-discovery -listen-ip -port 3003

# Run with upstream delegation to another discovery service
go run ./cmd/inv-discovery -listen-ip -port 3003 -upstream http://upstream:3003
```

### Names Service (`inv-names`)
The names service ([protocol description](docs/Names.md)) binds logical string names to 64-character IDs:
```bash
# Run with in-memory names storage
go run ./cmd/inv-names -listen-ip -port 3005 -discovery http://localhost:3003

# Run with persistent snapshot/journal names storage
go run ./cmd/inv-names -listen-ip -port 3005 -dir /tmp/names -discovery http://localhost:3003

# Run with upstream delegation to another name service
go run ./cmd/inv-names -listen-ip -port 3005 -upstream http://upstream:3005 -discovery http://localhost:3003
```

### Storage Service (`inv-storage`)
The storage service ([protocol description](docs/Storage.md)) stores and retrieves blobs of data:
```bash
# Run with in-memory storage naively
go run ./cmd/inv-storage -listen-ip -port 3000

# Run with AWS S3 backend
go run ./cmd/inv-storage -listen-ip -port 3000 -s3-bucket my-bucket -s3-prefix invariant-blocks/

# Run with persistent nested filesystem blocks and register with discovery & distribute
go run ./cmd/inv-storage -listen-ip -port 3000 -dir /tmp/blocks -discovery http://localhost:3003 -distribute distribute-1 -notify distribute-1
```
*(Note: The `-notify` flag points to IDs or names implementing the [Notify protocol](docs/Notify.md).)*

### Distribute Service (`inv-distribute`)
The distribute server ([protocol description](docs/Distribute.md)) coordinates block replication:
```bash
# Run with default in-memory distribution logic
go run ./cmd/inv-distribute -listen-ip -port 3001

# Run with a specific replication factor and connect to discovery
go run ./cmd/inv-distribute -listen-ip -port 3001 -N 3 -discovery http://localhost:3003 -name distribute-1

# Run with a backup destination (resolved via discovery) and rate limit (MB/hour)
go run ./cmd/inv-distribute -listen-ip -port 3001 -destination backup-storage-id -backup-rate 100
```

### Finder Service (`inv-finder`)
The finder server ([protocol description](docs/Finder.md)) manages Kademlia routing (default port `3004` when `-listen-ip` is set):
```bash
# Start a standalone finder on default port 3004
go run ./cmd/inv-finder -listen-ip

# Connect the finder to the discovery service on a custom port
go run ./cmd/inv-finder -listen-ip -port 3002 -discovery http://localhost:3003
```

### Slots Service (`inv-slots`)
Manages mutable slots ([protocol description](docs/Slots.md)):
```bash
go run ./cmd/inv-slots -listen-ip -port 3004 -discovery http://localhost:3003 -notify notify-service-id
```

### Files Service (`inv-files`)
Serves a file tree over HTTP ([protocol description](docs/Files.md)):
```bash
go run ./cmd/inv-files -listen-ip -discovery http://localhost:3003 -slot my-slot
```

### Key-Value Service (`inv-kv`)
Serves the transactional key-value store ([protocol description](docs/KeyValue.md)):
```bash
go run ./cmd/inv-kv -listen-ip -dir /tmp/kv-journal -discovery http://localhost:3003
```

## Invariant CLI Utility (`invariant`)

The `invariant` utility is the main client and orchestrator. It reads global configuration from `~/.invariant/config.yaml` (see [Config](docs/Config.md)) and accepts the global `-log` flag before any subcommand.

- `provision`: Bootstrap the control plane and provision service tokens and discovery URLs for services in `services.yaml` (`-config`, `-discovery`, `-bin-dir`, `--install`).
- `start`: Start services locally defined in `services.yaml` (`-config`, `-max-backoff`, `-retry-interval`).
- `systemd`: Generate, install, start, stop, or inspect systemd unit files from `services.yaml` (`install`, `update`, `uninstall`, `log`, `start`, `stop`, `files`).
- `status`: Display registered services from discovery (`-discovery`, `-verbose`).
- `invite`: Create, redeem, list, or revoke control-plane invitations (`create`, `redeem`, `list`, `revoke`).
- `token`: List or revoke active control-plane access tokens (`list`, `revoke`).
- `service-key`: Create, list, or revoke service keys (`create`, `list`, `revoke`).
- `login`: Authenticate with the OAuth2 provider to obtain user credentials (`~/.invariant/oauth_token.json`).
- `slot`: Allocate a new slot from the slots service (`-name`, `-protected` for Ed25519 keypair in `~/.invariant/keys/`).
- `name`: Register a logical name to a slot or block (`-tokens`).
- `lookup`: Look up a registered name to get its corresponding ID or address (`-discovery`).
- `nfs`: Export an Invariant file tree as a native NFSv3 server (`-listen :2049`, see [NFS](docs/NFS.md)).
- `mount`: Mount an Invariant file tree locally via FUSE (`-mount`, plus common mount flags).
- `cache`: Inspect cache status for a mounted directory (walks `.invariant-mount.json` upward).
- `upload`: Upload a local directory to Invariant storage as a file tree (`--compress`, `--encrypt`, `--key-policy`, `--key`, `--slot`, `--prev`, `--dry-run`, `--stats`, `--no-cache`, `--tag`).
- `scan-repo`: Index Git SHA-1 <-> Invariant SHA-256 mappings in KV ([GitScan](docs/GitScan.md)).
- `workspace`: Manage layered workspaces (`create`, `mount`, `unmount`, `pull`, `branch`, `merge`, `rebase`; see [Workspace](docs/Workspace.md)).
- `repository`: Manage Invariant version control repositories (`create`, `open`, `change`, `status`, `diff`, `clean`, `commit`, `sync`, `submit`, `log`, `show`, `restore`, `revert`, `blame`, `grep`, `stash`, `bisect`, `rebase`, `cherry-pick`, `branch`, `checkout`, `tag`, `config`, `layer`, `mount`, `unmount`, `git import`, `git export`, `review`; see [Repository](docs/Repository.md)).
- `print`: Print a block's or file's contents to stdout (supports ContentLink JSON, `name/subpath`, and `-s` / `-slot`).

```bash
# Provision service tokens and start services defined in services.yaml
go run ./cmd/invariant provision --install -config services.yaml
go run ./cmd/invariant start -config services.yaml
```

**Example `services.yaml`** (see [Config](docs/Config.md) for substitution rules and `$key:` secrets):
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
      listen-ip: "true"
      port: "3003"

  - command: inv-names
    use: [discovery]
    args:
      listen-ip: "true"
      dir: "*/names"

  - command: inv-distribute
    use: [discovery]
    args:
      listen-ip: "true"
      name: "distribute-1"

  - command: inv-storage
    use: [discovery, distribute]
    args:
      listen-ip: "true"
      dir: "*/storage-1"
      notify: "distribute-1"

  - command: inv-storage
    use: [discovery, distribute]
    args:
      listen-ip: "true"
      dir: "*/storage-2"
      notify: "distribute-1"
```

### Docker Compose
For containerized deployments using pre-built `./bin/inv-*` binaries (or a user-supplied `Dockerfile`):

```yaml
services:
  discovery:
    image: golang:1.24
    working_dir: /app
    volumes:
      - .:/app
    command: ["./bin/inv-discovery", "-listen-ip", "-port", "3003"]
    ports:
      - "3003:3003"

  names:
    image: golang:1.24
    working_dir: /app
    command: ["./bin/inv-names", "-listen-ip", "-port", "3005", "-dir", "/data/names", "-discovery", "http://discovery:3003", "-advertise", "http://names:3005"]
    volumes:
      - .:/app
      - ./data/names:/data/names
    depends_on:
      - discovery

  distribute:
    image: golang:1.24
    working_dir: /app
    volumes:
      - .:/app
    command: ["./bin/inv-distribute", "-listen-ip", "-port", "3001", "-name", "distribute-1", "-discovery", "http://discovery:3003", "-advertise", "http://distribute:3001"]
    depends_on:
      - discovery

  storage-1:
    image: golang:1.24
    working_dir: /app
    command: ["./bin/inv-storage", "-listen-ip", "-port", "3000", "-dir", "/data/storage-1", "-discovery", "http://discovery:3003", "-advertise", "http://storage-1:3000", "-distribute", "distribute-1", "-notify", "distribute-1"]
    volumes:
      - .:/app
      - ./data/storage-1:/data/storage-1
    depends_on:
      - discovery
      - distribute

  storage-2:
    image: golang:1.24
    working_dir: /app
    command: ["./bin/inv-storage", "-listen-ip", "-port", "3002", "-dir", "/data/storage-2", "-discovery", "http://discovery:3003", "-advertise", "http://storage-2:3002", "-distribute", "distribute-1", "-notify", "distribute-1"]
    volumes:
      - .:/app
      - ./data/storage-2:/data/storage-2
    depends_on:
      - discovery
      - distribute
```

## Installing the CLI and Services
To compile and install the `invariant` CLI and all microservice commands using the Go toolchain:
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
go install github.com/chuckjaz/invariant-go/cmd/inv-go-build-cache@latest
```

Ensure your Go binary directory (`$(go env GOPATH)/bin` or `$GOBIN`) is on your `PATH`. You can also pass `--install` to `invariant provision` (e.g., `invariant provision --install -config services.yaml`) to automatically install any missing service commands referenced in `services.yaml` using `go install`.

## Building Binaries
To build all 10 binaries into `bin/` (via `ninja` if installed, or `go build` fallback), run:
```bash
./build
```

## Running Tests
To run the standard unit test suite:
```bash
go test -v ./...
```

To run the multi-node simulated cluster integration tests (`internal/cluster`):
```bash
go test -tags cluster ./internal/cluster
```
