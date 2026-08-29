# The invariant project - Repository

An invariant repository is a collection of branches and snapshots of original data (e.g. source files) that tracks the history of changes to the originals. Each change is tracked as a commit. A commit contains a reference to its predecessor commit(s), and these references are used to produce a change log. Each commit has metadata, including the author, committer, commit message, timestamp, arbitrary string metadata tags (`Tags`), and named references to other commits (`Refs`, e.g. tracking update, revision, amendment, or revert history).

An invariant repository is conceptually similar to a Git repository but implemented natively on Invariant's distributed microservices architecture. Instead of storing commits, file trees, and content-addressed blobs in a hidden local `.git` subdirectory, they are stored in Invariant's content-addressable storage (CAS), backed by mutable slots registered in the Names Service. Instead of switching branches by rewriting a single directory on the filesystem, branches and change sets are represented as isolated workspaces mounted via FUSE into subdirectories of the repository directory. The file tree of any commit is a standard Invariant file tree that can be mounted directly, for example using `invariant mount <file-tree block-id> <location>`.

Repository and review operations are mediated by dedicated service interfaces (`CommitService`, `ReviewService`, and `ConfigService`), which can be executed directly in-process by the CLI or hosted as HTTP microservices to support external tools such as Web-based code review UIs and automated CI/presubmit systems.

## Summary

### Creating a repository

A repository is created by calling the `invariant repository create <name>` command, where `<name>` is a meaningful name for the repository. `<name>` must be a valid POSIX directory name.

As using the full name of the `invariant` command is cumbersome, for the remainder of this document we assume an alias `ir` which stands for `invariant repository`. With this alias, the command is `ir create <name>`.

By default, this performs the following actions:

1. Allocates a repository slot and registers the slot with the Names Service under `<name>`.
2. Creates an empty default branch called `main` in the repository.
3. Creates a subdirectory in the current directory named `<name>`, which contains a `main` subdirectory.
4. Mounts the repository as a workspace in that directory.

Steps 3 and 4 are optional and can be skipped by using the `-create-only` option.

By default, `main` is opened in a read-only workspace. No changes can be made directly. This can be overridden by using the `-writable` flag; this should only be used temporarily for a quick fix or for small projects with one or two contributors. While read-only, the workspace will always show the most recent source. To make changes to `main`, use `ir change <name>`, which creates a writable change workspace branched from `main` called `<name>` and switches to its directory. By default, `main` is a shared branch and the branch created by `change` is a private branch, private to the user that created it.

### Committing a change (commit, review, and submit)

The command `ir commit` is used to commit a change. By default, this opens a text editor of your choice to edit a commit message (or you can use the `-m` option to specify the message on the command line). Multiple `-m` options can be specified, which will be concatenated into a single commit message (separated by newlines).

When a change is committed, it becomes the current commit of the workspace's branch. If the branch was created using `ir change`, this commit remains private to the branch. To merge it into its shared upstream branch, use `ir submit`. Based on the requirements of the shared branch, `submit` will either succeed and become the new HEAD or fail. By default, a `submit` succeeds if the commit it is branched from is the current commit of the upstream branch (i.e. it can "fast-forward"). The branch may enforce other criteria (e.g. presubmit tests pass, formatting checks succeed, required code-review approvals are present) as well. Once a `submit` completes, the change workspace is retired (unmounted and removed), and the working directory is returned to the upstream repository directory.

Multiple commits in a single `change` branch are considered a change set and are synchronized together. When submitted, they are stacked on top of upstream changes by default, or squashed into a single commit, or merged, depending on branch configuration and command options.

#### Metadata: Tags and Refs

Every commit contains two flexible mapping sections:
- `Tags`: A string-to-string map for arbitrary metadata labels. For example:
  - Code review token: `Tags["review"] = "<token>"`
  - Ephemeral stash: `Tags["stash"] = "<timestamp>"`
  - Git commit mapping: `Tags["git-commit"] = "<sha1>"`
- `Refs`: A string-to-string map where values are commit SHA256 hashes. This is used to track lineage, amendments, reverts, and revision update histories across commit iterations (e.g. `Refs["supersedes"] = "<prev_sha>"`, `Refs["reverts"] = "<reverted_sha>"`).

#### User Identity & Authentication

User identity (author/committer name, email, and authentication token) is automatically extracted from the local Tailscale daemon (`WhoIs`) when available, matching the authentication pattern used across Invariant microservices.

#### Reviews

If the upstream branch is configured to require approved commits, you can use the `ir review request` command to request that the change be reviewed by a review system. For example, the branch may require a peer code review and presubmit tests to pass before submission. `review request` kicks this process off and returns a unique review token and an optional Web UI review URL.

Reviewers can use `ir review start <token>` or `ir review open <token>` to create a review workspace. If `-writable` is passed, a writable suggestion side-branch is created where the reviewer can commit suggested patches that the author can cherry-pick. Review comments can be submitted via `ir review comment` using a structured JSON format or reviewed in markdown/JSON format via `ir review comments`. Once the review is complete, the reviewer uses `ir review approve` to approve the change and close the review workspace. The review lifecycle is exposed over HTTP REST endpoints so that external Web UIs can directly drive reviews, post comments, and grant approvals.

### Getting the status and diff of changes

The command `ir status` produces a list of files that have been changed, added, or removed. These are the files that will be committed by the next `ir commit`. By default, `ir` does not stage changes like Git; all modified, added, and deleted workspace files are automatically part of the next commit.

The command `ir diff` produces a unified diff of the changes in the current workspace against the HEAD commit, or between any two arbitrary commits (`ir diff <commit1> <commit2>`), with optional `--stat` summary output.

### Workspace Hygiene (`ir clean`)

Because workspaces support temporary overlay layers for ignored and generated files, `ir clean` can be used to purge uncommitted build artifacts and temporary files from the local layer without affecting tracked source modifications.

### Ephemeral Shelving (`ir stash`)

`ir stash` provides fast, lightweight shelving of uncommitted edits to temporary CAS commits without requiring a new workspace mount.

### Getting a log of changes

In a branch directory, you can call `ir log` to get a change log of all commits. By default, this produces a log along the first-parent spine of the changes, ignoring merge branches. Options are available to view a full graph tree of commits (`--tree`/`--graph`) or filter history by specific file paths (`ir log <path>`).

### Investigating & Debugging (`ir show`, `ir blame`, `ir bisect`, `ir grep`)

- `ir show <commit>[:<path>]`: Inspect commit metadata, diffs, or file contents at any historical commit.
- `ir blame <file>`: Line-by-line attribution showing which commit and author last modified each line.
- `ir bisect [start|good|bad|run]`: High-speed binary search over the commit DAG to pinpoint regressions.
- `ir grep <pattern>`: Search for text or regex patterns across the current tree or historical commits directly in CAS.

### Synchronizing changes

A change workspace is isolated from upstream changes until `ir sync` is invoked in the workspace. This rebases the workspace onto the latest changes in the upstream branch (or the last-known-good change if it is an `lkg` branch). `ir sync` computes a 3-way merge and allows resolving any merge conflicts before calling `ir submit`. If conflicts occur, `ir sync --continue` finalizes the rebase once resolved, or `ir sync --abort` returns to the clean pre-sync state.

### Branch Lifecycle & Collaboration (`ir branch`, `ir checkout`, `ir tag`)

- `ir branch [list|delete]`: View local workspaces, upstream branches, and discovered peer change branches (`:<user>:<repo>:*`); delete stale branches.
- `ir checkout <peer-branch>`: Mount a peer's published change branch locally to collaborate.
- `ir tag [create|list|delete]`: Manage immutable named release pointers.

### Git Interoperability & Storage Integration

Invariant provides bidirectional synchronization with Git repositories:
- `ir git import` imports commits and file trees from a local Git repository into an Invariant branch. It leverages the Key-Value (KV) service to index and query bidirectional Git SHA1 $\leftrightarrow$ Invariant SHA256 object mappings (as populated by `invariant scan-repo`), avoiding redundant hashing and content duplication.
- `ir git export` exports Invariant commits and file trees into a standard Git repository.
- Storage Service Direct Git Backend (`-git-dir`): The Invariant storage service can read objects directly from a local `.git` repository on disk using the KV SHA1 $\leftrightarrow$ SHA256 mapping without copying them into Invariant CAS upfront.
- Distribute Service Lazy Upload (`-distribute`): When paired with a Git direct storage backend, a distribute service can lazily replicate Git objects to Invariant cluster storage in the background.

### Switching branches

Branches are subdirectories of the repository directory. To switch branches, simply `cd` into the target branch directory.

---

## Commands

### `ir create <name> [<content>]`
**Status:** Implemented

Create a new repository by the given name. The repository is opened in a subdirectory of the current directory.

#### Arguments

##### `<name>`
The name of the repository to create. This name is used to create a subdirectory in the current directory and is registered with the Names Service.

##### `<content>`
The name, address, or content link for the initial content. A directory can be imported by using the `-d` option, which will upload the contents of the directory and create the repository with the uploaded content.

#### Options

##### `-create-only`
Creates the repository without opening and mounting it.

##### `-d=<path>`, `-directory=<path>`
Uses the content of `<path>` as the initial content of the repository. This becomes the content of the `main` branch of the repository. This option cannot be combined with a `<content>` argument.

##### `-encrypt`
Encrypt the content of the repository. The repository will be encrypted at rest even when mounted. Files in the repository will only be decrypted when read. All data written will be encrypted before being stored.

##### `-compress`
Compress the content of the repository. The repository will be compressed at rest. It will only be decompressed when read. Files will be compressed when written.

##### `-writable`
The workspace is opened as `-writable` instead of read-only.

---

### `ir change <name>`
**Status:** Implemented

Create a change workspace.

The name is registered with the Names Service as `:<user>:<repository name>:<name>` unless `-private` is used.

#### Arguments

##### `<name>`
The name of the workspace that is created as a subdirectory of the current repository directory. The upstream branch is the branch of the current directory (for example, in a repository's `main` branch directory, the upstream branch is `main`).

#### Options

##### `-private`
Do not publish the branch name with the Names Service.

---

### `ir cherry-pick <branch|commit> [<commit>]`
**Status:** Implemented

Take the changes from specified commits and apply them to the current workspace (in chronological order, if multiple commits are selected).

#### Options / Arguments

##### `branch`
The name of a branch to cherry-pick, which picks all changes from the branch until the branches converge.

##### `<commit> [<commit>]`
The SHA commit hash (or a unique prefix) to cherry-pick. If two commits are provided, it cherry-picks both commits and all commits in between.

---

### `ir commit`
**Status:** Implemented

Commit all added, removed, and modified files in the workspace to its corresponding branch. If neither `-m` nor `-no-edit` is provided, or if `-e` is used, a text editor is opened to edit the message.

#### Options

##### `-amend`
Update the previous commit with the current changes, recording the superseded commit in `Refs["supersedes"]`.

##### `-e`, `-edit`
Open an editor for the commit message.

##### `-no-edit`
Do not open an editor.

##### `-m <message>`, `-message <message>`
Use the option value as the message for the commit. This option can be repeated, and the values are concatenated with newlines.

##### `--squash=<commit>`
Directly fold changes into an earlier commit in the change set.

---

### `ir clean`
**Status:** Implemented

Purge uncommitted temporary and ignored files from the local workspace overlay layer without affecting tracked source modifications.

#### Options

##### `-f`, `-force`
Force file deletion.

##### `-d`
Remove untracked directories in addition to untracked files.

##### `-x`
Also remove ignored files.

---

### `ir diff`
**Status:** Implemented

Produce a unified diff of the files that have changed. Supports diffing working tree vs HEAD, working tree vs arbitrary commit, `<commit1>` vs `<commit2>`, or 3-dot branch diff against upstream.

#### Options

##### `--stat`
Display a summary of changed files with insertion/deletion counts.

---

### `ir restore [<path>]`
**Status:** Implemented

Discard uncommitted local edits and restore specific file(s) or the entire working tree directly from the current HEAD commit snapshot.

---

### `ir revert <commit>`
**Status:** Implemented

Compute the inverse patch of the specified commit and apply it as a new commit on the current branch.

---

### `ir blame <file>`
**Status:** Implemented

Annotate each line of the specified file with the commit hash, author, and timestamp from which the line originated.

---

### `ir show <commit>[:<path>]`
**Status:** Implemented

Display commit metadata and unified diff for a commit, or display the content of `<path>` at that commit snapshot without mounting.

---

### `ir grep <pattern>`
**Status:** Implemented

Search for text or regular expression patterns across commit trees directly in CAS.

---

### `ir stash`
**Status:** Implemented

Manage ephemeral working tree snapshots.

#### Subcommands

##### `push [-m <msg>]`
Snapshot uncommitted changes to a temporary CAS commit and revert working tree to clean HEAD.

##### `pop`
Apply the latest stashed snapshot onto the working tree and remove the stash entry.

##### `list`
List all stashed snapshots.

##### `drop [<index>]`
Remove a stashed snapshot.

---

### `ir bisect`
**Status:** Implemented

Binary search across the commit history to locate the commit that introduced a bug.

#### Subcommands

##### `start`
Begin a bisect session.

##### `bad [<commit>]`
Mark a commit as bad (containing the regression).

##### `good [<commit>]`
Mark a commit as good (known working state).

##### `run <script>`
Automatically run a test script at each midpoint step until the culprit commit is isolated.

---

### `ir rebase`
**Status:** Implemented

Rebase commits in a private change branch.

#### Options

##### `-i`, `-interactive`
Interactively edit, reorder, squash, or drop commits in the change branch before submitting.

---

### `ir branch`
**Status:** Unimplemented

Manage branches and discover peer branches registered in the Names Service.

#### Subcommands

##### `list`
List local branch workspaces, upstream branches, and discovered peer branches (`:<user>:<repo>:*`).

##### `delete <name>`
Delete a change branch workspace and unregister it from the Names Service.

---

### `ir checkout <branch|peer-branch>`
**Status:** Unimplemented

Checkout and mount a local or peer-published change branch (e.g. `ir checkout :alice:myrepo:feat-x`) into a local workspace directory.

---

### `ir tag`
**Status:** Unimplemented

Manage immutable release tags.

#### Subcommands

##### `create <name> [<commit>]`
Create a named release tag pointing to a commit snapshot.

##### `list`
List all release tags.

##### `delete <name>`
Delete a release tag.

---

### `ir config`
**Status:** Unimplemented

Get or set configuration properties at repository scope or global user scope.

---

### `ir layer`
**Status:** Unimplemented

Manage pinned sub-repository and component dependency layers in the workspace.

---

### `ir git export [<repository directory>]`
**Status:** Unimplemented

Export the commits in the current branch to the corresponding branch of the given Git repository, mapping Invariant file trees to Git trees using the KV service SHA1 $\leftrightarrow$ SHA256 index.

---

### `ir git import [<repository directory>]`
**Status:** Unimplemented

Import the commits into this branch from the corresponding branch of the given Git repository, using the KV service SHA1 $\leftrightarrow$ SHA256 mapping objects to convert Git trees and blobs to Invariant file trees.

---

### `ir log`
**Status:** Implemented

Display the commit history of the current branch. By default, traverses the first-parent spine.

#### Options

##### `<path>`
Filter commit history to only commits modifying `<path>`.

##### `-tree`, `-graph`
Display the full DAG commit graph including merge branches.

---

### `ir mount [<directory>]`
**Status:** Implemented

Mount a repository in the given directory. If no `<directory>` argument is provided, the current directory is used.

#### Arguments

##### `<directory>`
The directory to mount. This must be a directory created by `create` or `clone`.

---

### `ir review abandon [<directory>]`
**Status:** Unimplemented

Abandon a review. This marks the review as abandoned and closes/removes the directory containing the review without additional comment.

#### Arguments

##### `[<directory>]`
The directory of the review branch. If not supplied, it is inferred from the current directory. If the current directory is within the review branch, the working directory will be changed to the repository root directory before removal.

---

### `ir review approve [<directory>]`
**Status:** Unimplemented

Approve the review. This marks the review branch as approved and closes the review workspace.

#### Arguments

##### `[<directory>]`
The directory of the review branch. If not supplied, it is inferred from the current directory. If the current directory is within the review branch, the working directory will be changed to the repository root directory before removal.

---

### `ir review comment [<comment-file>]`
**Status:** Unimplemented

Add or update comments on a review using a JSON comment file matching the schema below.

#### Arguments

##### `[<comment-file>]`
Provide a file in JSON format containing the review comments.

The format of the JSON file matches the following TypeScript type definition:

```ts
type ReviewCommentFile = ReviewComment[]

interface Comment {
    // The review comment in markdown format.
    comment: string

    // The author of the comment.
    author?: string

    // The token, SHA, or name of an associated review branch containing changes
    // requested by this comment.
    branch?: string
}

interface ReviewComment {
    comments: Comment[]

    // The repository-root-relative path to the file.
    file?: string

    // The offset, in UTF-8 code-units, from the beginning of the file where the comment starts.
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

If only `comments` is supplied, the comments apply to the entire review. If only `comments` and `file` are supplied, the comments apply to the entire file. The `author`, if omitted, defaults to the logged-in Tailscale/OS user.

If `offset` and `len` are used, `startLine` and `endLine` are ignored. If `offset` is supplied without `len`, or `startLine` without `endLine`, the comment is assumed to span to the end of the line on which it starts. Lines should be determined based on the source language rules. When in doubt, use `offset` and `len`.

---

### `ir review comments [<directory>]`
**Status:** Unimplemented

Display the current comments for a review. By default, this produces markdown-formatted text showing comments beneath the relevant code snippet with surrounding context.

#### Arguments

##### `[<directory>]`
The directory of the review branch. If not supplied, it is inferred from the current directory.

#### Options

##### `-json`
Display the raw JSON of the review comments instead of formatted markdown.

---

### `ir review open <sha>|<token>|<name>`
**Status:** Unimplemented

Open a read-only review branch without marking the review as started. This creates the review branch directory.

#### Arguments

##### `<sha>`
The SHA hash of the commit tracking the review.

##### `<token>`
The unique token emitted when the review was requested.

##### `<name>`
The name of the review registered in the Names Service (if one was registered).

#### Options

##### `-writable`
Open the branch as writable, creating a suggestion side-branch where proposed changes can be committed for the author to cherry-pick.

---

### `ir review reject [<directory>]`
**Status:** Unimplemented

Mark the review as rejected and close the review workspace.

#### Arguments

##### `[<directory>]`
The directory of the review branch. If not supplied, it is inferred from the current directory.

---

### `ir review request [<directory>]`
**Status:** Unimplemented

Request a code review for the changes in the specified branch directory (or inferred current directory).

Returns a unique review `<token>` and an optional HTTP link to a Web UI service for the review.

#### Arguments

##### `[<directory>]`
The directory of the workspace branch being reviewed. If not supplied, the branch is inferred from the current directory.

---

### `ir review start <sha>|<token>|<name>|[<directory>]`
**Status:** Unimplemented

Start a review. This creates the review branch directory (if not already opened) and marks the review status as in-progress.

#### Arguments

##### `<sha>`
The SHA hash of the commit tracking the review.

##### `<token>`
The review token emitted when the review was requested.

##### `<name>`
The name of the review registered in the Names Service (if one was registered).

##### `[<directory>]`
The directory of the review branch if already opened.

#### Options

##### `-writable`
Open the review branch as writable.

---

### `ir review update [<directory>]`
**Status:** Unimplemented

Update an existing code review with newly committed changes in the workspace branch.

#### Arguments

##### `[<directory>]`
The directory of the workspace branch being reviewed. If not supplied, it is inferred from the current directory.

---

### `ir status`
**Status:** Implemented

Produces a list of files that have been changed, added, or removed in the workspace relative to the current branch commit.

---

### `ir submit [<directory>]`
**Status:** Implemented

Submit the current changes in the change workspace to the associated upstream branch and close/retire the workspace.

If the upstream branch requires review approval, the associated review must be approved before submission can succeed.

#### Arguments

##### `[<directory>]`
The directory of the workspace branch being submitted. If not supplied, it is inferred from the current directory.

---

### `ir sync [<directory>]`
**Status:** Implemented

Synchronize and rebase the change workspace with the latest commits in the upstream branch, performing a 3-way tree merge and reporting any merge conflicts.

#### Arguments

##### `[<directory>]`
The directory of the workspace branch being synchronized. If not supplied, it is inferred from the current directory.

#### Options

##### `--continue`
Verify that all conflict markers have been resolved, snapshot the working tree, and finalize the rebased commit.

##### `--abort`
Abort the sync operation and restore the workspace to the clean pre-sync commit state.

---

### `ir unmount <directory>`
**Status:** Implemented

Unmount the repository and all nested workspaces.

#### Arguments

##### `<directory>`
The directory of the repository being unmounted. This must be the root directory of the repository.
