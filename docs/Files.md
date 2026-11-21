# The invariant project - Files Protocol

The files protocol is a filesystem HTTP protocol that creates, reads, and updates file trees as specified by the [FileTree](FileTree.md) specification. This system is used by the FUSE and NFS adapters to mount a file tree into a directory.

## Values

### `:content-information`

Information about a content node (`ContentInformationCommon`):

```typescript
enum ContentKind {
    File = "File",
    Directory = "Directory",
    SymbolicLink = "SymbolicLink"
}

interface ContentInformationCommon {
    node: number;
    kind: ContentKind;
    modifyTime: number;
    createTime: number;
    executable: boolean;
    writable: boolean;
    etag: string;
}
```

- `node` - The node number (`uint64`) of the entry.
- `kind` - The kind of the entry (`File`, `Directory`, or `SymbolicLink`).
- `modifyTime` - The time the entry was last modified (Unix seconds).
- `createTime` - The time the entry was created (Unix seconds).
- `executable` - Whether the entry has execute permission.
- `writable` - Whether the entry is writable.
- `etag` - The ETag of the content (SHA-256 hash: `expected` of the associated content link, or `address` if `expected` is absent).

### `:node-info`

Combined entry metadata and size/mode information (`NodeInfo`):

```typescript
interface NodeInfo {
    node: number;
    kind: ContentKind;
    size: number;
    mode: number;
    createTime: number;
    modifyTime: number;
    executable: boolean;
    writable: boolean;
    etag: string;
}
```

### `:entry-attributes`

Mutable or queried attributes of a file, directory, or symbolic link (`EntryAttributes`):

```typescript
interface EntryAttributes {
    writable?: boolean;
    modifyTime?: number;
    createTime?: number;
    mode?: string;
    size?: number;
    type?: string;
}
```

- `writable` - Whether the entry is writable. An entry is writable if its directory (or an ancestor) is backed by a writable slot.
- `modifyTime` - The time the entry was last modified.
- `createTime` - The time the entry was created.
- `mode` - The POSIX mode of the entry in octal format (e.g., `"0644"`). `writable` takes precedence over `mode`.
- `size` - The size of the entry in bytes (only valid for files).
- `type` - The MIME type of the entry (only valid for files). A value of `"-"` removes the MIME type.

### `:name`

The name of an entry in a directory.

### `:node`

The `uint64` node number of a file, directory, or symbolic link. Node `1` is reserved for the root directory.

## Endpoints

### `PUT /:node/:name`

Create a file, directory, or symbolic link with `:name` in the directory `:node`.

Note: This can be used to efficiently copy files or directories by passing the `content` query parameter.

#### Optional Query Parameters

- `kind` - Entry kind: `File` (default when `kind` is `"file"` or omitted), `Directory`, or `SymbolicLink`.
- `target` - Target of the symbolic link (required when `kind` is `SymbolicLink`).
- `content` - JSON-encoded `ContentLink` for `File` or `Directory`. If omitted, an empty file or directory is created.

#### Response

`201 Created` on success, `400 Bad Request` on invalid `:node` or invalid `content` JSON, `403 Forbidden` if the filesystem is read-only, or `500 Internal Server Error` on failure.

### `GET /file/:node`

Read a file or symbolic link target with node number `:node`.

#### Optional Query Parameters

- `offset` - Byte offset into the file (`int64`). If omitted, reads from the beginning. If negative, relative to the end of the file.
- `length` - Number of bytes to read (`int64`). If omitted, reads until the end of the file.

#### Response

`200 OK` streaming the file bytes, `400 Bad Request` on invalid `:node`, `404 Not Found` if `:node` is an invalid file node, or `500 Internal Server Error` on read error.

### `POST /file/:node`

Write or overwrite data in a file with node number `:node`.

#### Optional Query Parameters

- `offset` - Byte offset into the file (`int64`). If omitted, writes from the beginning. If negative, relative to the end of the file.
- `append` - If `"true"`, appends to the end of the file (`offset` is ignored).

#### Response

`200 OK` on success, `400 Bad Request` on invalid `:node`, `403 Forbidden` if read-only, `404 Not Found` if invalid file node, or `500 Internal Server Error` on failure.

### `GET /directory/:node`

Read directory entries for directory `:node`.

#### Optional Query Parameters

- `offset` - Entry index offset (`int64`). Negative values are relative to the end of the directory.
- `length` - Maximum number of directory entries to return (`int64`).

#### Response

`200 OK` (`Content-Type: application/json`) returning a `Directory` JSON array of entries, `400 Bad Request` on invalid `:node`, or `500 Internal Server Error` on failure.

### `GET /attributes/:node`

Read the `EntryAttributes` of `:node`. Returns `200 OK` (`Content-Type: application/json`), `400 Bad Request` on invalid `:node`, or `404 Not Found`.

### `POST /attributes/:node`

Update the attributes of `:node`. Request body is a JSON `EntryAttributes` object (omitted fields are not modified).

#### Response

`200 OK` (`Content-Type: application/json`) returning the updated `EntryAttributes`, `400 Bad Request` on invalid `:node` or JSON, `403 Forbidden` if read-only, or `404 Not Found`.

### `GET /content/:node`

Read the `ContentLink` of `:node`. Returns `200 OK` (`Content-Type: application/json`), or `400 Bad Request` on invalid `:node` or error.

### `GET /info/:node`

Read the `ContentInformationCommon` of `:node`. Returns `200 OK` (`Content-Type: application/json`), `400 Bad Request` on invalid `:node`, or `404 Not Found`.

### `GET /nodeinfo/:node`

Read the `NodeInfo` of `:node`. Returns `200 OK` (`Content-Type: application/json`), `400 Bad Request` on invalid `:node`, or `404 Not Found`.

### `GET /lookup/:node/:name`

Look up `:name` in directory `:node` and return its `ContentInformationCommon`. Returns `200 OK` (`Content-Type: application/json`), `400 Bad Request` on invalid `:node`, or `404 Not Found`.

### `GET /lookup-nodeinfo/:node/:name`

Look up `:name` in directory `:node` and return its `NodeInfo`. Returns `200 OK` (`Content-Type: application/json`), `400 Bad Request` on invalid `:node`, or `404 Not Found`.

### `PUT /remove/:node/:name`

Remove `:name` from directory `:node`. Returns `200 OK` on success, `400 Bad Request` on invalid `:node`, `403 Forbidden` if read-only, or `404 Not Found`.

### `POST /rename/:node/:name`

Rename or move `:name` from directory `:node`.

#### Query Parameters

- `name` - New entry name (required, `400 Bad Request` if missing).
- `directory` - Destination directory `:node` (`uint64`; defaults to `:node`).

#### Response

`200 OK` on success, `400 Bad Request` on invalid parameters, `403 Forbidden` if read-only, or `500 Internal Server Error` on failure.

### `PUT /link/:node/:name`

Create an in-memory hard link named `:name` in directory `:node` pointing to an existing node. Hard links are ephemeral and are not persisted across unmount.

#### Query Parameters

- `node` - Target `:node` number (`uint64`, required).

#### Response

`201 Created` on success, `400 Bad Request` on invalid parameters, `403 Forbidden` if read-only, or `500 Internal Server Error` on failure.

### `PUT /sync`

Sync pending writes in the filesystem. If `:node` is writable, the nearest ancestor backed by a writable slot has its slot updated after all modified content is flushed to storage.

#### Query Parameters

- `node` - Node number to sync (`uint64`, defaults to `1` for root).
- `wait` - If `"false"`, returns immediately without waiting for sync completion; otherwise (`!= "false"`, default `true`) waits for sync completion.

#### Response

`200 OK` on success, `400 Bad Request` on invalid `node`, or `500 Internal Server Error` if sync fails while `wait` is true.
