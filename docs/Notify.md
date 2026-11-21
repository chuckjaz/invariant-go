# The invariant project - Notify Protocol

The Notify protocol is a notification protocol for storage services to notify other services (such as Distribute and Finder) that they hold a set of blocks.

This protocol is shared by the [Distribute Protocol](Distribute.md) and the [Finder Protocol](Finder.md).

## Version

Version 1 of the notify protocol uses the protocol token `notify-v1`.

## Values

### `:id`

A service ID, which is a 32-byte hex-encoded value.

### `:address`

A block ID, which is a 32-byte hex-encoded SHA-256 hash of the block content.

## Endpoints

### `PUT /notify/:id`

Notifies the target service that the storage service with `:id` has blocks with the given addresses.

#### Request

JSON object (`Content-Type: application/json`) matching `HasRequest`:

```ts
interface HasRequest {
    addresses: string[];
}
```

#### Response

`200 OK` (empty body) on success, `400 Bad Request` on invalid input, or `500 Internal Server Error` on failure.
