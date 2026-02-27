# The invariant project - Names protocol

The names protocol is a protocol that allows you register and retieve IDs and addresses by name. The ID can be sent to the discovery service to discover the location of the service. An address can be used to send requests to the service. For example, for a block address, the address can be sent to the storage service to retrieve the block.

Both IDs and addresses are 32-byte hex strings. 

Names can also be retrieved from a DNS TXT record with the form `invariant:<id|address>;<tokens>` where `<tokens>` is a comma separated list of protocol version tokens. A synthetic version token of `block-v1` is used for block addresses. For tokens of server protocols indicates it is an ID for the a server that has the given protocol. For example, if the token `storage-v1` is present then the ID is for a server that has the storage protocol.

## Version

This version 1 of the names protocol is and has a version token of names-v1.

## Values

### `:name`

The name of a service or block.

### `:value`

The ID of a service or the address of a block.

### `:tokens`

The protocol version tokens for the service or block. For a block the token should be `block-v1`. For a service the token should be the protocol tokens of the protocols the service supports.

## GET /:name

Retrieve the ID and address of the service or block with the given name. The response is a JSON object with the TypeScript type of,

```ts
interface NameResponse {
    value: string;
    tokens: string[];
}
```

## PUT /:name?value=:id&tokens=:tokens

Store the ID of a service or the address of a block with the given name. The tokens are protocol version tokens. For a block the token should be `block-v1`. For a service the token should be the protocol tokens of the protocols the service supports.

## DELETE /:name

Delete the name from the names service.

### Required request headers

| Header        | Value                     |
| ------------- | ------------------------- |
| If-Match      | `:value`                  |

The If-Match must match the current ID or address associated with the name. If it does not match, or is missing, the request will be rejected with a 412 Precondition Failed response.

### Required response headers

| Header        | Value                     |
| ------------- | ------------------------- |
| ETag          | `:value`                  |