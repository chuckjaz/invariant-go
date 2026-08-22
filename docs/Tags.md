# The invariant project - Configuration Tags

Configuration tags provide a mechanism for specifying configuration intent (e.g. `cache`, `source`) across Invariant tools and layered storage drivers.

## Usage

Tags are accepted via command-line flags on supporting tools and CLI subcommands:

- `-tags <list>`: A comma-separated list of intent tags (e.g. `-tags cache,source`).
- `-tag <single>`: A single tag describing intent (e.g. `-tag cache`).

## Parsing Semantics

When multiple tags are supplied across `-tags` and `-tag` flags:

1. Values are split by commas.
2. Leading and trailing whitespace is stripped.
3. Empty entries are discarded.
4. Duplicate tag entries are deduplicated while preserving order.
