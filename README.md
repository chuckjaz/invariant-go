# The invariant project

A storage system of invariant data that can be used for general purpose programming.

## Executing the services

### Discovery Service
To run the discovery server, which defaults to port `3003`:
```bash
# Run with in-memory discovery storage
go run ./cmd/discovery -port 3003
```

### Storage Service
To run the storage server, you can fire off the entrypoint via mapping your preferred `-port` and an optional target `-dir`:
```bash
# Run with in-memory storage natively
go run ./cmd/storage -port 3000

# Run with persistent nested file system blocks
go run ./cmd/storage -port 3000 -dir /tmp/blocks
```

### Distribute Service
To run the distribute server, which coordinates block replication logic, typically on port `3001` or `3004`:
```bash
# Run with default in-memory distribution logic
go run ./cmd/distribute -port 3001

# With a specific replication factor and connecting to discovery
go run ./cmd/distribute -port 3001 -N 3 -discovery http://localhost:3003
```

### Finder Service
To run the finder server, which manages Kademlia routing logic, typically on port `3002` or `3004`:
```bash
# Start a standalone finder
go run ./cmd/finder -port 3002

# Connect the finder to the discovery service
go run ./cmd/finder -port 3002 -discovery http://localhost:3003
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