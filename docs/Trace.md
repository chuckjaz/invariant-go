# The invariant project - Distributed Tracing (`internal/trace`)

All Invariant HTTP microservices (`inv-discovery`, `inv-distribute`, `inv-files`, `inv-finder`, `inv-kv`, `inv-names`, `inv-slots`, `inv-storage`) integrate the `internal/trace` HTTP middleware when started with `-trace` (default `false`).

## Headers

- `X-Trace-ID`: Propagated or generated trace identifier.
- `X-Span-ID`: Generated span identifier for the current request.
- `X-Parent-Span-ID`: Optional parent span identifier from the caller.

## Endpoints

The tracing middleware intercepts `/trace` and `/trace/summary` on every instrumented service:

### `GET /trace`

Returns a JSON array of recorded `Span` objects from the in-memory FIFO ring buffer (capacity `10000`):

```ts
interface Span {
    trace_id: string;
    span_id: string;
    parent_span_id?: string;
    service: string;
    endpoint: string;    // normalized path, e.g. "/:id" or "/:node"
    method: string;
    path: string;
    status_code: number;
    start_time: string;
    duration_ms: number;
    bytes_in?: number;
    bytes_out?: number;
    error?: string;
    attributes?: Record<string, string>;
}
```

### `GET /trace/summary`

Returns aggregated per-endpoint latency and error statistics (`Summary`):

```ts
interface EndpointStat {
    count: number;
    errors: number;
    min_ms: number;
    mean_ms: number;
    p50_ms: number;
    p95_ms: number;
    p99_ms: number;
    max_ms: number;
    bytes_in: number;
    bytes_out: number;
}

interface Summary {
    service: string;
    total_spans: number;
    error_count: number;
    endpoints: Record<string, EndpointStat>; // keyed by "<METHOD> <Endpoint>"
}
```

### `POST /trace?enabled=<bool>`

Dynamically enables or disables span recording (`true`/`false` via `strconv.ParseBool`). Returns `200 OK`.

### `DELETE /trace`

Clears all spans from the ring buffer. Returns `200 OK`.
