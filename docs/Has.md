# The invariant project - Has Protocol

The Has protocol is a notification protocol for storage services to notify other storage services that they have a block.

This protocol is a common part of the Distribute protocol and the Finder protocol. 

## Version

The version 1 of the has protocol with the protocol token of has-v1.

## Values

### `:id`

A ID which is a 32 byte hex encoded value.

### `:address`

A block ID which is a 32 byte hex encoded sha256 hash of the content of the block.

# Endpoints

## `PUT /has/:id`

Notifies the storage service that the storage service with `:id` has blocks with the given addresses. The request is a JSON object with type of,

```ts
interface HasRequest {
    addresses: string[];
}
```

### Response

The response is empty.
