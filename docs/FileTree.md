# The invariant project - File tree

A file tree is a directory structure that is modeled after a POSIX compliant directory structure.

A directory is a collection of entries that can either be files, another directory or a symbolic link.

A file is content link, as specified in [Content](Content.md), and associated metadata.

A symbolic link is a the path of file or directory that should be resolved to get the actual file or directory.

## Directory

The directory is JSON encoded array where the elements are of the following TypeScript type,

```typescript
enum EntryKind {
    File = "File",
    Directory = "Directory",
    SymbolicLink = "SymbolicLink",
}

interface BaseEntry {
    kind: EntryKind
    name: string
    createTime?: bigint
    modifyTime?: bigint
    mode?: string
}

interface FileEntry extends BaseEntry {
    kind: EntryKind.File
    content: ContentLink
    size: bigint
    type?: string
}

interface DirectoryEntry extends BaseEntry {
    kind: EntryKind.Directory
    content: ContentLink
    size: bigint
}

interface SymbolicLinkEntry extends BaseEntry {
    kind: EntryKind.SymbolicLink
    target: string
}

type Entry = FileEntry | DirectoryEntry | SymbolicLinkEntry

type Directory = Entry[]
```

The `name` field the name of the entry and should be a POSIX compliant file name.  The `name` field should not contain any path separators.  The `name` field should not be empty.  The `name` field should not be "." or "..". The DOS special names should also be avoided, such as CON, PRN, AUX, NUL, COM1-9, and LPT1-9, case insensitive, with or without extensions.

The `createTime` and `modifyTime` fields are the creation and modification times of the entry in seconds since the epoch (Jan 1, 1970 UTC)  The `createTime` and `modifyTime` fields are optional.

The `mode` field is the POSIX mode of the entry in octal format.  The `mode` field is optional. The mode is not enforeced by the invariant project and is only a suggestion to clients of the invariant project when the directory is mounted into a POSIX filesystem. The invariant project clients should, instead, rely on encryption and slot access control to control access, not `mode`.

The `size` field is the size of the entry in bytes. The `size` field is required for file entries.

The `type` field is the MIME type of the file entry. The `type` field is optional.