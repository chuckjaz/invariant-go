# The invariant project - File Tree

A file tree is a directory structure modeled after a POSIX-compliant directory structure.

A directory is a collection of entries that can be files, subdirectories, or symbolic links.

- A **file** is a [Content](Content.md) link and associated metadata.
- A **directory** is a [Content](Content.md) link pointing to a JSON-encoded `Directory` array and associated metadata.
- A **symbolic link** stores a target path string to be resolved by the client.

## Directory

A directory is a JSON-encoded array of entries sorted by `name`:

```typescript
enum EntryKind {
    File = "File",
    Directory = "Directory",
    SymbolicLink = "SymbolicLink",
}

interface BaseEntry {
    kind: EntryKind;
    name: string;
    createTime?: number; // uint64 Unix seconds
    modifyTime?: number; // uint64 Unix seconds
    mode?: string;       // octal string matching ^0[0-7]{1,4}$
}

interface FileEntry extends BaseEntry {
    kind: EntryKind.File;
    content: ContentLink;
    size: number;        // uint64
    type?: string;       // MIME type matching ^[^/]+/[^/]+$
}

interface DirectoryEntry extends BaseEntry {
    kind: EntryKind.Directory;
    content: ContentLink;
    size: number;        // uint64
}

interface SymbolicLinkEntry extends BaseEntry {
    kind: EntryKind.SymbolicLink;
    target: string;
}

type Entry = FileEntry | DirectoryEntry | SymbolicLinkEntry;

type Directory = Entry[];
```

### Field Constraints and Normalization

- `name`: Must be non-empty, must not be `"."` or `".."`, must not contain `/`, and must not be a DOS reserved device name (`CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`, `LPT1`–`LPT9`, case-insensitive, with or without extensions).
- `createTime` / `modifyTime`: Unix timestamps in seconds (`uint64`). When marshaling JSON, `modifyTime` is omitted if it equals `createTime`; when unmarshaling, `modifyTime` defaults to `createTime` if omitted.
- `mode`: Optional octal POSIX mode matching `^0[0-7]{1,4}$`. This is advisory for POSIX mounts; access control is enforced via encryption and slot policies rather than `mode`.
- `size`: Size in bytes (`uint64`).
- `type`: Optional MIME type matching `^[^/]+/[^/]+$`.

### Ignore Rules (`IgnoreMatcher`)

When uploading or layering directories, `internal/filetree` uses `.invariant-ignore` and `.gitignore` rules. `.invariant` is always ignored, and `.git` is ignored whenever ignore rules are present.
