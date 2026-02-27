# The invarient project - files protocol

The files protocol is a file system protocol that creates and updates a file trees as specified by the [FileTree](docs/FileTree.md) specification. This system is intended to be used in conjunction with a FUSE to enable mounting a file tree into a directory.

## Values

### `:content-information`

The information about a content link.

```typescript
enum ContentKind {
    File = "File",
    Directory = "Directory",
    SymbolicLink = "SymbolicLink"
}

interface ContentInformationCommon {
    node: Node
    kind: ContentKind
    modifyTime: number
    createTime: number
    executable: boolean
    writable: boolean
    etag: string
}
```

- `node` - The node number of the content.
- `kind` - The kind of the content.
- `modifyTime` - The time the content was last modified.
- `createTime` - The time the content was created.
- `writable` - Whether the content is writable.
- `mode` - The mode of the content in octal format.
- `etag` - The etag of the content which is sha256 hash of the content. This is either the `expected` of the associated content link or `address` if their is not `expected` field.

### :entry-attributes

The attributes of a file, directory or symbolic link.

```typescript
interface EntryAttributes {
    writable?: boolean
    modifyTime?: bigint
    createTime?: bigint
    mode?: string
    size?: bigint
    type?: string
}
```

- `writable` - Whether the entry is writable. An entry is writable if it is in a directory that is writable. A directory is writable if one of its ancestors is associated with a writable slot. The slot is kept updated with the address of the new root of the file tree as changes are made.
- `modifyTime` - The time the entry was last modified.
- `createTime` - The time the entry was created.
- `mode` - The mode of the entry in octal format. `writable` takes precedence over `mode`. 
- `size` - The size of the entry. Only valid for files.
- `type` - The type of the entry. Only valid for files. A type of "-" is used to remove a type.

### `:name`

The name of of an entry in a directory.

### `:node`

The node number of a file, directory or symbolic link.

## `PUT /:node/:name`

Create a file, directory or symbolic link with the given name in a directory with the given node number. The node number must be a valid node number for a directory. The node number 1 is reserved for the root directory and is always a directory.

Note: this can be used to efficiently copy files or directories by using the `content` parameter to reference the content of the file or directory to be copied.

### Optional Query Parameters

- `kind` - The kind of the entry to create. Must be `File`, `Directory` or `SymbolicLink`.
- `target` - The target of the symbolic link. Only used if `kind` is `SymbolicLink` and required if `kind` is `SymbolicLink`.
- `content` - A content link to the content of the file. Only used if `kind` is `File` or `Directory`.  If `content` is not supplied, the file or directory is created empty.

## `GET /file/:node`

Read a file with the given node number. This reads both files and symbolic links. For symbolic links, the target is returned.

### Optional Query Parameters

- `offset` - The offset into the file to read. If offset is omitted it is read from the beginning of the file. If offset is greater than the size of the file, an empty response is returned. If offset is negative, it is relative to the end of the file.
- `length` - The length of the file to read. If length is omitted it is read until the end of the file.

### Response

A bytes stream of the request content of the file.

## `POST /file/:node`

Overwrite a file with the given node number.

### Optional Query Parameters

- `offset` - The offset into the file to write. If offset is omitted it is written from the beginning of the file. If offset is greater than the size of the file, an empty response is returned. If offset is negative, it is relative to the end of the file.
- `append` - If true, the file is appended to the end of the file. If `append` is true, `offset` is ignored.

## `GET /directory/:node`

Read a directory with the given node number.

### Optional Query Parameters

- `offset` - The offset into the directory to read. If offset is omitted it is read from the beginning of the directory. If offset is greater than the size of the directory, an empty response is returned. If offset is negative, it is relative to the end of the directory. The offset is directory entry count.
- `length` - The length of the directory to read. If length is omitted it is read until the end of the directory. Length is the number of directory entries to read.

### Response

A JSON array of directory entries.

## `GET /attributes/:node`

Read the attributes of a file, directory or symbolic link with the given node number.

### Response

The response is a JSON object of type `:entry-attributes`.

## `POST /attributes/:node`

Update the attributes of a file, directory or symbolic link with the given node number.

### Request Body

A JSON object of type `:entry-attributes`. Missing values are not modified.

### Response

A JSON object of type `:entry-attributes` of the updated attributes.

## `GET /content/:node`

Read the content link of a file with the given node number.

### Response

The response is a JSON object of type `:content-link`.

## `GET /info/:node`

Read the content information of a file with the given node number.

### Response

The response is a JSON object of type `:content-information`.

## `GET /lookup/:node/:name`

Lookup a file, directory or symbolic link with given name in the directory with the given node number.

### Response

The response is a JSON object of type `:content-information`.

## `PUT /remove/:node/:name`

Remove a file, directory or symbolic link with given name in the directory with the given node number.

## `POST /rename/:node/:name`

Rename a file, directory or symbolic link with given name in the directory with the given node number.

### Query Parameters

- `name` - The new name of the file, directory or symbolic link. This parameter is required.
- `directory` - The `:node` of the new directory. If not provided, it is `:node`.

## `PUT /link/:node/:name`

Create a link with the given name in a directory with the given node number. This is a hard link where changes made to the the node are reflected in the link and vice versa. This is not, however, presistent. Hard links are broken when the file system is unmounted.

### Query Parameters

- `node` - The node number of the file, directory or symbolic link to link to. This parameter is required.

## `PUT /sync`

Sync the file system. This is ignored if the node provided is not writable. If it is writable the first ancestor that is a directory a slot content link is updated after all its content has been written to the storage services. If the node is a file, pending writes to other files may or may not be written at the same time.

### Query Parameters

- `node` - The node number of the file or directory to sync. If not provided, it is the root directory.
- `wait` - If true, the request will wait for the sync to complete before returning. If false, the request will return immediately. The default is true. If `wait` is `false` the request will be successful even if the sync fails.

