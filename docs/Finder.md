# The invariant project - Finder Protocol

The finder protocol is a protocol for finding blocks in storage services.

The finder service uses the Kademlia algorithm to find the storage service that have the given block ID.

## Values

### `:id`

A ID which is a 32 byte hex encoded value.

### `:address`

A block ID which is a 32 byte hex encoded sha256 hash of the content of the block.

## Endpoints

### `GET /id`

Returns the ID of the finder service.

### Response

The hex encoded ID of the finder service.

### `GET /:address`

Returns the ID of the storage service that has the given block address or the ID of another finder that may know about it. The response is an array of JSON objects with TypeScript type of,

```ts
interface FindResponse  {
    id: string;
    protocol: string;
}
```

The `protocol` is the protocol of the service. If it is a `storage-v1` then it has the block. If it is a `finder-v1` then it may know about the block and the client should query it. The client should query the services in the order they are returned. 

## `PUT /has/:id`

Notifies the finder service that the storage service with `:id` has blocks with the given addresses. The request is a JSON object with type of,

```ts
interface HasRequest {
    addresses: string[];
}
```

### Response

The response is empty.

## PUT `/notify/:id`

Notifies the finder service that there is another finder with `:id`.

A finder should notify other finders of its existence periodically. If a finder is created with a discovery service, it should register with the discovery service and then query for other finder services and notify them of its existence.

Once a finder has been notified of a finder, it should tell the finder of all the blocks it knows about that are closer to the new finder than the current finder. Closer is defined as having a smaller Kademlia distance from its ID and the block address. 

### Request

The request is empty.

### Response

The response is empty.
