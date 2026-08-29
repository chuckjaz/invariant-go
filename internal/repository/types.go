// Package repository provides core types, commit graph models, naming schemas,
// and workflow command orchestrations for the Invariant Repository (ir) version control system.
package repository

import (
	"invariant/internal/repository/commit"
	"invariant/internal/repository/config"
	"invariant/internal/repository/identity"
	"invariant/internal/repository/review"
)

// Identity captures user identity and authorization credentials.
type Identity = identity.Identity

// IdentityProvider abstracts user identity and authorization lookup across different backends.
type IdentityProvider = identity.Provider

// Commit represents an immutable snapshot and history node in an Invariant repository.
type Commit = commit.Commit

// CanonicalTag represents a key-value pair for deterministic serialization.
type CanonicalTag = commit.CanonicalTag

// CanonicalRef represents a key-value ref mapping for deterministic serialization.
type CanonicalRef = commit.CanonicalRef

// LayerDependency represents a pinned sub-repository layer in a workspace.
type LayerDependency = config.LayerDependency

// RepositoryConfig represents the configuration stored for a repository.
type RepositoryConfig = config.RepositoryConfig

// Comment represents a single comment entry in a review thread.
type Comment = review.Comment

// ReviewComment represents a targeted review comment thread on a file or line range.
type ReviewComment = review.ReviewComment

// ReviewStatus represents the state of a code review.
type ReviewStatus = review.Status

// ReviewRecord holds the complete metadata and comment threads for a code review.
type ReviewRecord = review.Record

// BlameLine captures line-by-line commit attribution.
type BlameLine = commit.BlameLine

// DiffStat summarizes the count of files changed and lines inserted or deleted.
type DiffStat = commit.DiffStat

// RebaseActionType specifies the action type in an interactive rebase plan.
type RebaseActionType = commit.RebaseActionType

// RebaseAction specifies an individual operation in an interactive rebase plan.
type RebaseAction = commit.RebaseAction

// CanonicalCommitJSON formats a Commit into deterministic canonical JSON bytes.
var CanonicalCommitJSON = commit.CanonicalCommitJSON

// CalculateCommitHash computes the deterministic SHA256 hex hash of a Commit.
var CalculateCommitHash = commit.CalculateCommitHash

// WriteCommit serializes and writes a Commit into CAS storage and returns its deterministic SHA-256 hash address.
var WriteCommit = commit.WriteCommit

// ReadCommit reads and deserializes a Commit from CAS storage by its content address hash.
var ReadCommit = commit.ReadCommit

// WriteRepositoryConfig serializes and writes a RepositoryConfig into CAS storage.
var WriteRepositoryConfig = config.WriteRepositoryConfig

// ReadRepositoryConfig reads and deserializes a RepositoryConfig from CAS storage.
var ReadRepositoryConfig = config.ReadRepositoryConfig

// SortTags returns a slice of CanonicalTag sorted alphabetically by Key.
var SortTags = commit.SortTags

// SortRefs returns a slice of CanonicalRef sorted alphabetically by Name.
var SortRefs = commit.SortRefs

// CurrentIdentity resolves the active user's identity using the default provider chain.
var CurrentIdentity = identity.CurrentIdentity

// DefaultIdentityProvider returns the current global IdentityProvider.
var DefaultIdentityProvider = identity.DefaultProvider

// SetDefaultIdentityProvider sets the global IdentityProvider.
var SetDefaultIdentityProvider = identity.SetDefaultProvider
