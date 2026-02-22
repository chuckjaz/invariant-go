# The intervariant project - Discovery Protocol

The Discovery Protocol is a simple HTTP-based protocol for discovering Invariant services.

As services are brought up, they register their ID and address with the Discovery service. Clients can then query the Discovery service to find the address of a service by its ID. The service can also register the protocols it supports and clients can query the Discovery service to find the address of a service by its ID and the protocol it supports. The client should attempt to connect to the service using the protocol it supports in the order reported by the discovery service.

The discovery service will periodally validate registered services are still available. If a service is not available, the discovery service will remove it from the list of registered services. A service is considered availabe if it responds to a GET request to `/id` returns the same ID as registered.

## Values

### `:id`

A ID which is a 32 byte hex encoded value.

### `:protocol`

A protocol which is a string.

### `:count`

A count which is an integer.

# Endpoints

## `GET /id`

Returns the ID of the discovery service.

### Response

The hex encoded ID of the discovery service.

## `GET /:id`

Returns the service description for the given ID. The service description includes the id, address, and protocols supported by the service. The response is a JSON object with TypeScript type of,

```ts
interface ServiceDescription {
    id: string;
    address: string;
    protocols: string[];
}
```

## `GET /?protocol=:protocol&count=:count`

Returns a list of service descriptions for the given protocol. The response is a JSON array of service descriptions. The count parameter is optional and defaults to 1.

### Query Parameters

| Parameter | Description |
| --------- | ----------- |
| protocol  | The protocol to search for. |
| count     | The number of services to return. |

### Response

The response is a JSON array of service descriptions.

## `PUT /:id`

Register a service with the discovery service.

### Request

The request is a JSON object with TypeScript type of,

```ts
interface ServiceRegistration {
    id: string;
    address: string;
    protocols: string[];
}
```

### Response

The respons is empty.
