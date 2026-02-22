# The intervariant project - Distribute Protocol

The Distribute Protocol is a simple protocol for distributing blobs between storage services.

The job of a distribute service is to ensure that data blocks are distributed among storage services to reduce the chance that one of the storage services becoming unavailable will make the block unavailable. This is done by having the distribute service maintain a list of storage services and their addresses. The distribute service will then periodically check the health of the storage services and if a storage service is not available, it will attempt to replicate the data blocks from the unavailable storage service to other storage services.

The primary algorithm used by distribute is to determine Kademlia distance of the block from the storage service ID and to take the top N storage services, where N is by default 3, and ensure they have the block. If they don't have the block the block is uploaded to the service.

The kademlia distance is calculated as the XOR of the binary representation of the two IDs.

# Version

The version 1 of the distribute protocol with the protocol token of distribute-v1.

# Values

## `:id`

A server ID which is a 32 byte hex encoded value.

## `:address`

The content hash of the content.

# Endpoints

## `GET /id`

Returns the ID of the distribute service.

### Response

The hex encoded ID of the distribute service.

## `PUT /register/:id`

Register a storage service with the distribute service. Once a storage service is registered, the distribute service will periodically check the health of the storage service and if it is not available, it will attempt to replicate the data blocks from the unavailable storage service to other storage services.

When a storage service starts it is expected to send `PUT /has` requests to the distribute service to identify all the blocks it has. It is also expected to send `PUT /has` requests periodically to update the distribute service of new blocks receives.

### Request

The request is empty.

### Response

The response is empty.

## `PUT /has/:id`

Notifies the distribute service that the storage service with `:id` has blocks with the given addresses. The request is a JSON object with TypeScript type of,

```ts
interface HasRequest {
    addresses: string[];
}
```

### Response

The response is empty. 