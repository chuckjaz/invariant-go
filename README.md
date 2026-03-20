# The invariant project

A content addressing storage system that can be used to store abitrary data. Data can be uploaded to the storage system and retrieved using a content link. The storage system is built on top of a distributed storage system that can be scaled arbitrarily.

The system is made up of multiple micro-services that can be run independently or together and replaced or updated arbitrarily.

### File System
A directory can be uploaded to the storage system and then mounted locally via FUSE or exported natively as an NFS server. The directory can then be interacted with as a regular file system. The file system can be mounted on mutliple machines and will automatically sync from one machine to another.

The file system can be layered, which allows for files in a dirctory to go to different storage devices or cloud storage providers. For example, a development directory is often made up of source files and built files. The source files are usually stored in some subdirectory of the project directory. The source files can cached locally, replicated to a network storage device, and always backed up to the cloud. The files could be stored only locally. This prevents files that can be rebuilt taking up space on the cloud storage provider.

### Automatic backup and recovery
The system can be configured to automatically upload the file system to cloud storage providers such as AWS S3, Google Cloud Storage, or Azure Blob Storage. The system can be configured to compress and encrypt the data before uploading it to the cloud storage provider as well as only storing encyrpted copies of the files at rest.

If the local storage device fails, the system can be configured to automatically recover from the cloud storage provider incrementally. The files are then cached locally as they are read from the cloud storage provider.

### Replication
When storing files locally, either on the same machine, or via a connected storage device, the data can be distributed amoung multiple physical storage devices to improve redundancy and availability. These devices can be added and removed from the system arbitrarily which will redistribute the data among the remaining devices or populate a new device with replications of the data. Data will not be lost if a storage device fails unless the total number of storage devices falls below the replication factor. If the data is backed up to the cloud, the data will not be lost if the local storage device fails. The data will be read from the cloud storage provider not cached locally.

## Executing the services

Many of the services support dynamic port binding if you omit the `-port` flag or provide `-port 0`. This is useful for avoiding port conflicts when running multiple instances.

### Discovery Service
To run the discovery server ([protocol description](docs/Discovery.md)), which defaults to port `3003`:
```bash
# Run with in-memory discovery storage
go run ./cmd/discovery -port 3003

# Run with upstream delegation to another discovery service
go run ./cmd/discovery -port 3003 -upstream http://upstream:3003
```

### Names Service
The names service ([protocol description](docs/Names.md)) provides a mechanism to bind logical string names to 64-character IDs. It can be run in memory or backed by the file system.
```bash
# Run with in-memory names storage
go run ./cmd/names -port 3005 -discovery http://localhost:3003

# Run with persistent snapshot/journal names storage
go run ./cmd/names -port 3005 -dir /tmp/names -discovery http://localhost:3003

# Run with upstream delegation to another name service
go run ./cmd/names -port 3005 -upstream http://upstream:3005 -discovery http://localhost:3003
```

### Storage Service
The storage service ([protocol description](docs/Storage.md)) is responsible for storing and retrieving blobs of data.
```bash
# Run with in-memory storage naitvely
go run ./cmd/storage -port 3000

# Run with AWS S3 backend
go run ./cmd/storage -port 3000 -s3-bucket my-bucket -s3-prefix invariant-blocks/

# Run with persistent nested file system blocks and register with discovery & distribute services
go run ./cmd/storage -port 3000 -dir /tmp/blocks -discovery http://localhost:3003 -distribute distribute-1 -notify notify-service-id
```
*(Note: The `-notify` flag points to IDs implementing the Notify protocol.)*

### Distribute Service
The distribute server ([protocol description](docs/Distribute.md)) coordinates block replication logic. It can pull available names/IDs from the discovery service.
```bash
# Run with default in-memory distribution logic
go run ./cmd/distribute -port 3001

# Run with a specific replication factor and connect to discovery
go run ./cmd/distribute -port 3001 -N 3 -discovery http://localhost:3003 -name distribute-1

# Run with a backup destination (resolved automatically via discovery) and rate limit (MB/hour)
# The destination is excluded from standard replication
go run ./cmd/distribute -port 3001 -destination backup-storage-id -backup-rate 100
```

### Finder Service
The finder server ([protocol description](docs/Finder.md)) manages Kademlia routing logic, interacting via the Peer protocol.
```bash
# Start a standalone finder
go run ./cmd/finder -port 3002

# Connect the finder to the discovery service
go run ./cmd/finder -port 3002 -discovery http://localhost:3003
```

### Slots Service
A service to allocate and manage mutable slots. It can also notify other services.
```bash
go run ./cmd/slots -port 3004 -discovery http://localhost:3003 -notify notify-service-id
```

### Invariant CLI Utility
The `invariant` utility is the main client and orchestrator for the system. It reads global configuration from `~/.invariant/config.yaml` and provides subcommands for cluster interaction:

- `start`: Start services locally defined in a YAML configuration file.
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
- `print`: Print a block's contents to standard output. Supports ContentLink JSON input directly or via pipe.

```bash
# Start services defined in services.yaml
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
  - command: discovery
    args:
        port: 3003

  - command: names
    use: [discovery]
    args:
        dir: "*/names"

  - command: distribute
    use: [discovery]
    args:
        name: "distribute-1"

  - command: storage
    use: [discovery, distribute]
    args:
        dir: "*/storage-1"
        notify: "distribute-1"

  - command: storage
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
    command: ["./bin/discovery", "-port", "3003"]
    ports:
      - "3003:3003"

  names:
    build: .
    command: ["./bin/names", "-port", "3005", "-dir", "/data/names"]
    volumes:
      - ./data/names:/data/names
    depends_on:
      - discovery
    environment:
      # Inject discovery args via command override conceptually, or rely on internal entrypoint scripts
      # For simplicity, assumed inline here
    command: ["./bin/names", "-port", "3005", "-dir", "/data/names", "-discovery", "http://discovery:3003", "-advertise", "http://0.0.0.0"]

  distribute:
    build: .
    command: ["./bin/distribute", "-port", "3001", "-name", "distribute-1", "-discovery", "http://discovery:3003", "-advertise", "http://0.0.0.0"]
    depends_on:
      - discovery

  storage-1:
    build: .
    command: ["./bin/storage", "-port", "3000", "-dir", "/data/storage-1", "-discovery", "http://discovery:3003", "-advertise", "http://0.0.0.0", "-distribute", "distribute-1", "-notify", "distribute-1"]
    volumes:
      - ./data/storage-1:/data/storage-1
    depends_on:
      - discovery
      - distribute

  storage-2:
    build: .
    command: ["./bin/storage", "-port", "3002", "-dir", "/data/storage-2", "-discovery", "http://discovery:3003", "-advertise", "http://0.0.0.0", "-distribute", "distribute-1", "-notify", "distribute-1"]
    volumes:
      - ./data/storage-2:/data/storage-2
    depends_on:
      - discovery
      - distribute
```

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