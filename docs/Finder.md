# The invariant project - Finder Protocol

The Finder Protocol is a protocol for locating blocks across storage services using Kademlia routing.

## Version

Version 1 of the finder protocol uses the protocol token `finder-v1`.

## Values

### `:id`

A service ID, which is a 32-byte hex-encoded value.

### `:address`

A block address, which is a 32-byte hex-encoded SHA-256 hash of the block content.

## Endpoints

### `GET /id`

Returns the hex-encoded ID of the finder service (`200 OK`, `Content-Type: text/plain`).

### `GET /:address`

Returns the IDs of storage services that hold the given block address, or the IDs of peer finder services that may know about it.

#### Response

`200 OK` (`Content-Type: application/json`) returning a JSON array of `FindResponse` objects:

```ts
interface FindResponse {
    id: string;
    protocol: string; // "storage-v1" or "finder-v1"
}
```

If `protocol` is `storage-v1`, that service holds the block. If `protocol` is `finder-v1`, it is a peer finder closer in Kademlia distance that may know about the block. Clients should query the returned services in order.

Returns `400 Bad Request` if `:address` is empty or not a valid hex block address, or `500 Internal Server Error` on failure.

### `PUT /notify/:id`

Notifies the finder service that the storage service with `:id` has blocks with the given addresses (see [Notify Protocol](Notify.md)).

#### Request

```ts
interface HasRequest {
    addresses: string[];
}
```

#### Response

`200 OK` (empty body) on success, `400 Bad Request` on empty `:id` or invalid JSON, or `500 Internal Server Error` on failure.

### `PUT /peer/:id`

Notifies the finder service that another finder service exists with `:id`.

A finder periodically notifies peer finders discovered via the discovery service. Once notified of a peer finder, it asynchronously pushes information about blocks it knows of that are closer in Kademlia XOR distance to the new peer than to itself.

#### Request

Empty body.

#### Response

`200 OK` (empty body) on success, or `400 Bad Request` if `:id` is empty or peer registration fails.
