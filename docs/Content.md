# The invariant project - Content

Content is a set of blocks that, taken together, form a complete file. The content is described by a :content-link type that includes the address of the content or the root block of the content and any transforms that should be performed on the content while it is being retrieved.

## Version

The version 1 of the content protocol with the protocol token of content-v1.

## Values

### `:address`

The address of the content or the root block of the content.

### `:transforms`

A list of transforms to be performed on the content while it is being retrieved.

## Content Link

A content link is a JSON object with the TypeScript type of,

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
    DecompressTransform

interface BlocksTransform {
    kind: "Blocks"
}

interface AesCbcDecipherTransform {
    kind: "Decipher"
    algorithm: "aes-256-cbc"
    key: string
    iv: string
}

interface DecompressTransform {
    kind: "Decompress"
    algorithm: "inflate" | "brotli" | "unzip"
}
```

The `address` field is the address of the content or the the `slot` ID, if it is a `slot` reference. The `slot` field is true if the address is a slot ID. The `transforms` field is a list of transforms to be performed on the content while it is being retrieved. The `expected` field is the expected address (sha256 hash) of the content after the transforms are performed. The `primary` field is the ID of a storage that is highly likely to contain the `:address`.

## Reading a `:content-link`

To read a `:content-link`, the root block is retrieved from a storage service. Perferably this is an `storage.AggerageClient`, which uses a `finder-v1` service to find the best storage service to retrieve the content from. If the `primary` field is present, and the aggregate storage client cannot find the block, the reader should try to retrieve the block from the storage service with the `primary` ID. The reader MUST perform the transforms in the order they are listed but MUST NOT require the same order as is currently required by the writer.

### Transforms

The transforms are performed in the order they are listed where the output of one transform is the input to the next transform. If the `expected` field is present, the `sha256` hash of the output of the last transform must match the `expected` field, if it exists.

There are three supported transforms:

#### Blocks

This transform indicates that `:address` refers to a JSON encoded block list where each block is a `:content-link` and size. The type of the block list is:

```ts
interface BlockList {
    blocks: {
        content: ContentLink;
        size: bigint;
    }[];
}
```

A block may be another block list, so this can be recursive. The size is the size of the block in bytes. The size must be able to be represented as an unsigned 64 bit integer.

The transform is performed by retrieving all the blocks and concatenating them in order. Each block is retrieved using the `address` and `transforms` from the `blocks` list. The `slot` field is ignored. The resulting content is the concatenation of all the blocks.

#### AesCbcDecipher

This transform is used to decrypt content that has been encrypted with AES-256-CBC. Other encryption algorithms may be supported in the future.

#### Decompress

This transform is used to decompress content that has been compressed with deflate, brotli, or unzip. Other compression algorithms may be supported in the future.

## Writing a `:content-link`

To write a `:content-link`, the stream of bytes SHOULD be split into approximately 1 MB blocks, which MUST NOT exceed 2 MB. If a stream is less than 1 MB is SHOULD NOT be split into multiple blocks. The split blocks are then compressed if requested, and then encrypted if requested. If a block list is larger than 1 MB it SHOULD also itself be split into blocks which form a block tree. Once all the non-root blocks are written, the root block is written. The writer then can return a `:content-link` that whose `:address` is either a block list or the content's `:address`.

Each block can have its own encryption and compression, which is recorded in the resulting `:content-link`'s `:transforms` field.

### Block Splitting

A block is a contiguous sequence of bytes. If a stream is split into blocks, the blocks MUST be of approximately the same size, and MUST NOT exceed 2 MB. If a stream is less than 1 MB it SHOULD NOT be split into blocks. The technique for splitting the blocks is up to the implementer. However, the writer SHOULD choose an algorithm that is likely to produce blocks that are of approximately the same size and increase the chance that blocks will be shared between files. This can be done by using a rolling hash to find the best places to split the stream, for example. 

### Compression

Each block MAY be compressed. The compression algorithm must be one of the supported compression algorithms but either specfied by the user or selected by the writer based on which produces the smallest compressed block. 

### Encryption

Each block MAY be encrypted. If a block is encrypted, it MUST be encrypted with the same encryption algorithm and same, or greater, key length, as is used to encrypt the block list that contains the block. 

There are several techniques that can be used to select encryption keys for the block in decreasing strength.

1. Use a cryptographically random key for each block.
2. Use the same cryptographically random key for all blocks.
3. Use the encrypted `:address` of the block to derive the encryption key of the block.  

The order of these is also in decreasing strength but increasing storage efficiency. Using a unique key for each block ensures that blocks that may be common between files will never have the same `:address` so copies of the same data will produce unique blocks.

Using the same key for all blocks can share content between files as well as copies of the same file will have the same `:address`. Blocks will not be sharable among files with different keys.

The third option obscures the data but still enables sharing of blocks among encrypted files. With this option, however, a third party can determine if a store has an encrypted version of the file. A third party can determine if a store has an encrypted version of the file by computing what the `:address` of the file would be if it were encrypted and checking the store for that `:address`.

