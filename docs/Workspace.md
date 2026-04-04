# The invariant project - Workspace

An invariant workspace is a layered directory that can be mounted and contains source files and the files those sources
produce. The source files are persistent and findable outside the workspace but the produced files are, by default,
only stored temporarily. When using a workspace, it is assumed that the produced files can be recreated at any time by
running the tools that produce the files from the source files.

## Using a workspace

### Creating a workspace

A workspace is created using the `invariant workspace create` command. The command has the following arguments:

- `directory` - the directory to create the workspace.
- `content` - the initial content of the directory.

The `create` command will create the directory and a `.invariant-workspace` which is a JSON file that contains an
object with a `content` field which is the content link to the workspace directory. Unless the `-create-only` option
is provided, mounted over the directory created (see mounting below).

The `content` can be a file tree address, slot address or content link. If the address provided is to a file tree,
a slot will be created and substituted for the file tree address.

A workspace directory only contains a `.invariant-layer` file which describes the layers of the workspace. Mounting
a workspace reads this file and mounts the workspace directory content in the directory.

### Mounting a workspace

A workspace is mounted using the `invariant workspace mount` command. The command has the following argument:

- `directory` - the workspace directory to mount.

The `directory` must refer to a workspace directory (that is, contain a `.invariant-workspace` file).

If a `-systemd` flag is provided then the workspace will be remounted on boot using systemd.
If a `-foreground` flag is provided command  will mount directly instead of spawning a background task.

### Unmounting a workspace

A workspace is un-mounted by using `invariant workspace unmount`. The `unmount` command has the following argument:

- `directory` - the workspace directory to unmount.

If `-systemd` was supplied when mounted, systemd configuration is removed for the mount.

`fusermount -u` can also be used to unmount the directory but it does not update the systemd configuration.

## Implementation

### Creating the workspace

A workspace consists of multiple file-trees that are merged together to form one logical directory. The layers
are the base layer (the file tree containing the `.invariant-layer` file), the source layer (the file tree of the
sources) and zero or more produced file layers.

The `create` command will

1. Load the reference file tree
    1. if it it has a `.invariant-share` file, add the layers described in description in the file.
    2. add any layers requested in the `-layers` option.
    3. add the source layer. If `.invariant-ignore` or `.gitignore`, add the ignore statements to the `Exclude`
    array of the layer.
2. If any files are ignored by the source layer, create a temporary layer for all other files.

#### Example

For example, a workspace can be created for the `invariant-go` project by first cloning
`https://github.com/chuckjaz/invariant-go` and then using `invariant upload` to upload the content of the directory.
Then a workspace can be created using `invariant workspace create invariant-go-mnt <address>` where `<address>` is the
address returned by `upload`. This will mount a copy of the sources in `invariant-go-mnt`.

This directory will contain a `.invariant-layer` file containing something like,

```json
[
    {
        "rootLink": { "address": "<slot>", "slot": true },
        "exclude": ["bin/"]
    },
    {
        "rootLink": "temporary",
        "storageDestination": "local"
    }
]
```

This means that the sources are assumed to be anything not in the `bin` directory. A `rootLink` of `temporary` creates
a temporary slot that will last only as long as the mount (this is not preserved across unmount/mount or reboot). A
storage destination of `"local"` will only be stored locally.

A `.invariant-share` file and `-layers` option can be used to create more specialized workspaces. The `.invariant-share`
layers are always first followed by the `-layers`. When resolving the layer names, first a file is looked for in the
source root directory called `.invariant-<name>` where `<name>` is replaced by the layer name. If that is not found the
name is looked for in the name server.

### Mounting the workspace

The workspace is mounted by reading the `.invariant-workspace` file in the directory and mounting the content link that
is contained in the `content` field. The additional fields, if present, are ignored and are reserved for future versions.
