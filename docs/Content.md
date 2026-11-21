# The invariant project - Content

Content is a set of blocks that, taken together, form a complete file or directory payload. The content is described by a `:content-link` object that includes the address of the content (or root block / slot) and any transforms that must be performed on the content while it is being retrieved.

## Version

The current version of the content protocol is `content-v1`.

## Values

### `:address`

The 32-byte hex-encoded SHA-256 address of the content, root block list, or slot.

### `:id`

A service or slot ID.

### `:transforms`

An ordered list of transforms to be performed on the content while it is being retrieved.

## Content Link

A content link is a JSON object with the TypeScript type of:

```ts
interface ContentLink {
    address: string;
    slot?: boolean;
    transforms?: ContentTransform[];
    expected?: string;
    primary?: string;
}

type ContentTransform =
    BlocksTransform |
    AesCbcDecipherTransform |
    DecompressTransform;

interface BlocksTransform {
    kind: "Blocks";
}

interface AesCbcDecipherTransform {
    kind: "Decipher";
    algorithm: "aes-256-cbc";
    key?: string;
    iv: string;
}

interface DecompressTransform {
    kind: "Decompress";
    algorithm: "inflate" | "gzip" | "zstd";
}
```

- `address`: The content address or the slot ID (when `slot` is `true`).
- `slot`: `true` if `address` is a slot ID.
- `transforms`: Ordered list of transforms to apply when reading.
- `expected`: The expected SHA-256 hash of the final plaintext content after all transforms are performed.
- `primary`: The ID of a storage service that is likely to hold `:address`.

## Reading a `:content-link`

To read a `:content-link`, if `slot` is `true`, the reader first resolves the slot ID to its current content address. The root block is retrieved from a storage service—preferably via `storage.AggregateClient`, which uses `finder-v1` to locate the closest storage service. If `primary` is present and the aggregate client cannot find the block, the reader SHOULD try the storage service with the `primary` ID.

The reader MUST perform the `transforms` in the order they are listed, and if `expected` is present, verify that the SHA-256 hash of the final output matches `expected`.

### Transforms

The output of each transform is the input to the next transform:

#### `Blocks`

Indicates that `:address` refers to a JSON-encoded block list (`BlockList`):

```ts
interface BlockList {
    blocks: {
        content: ContentLink;
        size: number; // uint64
    }[];
}
```

A block in `blocks` may itself be another block list (recursive block trees). `size` is the uncompressed/plaintext size of that block in bytes (`uint64`).

The transform retrieves all blocks and concatenates them in order (ignoring `slot` on child links). The reader prefetches and caches blocks in an LRU cache and supports seeking by byte offset.

#### `Decipher` (`aes-256-cbc`)

Decrypts AES-256-CBC (PKCS#7 padded) ciphertext using `key` (32-byte hex) and `iv` (16-byte hex). If `key` is omitted or empty, the reader falls back to `link.expected` (used by the `Deterministic` key policy).

#### `Decompress` (`inflate` | `gzip` | `zstd`)

Decompresses content compressed with raw deflate (`inflate`), `gzip`, or Zstandard (`zstd`).

## Writing a `:content-link`

When writing content, streams larger than the target block size (~1 MB, max 2 MB) are split into blocks. Each block is optionally compressed first, then optionally encrypted (so `transforms` lists `Decipher` before `Decompress`). If a serialized `BlockList` exceeds 2 MB, it is recursively split into chunks of 1000 items, and a `"Blocks"` transform is appended last.

### Block Splitting

Available content-defined splitters in `internal/content`:

- **`BuzHashSplitter`** (default fallback): 64-byte rolling window, ~1 MB target (`mask = (1<<20)-1`), 512 KB minimum, 2 MB maximum.
- **`RepMaxSplitter`**: Content-defined chunker with 512 KB minimum chunk size and 128 KB horizon.
- **`ZipSplitter`**: Detects ZIP local file headers (`PK\x03\x04`) and splits on ZIP entry boundaries (bundling header + payload if `<= 1 MB`, or streaming large payloads with inner splitters).

### Compression

Each block MAY be compressed using `inflate`, `gzip`, or `zstd`.

### Encryption & Key Policies

Each block MAY be encrypted with `aes-256-cbc` using one of four key policies:

1. **`RandomPerBlock`** (default when empty): Generates a fresh cryptographically random 32-byte key and 16-byte IV per block. Strongest isolation; identical blocks do not deduplicate.
2. **`RandomAllKey`**: Generates one random 32-byte key for the entire write and random IVs per block, allowing deduplication within the same key.
3. **`Deterministic`**: Uses the SHA-256 hash of the plaintext as the encryption key (omitted from the `Decipher` transform and inferred from `expected`) and `SHA-256(key + data)[:16]` as the IV. Enables cross-user deduplication of identical encrypted content.
4. **`SuppliedAllKey`**: Uses a caller-supplied 32-byte key with deterministic IV `SHA-256(key + data)[:16]`.
