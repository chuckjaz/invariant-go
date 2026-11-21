# The invariant project - Distribute Protocol

The Distribute Protocol is an HTTP protocol for coordinating blob replication across storage services.

A distribute service ensures that data blocks are replicated across multiple storage services so that losing a storage device does not make blocks unavailable. It maintains a list of registered storage services, periodically checks their health, and replicates blocks when a storage service becomes unavailable or under-replicated.

By default, distribute calculates the Kademlia XOR distance between a block address and each storage service ID, selecting the closest `N` storage services (default `N = 3`) and ensuring each holds the block. When configured with a `-destination` backup storage service (and optional `-backup-rate` in MB/hour), that destination is excluded from standard replication and receives rate-limited backup copies instead.

## Version

Version 1 of the distribute protocol uses the protocol token `distribute-v1`.

## Values

### `:id`

A server ID, which is a 32-byte hex-encoded value.

### `:address`

The content hash of a block.

## Endpoints

### `GET /id`

Returns the hex-encoded ID of the distribute service (`200 OK`, `Content-Type: text/plain`).

### `PUT /register/:id`

Register a storage service with the distribute service. Once registered, the distribute service periodically checks the storage service's health and replicates blocks from it if it becomes unavailable.

When a storage service starts, it sends `PUT /notify/:id` requests to report the blocks it holds, and sends periodic `PUT /notify/:id` updates as new blocks arrive.

#### Request

Empty body.

#### Response

`200 OK` (empty body) on success, `400 Bad Request` if `:id` is empty, or `500 Internal Server Error` on failure.

### `PUT /notify/:id`

Notifies the distribute service that the storage service with `:id` holds blocks with the given addresses (see [Notify Protocol](Notify.md)).

#### Request

```ts
interface HasRequest {
    addresses: string[];
}
```

#### Response

`200 OK` (empty body) on success, `400 Bad Request` on empty `:id` or invalid JSON, or `500 Internal Server Error` on failure.
