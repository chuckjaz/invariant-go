# The invariant project - Repository

An invariant repository is a collection of branches and snapshots of original data (e.g. source files) that tracks the history of changes to the originals. Each change is tracked as a commit. A commit contains a reference its predecessor(s) commit(s) and the references can be used to produce a log of changes. Each commit has a information about the commit, including the author, a description of the change (i.e. a commit message), the date is was created, etc.

An invariant repository is very similar to a Git repository but implemented much differently. For example, instead of using the file-system (by default) a workspace is used. Instead of storing the commits, file-tree, and content address storage in a sub-directory of the repository, these are all stored in invariant. The file-tree of a commit is a normal invariant file-tree that can be mounted directly in a file-system, for example, using `invariant mount <file-tree block-id> <location>`.

## Summary

### Creating a repository

A repository is created by calling the `invariant repository create <name>` command, where `<name>` is some meaningful name of the repository. `<name>` must be a valid POSIX directory name.

As using the full name of the `invariant` command is cumbersome, we will for the remainder of this document, assume that there is a alias `ir` which is `invariant repository`. With this alias the command would be `ir create <name>`.

By default, this does three things simultaneously,

1. Create a repository slot and registers the slot with the name service named `<name>`.
2. An empty branch, called `main` is created in the repository.
3. Creates a sub-directory in the current directory with named `<name>` which will contain `main` sub-directory.
4. Mounts the repository as a workspace in that directory.

Steps 3 and 4 are optional and can be skipped by using the `-create-only` option.

By default, `main` opened in a read-only workspace. No changes can be made directly. The can be overridden by using the `-writable` flag this should only be used temporarily for a quick fix, or for small projects with one or two contributors. While read-only, the workspace will always show the most recent source. To make changes to `main` use `ir changes <name>` which create a writable change set of `main` called `<name>` and switches to its directory. By default, `main` is a shared branch and the branch created by `changes` is a private branch, private to the user that created it.

### Committing a change (commit, review, and submit)

The command `ir commit` can be used to commit a change. This, by default, will bring up a text editor of your choice to edit a commit message (or you can used the `-m` option to specify the message on the command-line). Multiple `-m` options can be specified which will be concatenated into a single commit message (separated by line-feeds).

When a change is committed it becomes the current commit of branch of the workspace. If the branch was created using `ir changes` then this commit is still private in the branch. To merge it into its shared branch, use `ir submit`. Based on the requirements of the shared branch, the `submit` will either become the new current or fails. By default, a `submit` will succeed if the commit it is branched from is the branches current commit (e.g. it can "fast-forward"). The branch may have other criteria (e.g. proof tests pass, formatting has run, code-review signatures present, etc.) as well. By default, once a `submit` completes the workspace is retired (that is, it is no longer mounted and the working directory is changed to the upstream workspace).

Multiple commits in a single `changes` branch are considered a change set and are synchronized together. When submitted they, by default, are stacked on top of the upstream changes. However, they can also be squashed into a single commit or merged depending on either `changes` parameters to the command or parameters to the `submit` command. A change set with a fixup commit is automatically squashed into the change behind it during the `submit`.

#### Reviews

If the upstream branch is configured to only take approved commits, you can use the `ir review request` command to request a the change be reviewed by a branch specific review system. For example, the branch may require a second developer to code-review the change and and pre-submit tests to run before it is submitted . `review` kicks this process off. If, for example, the review process can send an e-mail a developer in the reviewers list and then, once the code review is complete, the review can use the `ir review approve` command to approve the change. To review a change like this, the reviewer can use the `ir review start` command to create a workspace for the incoming review. `ir review approve`, in this workspace, will approve the change and remove the workspace.

`review` may also be configured to connect to a review system (similar to Gerrit or a GitHub Pull Request) to automate the process of reviews and approvals. In this case `ir review request` will start this process.

### Getting the status of changes

The command `ir status` produces a list of files that have been changed, added, or removed, These are the files that will be committed by the next `ir commit`. Be default, `ir` doesn't stage changes like Git. All changes are automatically part of the next commit.

### Producing a diff of changes

The command `ir diff` produces a diff of the changes of the files that have changed in the current directory.

### Getting a log of changes.

In a branch directory, you can call `ir log` to get a change log of all the files. This produces a log along a spine of the changes, ignoring merge commits. There are options to get a more detailed log as as a tree of commits.

### Synchronizing changes

A change workspaces is isolated from the upstream changes until `ir sync` is invoked in the workspace. This rebases the workspace to the latest changes in the upstream branch (or the last-known good change if it is an `lkg` branch). This happens automatically with `ir submit` during the submit process. `ir sync` allows resolving any merge conflicts that may prevent a `ir submit` from succeeding.

### Switching branches

Branches are subdirectories of the repository directory. To switch branches just `cd` into the branch directory.


## Commands

### `ir create <name> [<content>]`

Create a new repository by the given name. The file is opened in a subdirectory of the current directory.

#### Arguments

##### `<name>`

The name of the repository to create. This name is used to create a subdirectory of the current directory when the directory repository is open.

##### `<content>`

The name, address, or content link for the initial content. A directory can be imported by using `-d` option which will upload the content of the directory then create the repository with the uploaded content.

#### Options

##### `-create-only`

Creates the repository without opening it.

##### `-d=<path>, -directory=<path`

Uses the content of `<path>` as the initial content of the repository. This becomes the content of the `main` branch of the repository. This option cannot be combined with a `<content>` argument.

##### `-encrypt`

Encrypt the content of the repository. The repository will be encrypted at rest even when mounted. The files in the repository will only be decrypted when read. All data written will be encrypted before being stored.

##### `-compress`

Compress the content of the repository. The repository will be compressed at rest. It will only be decompressed when read. Files will be compressed when written.

##### `-writable`

The workspace is opened as `-writable` instead of read-only.


### `ir change <name>`

Create a change workspace.

The name is registered with the names service as `:<user>:<repository name>:<name>` unless `-private` is used.

#### Arguments

##### `<name>`

The name of the workspace that is created as a subdirectory of the current directory. The upstream branch is the branch of the current directory. For example, in a repository's `main` branch directory the upstream will be `main`.

#### Options

##### `-private`

Do not publish the branch with the names service.


### `ir cherry-pick <branch|commit> [<commit>]`

Take the changes and apply them to the current workspace (in order, if multiple commits are selected).

#### Options

##### `branch`

The name of a branch to cherry-pick which pick all the changes until the branches converge.

##### `<commit> [<commit>]`

The SHA commit (or a unique prefix) which will cherry-pick that commit. If two commits are mentioned it will cherry-pick both commits and all commits in between.


### `ir commit`

Commit all added, removed, and modified files in workspace to its corresponding branch. If no `-m`, or the `-e` option is used, an editor is started to edit the message.

#### Options

##### `-amend`

Update the previous commit with the current changes.

##### `-e, -edit`

Open an editor for the commit message.

##### `-no-edit`

Do not bring up an editor.

##### `-m <message>, -message <message>`

Use the option value as the message for the commit. This option can be repeated and the values are concatenated with new-lines.


### `ir git export [<repository directory>]`

Export the commits in the branch to the corresponding branch of the given repository.


### `ir git import [<repository directory>]`

Import the commits into this branch from the corresponding branch from the given repository.


### `ir mount [<directory>]`

Mount a repository in the given directory. If there is no `<directory>` argument then the current directory is used.

#### Arguments

##### `<directory>`

The directory to mount. This directory must be a directory crated by `create` or `clone`.


### `ir review abandon [<directory>]`

Abandon a review. This closes and removes the directory containing the review without additional comment.

#### Arguments

##### `[<directory>]`

The directory of the review branch. If not supplied it is assumed that the current directory is the directory, or sub-direction, of the review branch being abandoned. In either case, if the current directory is in the review branch directory the current directory will be changed to the repository directory.


### `ir review approve [<directory>]`

Approve the review. This will mark the review branch as approved and the review branch is closed.

#### Arguments

##### `[<directory>]`

The directory of the review branch. If not supplied it is assumed that the current directory is the directory, or sub-direction, of the review branch being abandoned. In either case, if the current directory is in the review branch directory the current directory will be changed to the repository directory.


### `ir review comment [<comment-file>]`

Add a comment to a review.

#### Arguments

#### `[<comment-file]`

Provide a file, in JSON format, that has the review comments.

The format of the JSON file has the following TypeScript type definition.

```ts
type ReviewCommentFile = ReviewComment[]

interface Comment {
    // The review comment. This is assumed to be in mark-down format.
    comment: string

    // The author of the comment.
    author?: string

    // The token, sha or name of an associated review branch that contains changes
    // requested by this comment.
    branch?: string
}

interface ReviewComment {
    comments: Comment[]

    // The repository root relative path to the file.
    file?: string

    // The offset, in UTF-8 code-units, from the beginning of the file where the comments starts.
    offset?: number

    // The number of UTF-8 code-units the comment applies to.
    len?: number

    // The starting line number of the code the comment applies to.
    startLine?: number

    // The next line after the code the comment applies to.
    endLine?: number

    // Whether the comment has been resolved.
    resolved?: boolean
}
```

If only `comments` is supplied, they are comments for the entire review. If only `comments` and `files` are supplied it is a comment for the entire file. The `author`, if missing, is supplied as the logged in user. If the `author` is not the logged in user, special authorization may be required to update the comment.

The intent is either `offset` and `len` are used, or `startLine` and `endLine` are used. If they are used together, the `startLine`, `endLine` are ignored. If `offset` is supplied without `len`, or `startLine` is supplied without `endLine`, the comment is assumed to be to the end of the line the comment starts on. Line should be determined based on the source language rules. When in doubt (or the language rules are unknown), use `offset` and `len` instead.

If a comment already exists that matches the comment, the comment file is assumed to update the comment. Updates for comments not authored by the logged in user may be rejected.

### `ir review comments [<directory>]`

Display the current comments for a review. By default, this produces a mark-down formatted text that show the review comments below the snippet of code that it applies to (with more code surrounding it for context).

Raw JSON of the review can be requested by using `-json`.

#### Arguments

##### `[<directory>]`

The directory of the review branch. If not supplied it is assumed that the current directory is the directory, or sub-direction, of the review branch.

#### Options

##### `-json`

Display the JSON of the review instead of the mark-down of the review.


### `ir review open <sha>|<token>|<name>`

Open a read-only review branch without starting the view. This creates the review branch directory but doesn't mark the review as started.

#### Arguments

##### `<sha>`

The SHA code of the commit tracking the review.

##### `<token>`

The token a review emitted when a review is requested. This token will not change over the lifetime of the review while the `<sha>` may change.

##### `<name>`

The name of the review registered in the naming service (if one was).

#### Options

##### `-writeable`

Open the branch as writable. The creates a writable side branch of the review branch. For example, code change suggestions can be made in this branch as commits that can be cherry-pick by the review author into the branch.

The the branch is already open or started, `open` can be used to convert the branch to a writable branch.


### `ir review start <sha>|<token>|<name>|[<directory>]`

Start a review. This will create the read-only review branch (it one is not created already by `open`). If the branch is already opened then a review can be started by calling `start` in the review directory or supplying it as an argument.

#### Arguments

##### `<sha>`

The SHA code of the commit tracking the review.

##### `<token>`

The token a review emitted when a review is requested. This token will not change over the lifetime of the review while the `<sha>` may change.

##### `<name>`

The name of the review registered in the naming service (if one was).

##### `[<directory>]`

The directory of the review branch. If not supplied it is assumed that the current directory is the directory, or sub-direction, of the review branch being abandoned. In either case, if the current directory is in the review branch directory the current directory will be changed to the repository directory.

#### Options

##### `-writeable`

Open the branch as writable. The creates a writable side branch of the review branch. For example, code change suggestions can be made in this branch as commits that can be cherry-pick by the review author into the branch.


### `ir review request [<directory>]`

Request a code review with the submitted change (or changes) in the branch `directory`. If a `directory` is not supplied it the branch is inferred from the current directory.

Requesting a review will return a `<token>` that can be used with other commands that uniquely identifies the review.

Requesting a review may also return a http link to a service that displays a web UI for the review.

#### Arguments

##### `[<directory>]`

The directory of the workspace branch being reviewed. If not supplied it is assumed that the current directory is the directory, or sub-direction, of the workspace branch.


### `ir review reject [<directory>]`

Mark the review as rejected and close the review branch.

#### Arguments

##### `[<directory>]`

The directory of the review branch. If not supplied it is assumed that the current directory is the directory, or sub-direction, of the review branch being rejected. In either case, if the current directory is in the review branch directory the current directory will be changed to the repository directory.


### `ir review update [<directory>]`

Update a code review with the submitted change (or changes) in the branch `directory`. If a `directory` is not supplied it the branch is inferred from the current directory.


#### Arguments

##### `[<directory>]`

The directory of the workspace branch being reviewed. If not supplied it is assumed that the current directory is the directory, or sub-direction, of the workspace branch.


### `ir submit [<directory>]`

Request current change in the change workspace be submitted to associated upstream branch and close the workspace branch.

A review may be required for a branch in which case a pending review would need to be requested and approved. Other submit rules may be required the branch manager which could cause a submit to fail.

#### Arguments

##### `[<directory>]`

The directory of the workspace branch being submitted. If not supplied it is assumed that the current directory is the directory, or sub-direction, of the workspace branch.


### `ir sync [<directory>]`

#### Arguments

##### `[<directory>]`

The directory of the workspace branch being synchronized. If not supplied it is assumed that the current directory is the directory, or sub-direction, of the workspace branch.


### `ir unmount <directory>`

Unmount the repository and all the nested workspaces.

#### Arguments

##### `[<directory>]`

The directory of the repository branch being unmounted. This must be the root directory of the repository.
