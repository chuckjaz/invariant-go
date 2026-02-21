# The invariant project

A storage system of invariant data that can be used for general purpose programming.

## Executing the project
To run the server gracefully, you can fire off the entrypoint via mapping your preferred `PORT` and a target `--dir`:
```bash
# Run with in-memory storage natively
PORT=3000 go run ./cmd/storage

# Run with persistent nested file system blocks
PORT=3000 go run ./cmd/storage --dir=/tmp/blocks
```

## Running tests
To run continuous tests, execute standard go test coverage:
```bash
go test -v ./...
```