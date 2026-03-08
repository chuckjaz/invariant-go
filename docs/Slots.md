# The invariant project - slots protocol

A slots service is a container of mapping of an :id to to its current :address. The :id is a 32 byte hex encoded value. The :address is a string representing the sha256 hash of a content block. A slots service can be queried for the :address of a given :id. A slots service can be updated with a new :address for a given :id.

## Values

### `:id`

A 32 byte hex encoded value.

### `:address`

A string representing the sha256 hash of a content block.

### `:policy`

A string representing the policy for the slot. The policy can be `ecc` for an Ed25519 256-bit elliptic curve key pair.

## Endpoints

## `GET /id`

Returns the ID of the slots service.

## `GET /:id`

Returns the :address for the given :id.

## `PUT /:id`

Sets the :address for the given :id. 

### Request

The request is a JSON object with TypeScript type of,

```ts
interface SlotUpdate {
    address: string;
    previousAddress: string;
}
```

The request is rejected if the current :address is not equal to previousAddress. Clients should use GET /:id to get the :address before attempting an update. If the address has changed the client should attempt to merge the changes, which depends on the content, before attempting to update again. The slots service does not validate the merge, it just uses previousAddress to enforce a serialization of updates.

When a slot is created with a :policy, the :address is the public key of the :policy. Request to update the slot require an authorization header with the signature of the request data using the private key of the :policy.

### Response

The response is empty.

### `POST /:id?protected=:policy`

Create a new slot with the given :id.

### Request

The request is a JSON object with TypeScript type of,

```ts
interface SlotRegistration {
    address: string;
}
```

### Response

The response is empty.
