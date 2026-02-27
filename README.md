# The invariant project

A storage system of invariant data that can be used for general purpose programming.

## Executing the services

Many of the services support dynamic port binding if you omit the `-port` flag or provide `-port 0`. This is useful for avoiding port conflicts when running multiple instances.

### Discovery Service
To run the discovery server, which defaults to port `3003`:
```bash
# Run with in-memory discovery storage
go run ./cmd/discovery -port 3003
```

### Names Service
The names service provides a mechanism to bind logical string names to 64-character IDs. It can be run in memory or backed by the file system.
```bash
# Run with in-memory names storage
go run ./cmd/names -port 3005 -discovery http://localhost:3003

# Run with persistent snapshot/journal names storage
go run ./cmd/names -port 3005 -dir /tmp/names -discovery http://localhost:3003
```

### Storage Service
The storage service is responsible for storing and retrieving blobs of data.
```bash
# Run with in-memory storage naitvely
go run ./cmd/storage -port 3000

# Run with persistent nested file system blocks and register with discovery & distribute services
go run ./cmd/storage -port 3000 -dir /tmp/blocks -discovery http://localhost:3003 -distribute distribute-1 -has has-service-id
```

### Distribute Service
The distribute server coordinates block replication logic. It can pull available names/IDs from the discovery service.
```bash
# Run with default in-memory distribution logic
go run ./cmd/distribute -port 3001

# Run with a specific replication factor and connect to discovery
go run ./cmd/distribute -port 3001 -N 3 -discovery http://localhost:3003 -name distribute-1
```

### Finder Service
The finder server manages Kademlia routing logic.
```bash
# Start a standalone finder
go run ./cmd/finder -port 3002

# Connect the finder to the discovery service
go run ./cmd/finder -port 3002 -discovery http://localhost:3003
```

### Start Service (Orchestrator)
The `start` command lets you execute multiple services governed by a single YAML configuration file. Wait for all processes to start, substitute environment variables, substitute `~` for the home directory and `*` for the configuration's base directory, and share common arguments.

```bash
# Start services defined in services.yaml
go run ./cmd/start services.yaml
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
        has: "distribute-1"

  - command: storage
    use: [discovery, distribute]
    args:
        dir: "*/storage-2"
        has: "distribute-1"
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