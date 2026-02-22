# The invariant project

A storage system of invariant data that can be used for general purpose programming.

## Executing the services

### Discovery Service
To run the discovery server, which defaults to port `3003`:
```bash
# Run with in-memory discovery storage
PORT=3003 go run ./cmd/discovery
```

### Storage Service
To run the storage server, you can fire off the entrypoint via mapping your preferred `PORT` and an optional target `--dir`:
```bash
# Run with in-memory storage natively
PORT=3000 go run ./cmd/storage

# Run with persistent nested file system blocks
PORT=3000 go run ./cmd/storage --dir=/tmp/blocks
```

### Distribute Service
To run the distribute server, which coordinates block replication logic, typically on port `3001` or `3004`:
```bash
# Run with default in-memory distribution logic
PORT=3001 go run ./cmd/distribute

# With a specific replication factor and connecting to discovery
PORT=3001 go run ./cmd/distribute -N 3 -discovery http://localhost:3003
```

### Finder Service
To run the finder server, which manages Kademlia routing logic, typically on port `3002` or `3004`:
```bash
# Start a standalone finder
PORT=3002 go run ./cmd/finder

# Connect the finder to the discovery service
PORT=3002 go run ./cmd/finder -discovery http://localhost:3003
```

## Running tests
To run continuous tests, execute standard go test coverage:
```bash
go test -v ./...
```