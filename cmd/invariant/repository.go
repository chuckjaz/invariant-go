package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"invariant/internal/config"
	"invariant/internal/discovery"
	"invariant/internal/finder"
	"invariant/internal/names"
	"invariant/internal/repository"
	"invariant/internal/repository/commit"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

type stringListFlag []string

func (s *stringListFlag) String() string {
	return fmt.Sprintf("%v", []string(*s))
}

func (s *stringListFlag) Set(val string) error {
	*s = append(*s, val)
	return nil
}

func runRepository(globalCfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository <command> [options]\n\n")
		fmt.Fprintf(os.Stderr, "Commands:\n")
		fmt.Fprintf(os.Stderr, "  create       Create a new repository and initialize main branch\n")
		fmt.Fprintf(os.Stderr, "  change       Create a writable change branch workspace\n")
		fmt.Fprintf(os.Stderr, "  status       Show workspace working tree changes against HEAD commit\n")
		fmt.Fprintf(os.Stderr, "  diff         Show unified diffs and statistics\n")
		fmt.Fprintf(os.Stderr, "  clean        Purge untracked files from the workspace\n")
		fmt.Fprintf(os.Stderr, "  commit       Snapshot and commit changes to the branch slot\n")
		fmt.Fprintf(os.Stderr, "  sync         Rebase workspace onto upstream HEAD\n")
		fmt.Fprintf(os.Stderr, "  submit       Fast-forward/rebase change into upstream and retire workspace\n")
		fmt.Fprintf(os.Stderr, "  log          Show commit history log\n")
		fmt.Fprintf(os.Stderr, "  show         Inspect a commit or view a file snapshot in CAS\n")
		fmt.Fprintf(os.Stderr, "  restore      Discard uncommitted edits and restore files from HEAD commit\n")
		fmt.Fprintf(os.Stderr, "  revert       Apply inverse commit patch and record revert commit\n")
		fmt.Fprintf(os.Stderr, "  blame        Show line-by-line attribution of a file\n")
		fmt.Fprintf(os.Stderr, "  grep         Search for patterns in commit trees directly in CAS\n")
		fmt.Fprintf(os.Stderr, "  stash        Shelve and restore uncommitted changes\n")
		fmt.Fprintf(os.Stderr, "  bisect       Binary search across history to locate regressions\n")
		fmt.Fprintf(os.Stderr, "  rebase       Rebase and groom commit history interactively\n")
		fmt.Fprintf(os.Stderr, "  cherry-pick  Apply commits from another branch or commit range\n")
		fmt.Fprintf(os.Stderr, "  mount        Mount an existing repository workspace\n")
		fmt.Fprintf(os.Stderr, "  unmount      Unmount repository workspace\n")
		os.Exit(1)
	}

	switch args[0] {
	case "create":
		runRepoCreate(globalCfg, args[1:])
	case "change":
		runRepoChange(globalCfg, args[1:])
	case "status":
		runRepoStatus(globalCfg, args[1:])
	case "diff":
		runRepoDiff(globalCfg, args[1:])
	case "clean":
		runRepoClean(globalCfg, args[1:])
	case "commit":
		runRepoCommit(globalCfg, args[1:])
	case "sync":
		runRepoSync(globalCfg, args[1:])
	case "submit":
		runRepoSubmit(globalCfg, args[1:])
	case "log":
		runRepoLog(globalCfg, args[1:])
	case "show":
		runRepoShow(globalCfg, args[1:])
	case "restore":
		runRepoRestore(globalCfg, args[1:])
	case "revert":
		runRepoRevert(globalCfg, args[1:])
	case "blame":
		runRepoBlame(globalCfg, args[1:])
	case "grep":
		runRepoGrep(globalCfg, args[1:])
	case "stash":
		runRepoStash(globalCfg, args[1:])
	case "bisect":
		runRepoBisect(globalCfg, args[1:])
	case "rebase":
		runRepoRebase(globalCfg, args[1:])
	case "cherry-pick":
		runRepoCherryPick(globalCfg, args[1:])
	case "mount":
		runRepoMount(globalCfg, args[1:])
	case "unmount":
		runRepoUnmount(globalCfg, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown repository command: %s\n", args[0])
		os.Exit(1)
	}
}

func initRepoClients(globalCfg *config.InvariantConfig, explicitTag string) (storage.Storage, slots.Slots, names.Names, commit.Service) {
	if globalCfg == nil || globalCfg.Discovery == "" {
		fmt.Fprintf(os.Stderr, "Invariant is not configured correctly: discovery service URL is not configured (check ~/.invariant/config.yaml)\n")
		os.Exit(1)
	}

	discClient := discovery.NewClient(globalCfg.Discovery, nil)

	findService := func(kind string, tag string) string {
		id, err := discClient.Find(context.Background(), kind, tag, 1)
		if err != nil || len(id) == 0 {
			return ""
		}
		return id[0].Address
	}

	// Determine write tag (default: "originals")
	writeTag := explicitTag
	if writeTag == "" {
		if globalCfg.Repository != nil && globalCfg.Repository.WriteTag != "" {
			writeTag = globalCfg.Repository.WriteTag
		} else if globalCfg.WriteTag != "" {
			writeTag = globalCfg.WriteTag
		} else {
			writeTag = "originals"
		}
	}
	if strings.EqualFold(writeTag, "any") {
		writeTag = ""
	}

	// 1. Initialize Storage with write tag restriction
	finderAddr := findService("finder-v1", "")
	var storageClient storage.Storage
	var aggregateOpts []storage.AggregateClientOption
	if writeTag != "" {
		aggregateOpts = append(aggregateOpts, storage.WithWriteTagOption(writeTag))
	}

	if finderAddr != "" {
		finderClient := finder.NewClient(finderAddr, nil)
		storageClient = storage.NewAggregateClient(finderClient, discClient, 3, 1000, aggregateOpts...)
	} else {
		sAddr := findService("storage-v1", writeTag)
		if sAddr == "" && writeTag != "" {
			fmt.Fprintf(os.Stderr, "Invariant is not configured correctly: storage service (storage-v1) with tag %q could not be discovered\n", writeTag)
			os.Exit(1)
		}
		if sAddr == "" {
			sAddr, _ = discovery.ResolveName(context.Background(), discClient, "storage-v1")
		}
		if sAddr == "" {
			sAddr = findService("storage-v1", "")
		}
		if sAddr == "" {
			fmt.Fprintf(os.Stderr, "Invariant is not configured correctly: storage service (storage-v1) could not be discovered\n")
			os.Exit(1)
		}
		storageClient = storage.NewClient(sAddr, nil)
	}

	// 2. Initialize Slots
	slotsAddr, err := discovery.ResolveName(context.Background(), discClient, "slots-v1")
	if err != nil || slotsAddr == "" {
		slotsAddr = findService("slots-v1", "")
	}
	if slotsAddr == "" {
		fmt.Fprintf(os.Stderr, "Invariant is not configured correctly: slots service (slots-v1) could not be discovered\n")
		os.Exit(1)
	}
	slotsClient := slots.NewClient(slotsAddr, nil)

	// 3. Initialize Names
	namesAddr, err := discovery.ResolveName(context.Background(), discClient, "names-v1")
	if err != nil || namesAddr == "" {
		namesAddr = findService("names-v1", "")
	}
	if namesAddr == "" {
		fmt.Fprintf(os.Stderr, "Invariant is not configured correctly: names service (names-v1) could not be discovered\n")
		os.Exit(1)
	}
	namesClient := names.NewClient(namesAddr, nil)

	commitSvc := commit.NewLocalService(storageClient, slotsClient, namesClient, nil)
	return storageClient, slotsClient, namesClient, commitSvc
}

func runRepoCreate(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository create", flag.ExitOnError)
	dirFlag := fs.String("d", "", "Initial content directory path")
	createOnly := fs.Bool("create-only", false, "Create repository in CAS without mounting local workspace")
	encrypted := fs.Bool("encrypt", false, "Enable encryption for repository objects")
	compressed := fs.Bool("compress", false, "Enable compression for repository objects")
	writable := fs.Bool("writable", false, "Make main branch workspace writable")
	tagFlag := fs.String("tag", "", "Storage write tag to restrict CAS writes (default: 'originals', use 'any' to write to any server)")

	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository create <name> [<content>] [-d=<dir>] [-tag=<tag>] [-create-only] [-encrypt] [-compress] [-writable]\n")
		os.Exit(1)
	}

	name := fs.Arg(0)
	contentArg := ""
	if fs.NArg() > 1 {
		contentArg = fs.Arg(1)
	}

	store, slotsClient, namesClient, commitSvc := initRepoClients(globalCfg, *tagFlag)
	ctx := context.Background()

	cfg, rootCommit, err := repository.CreateRepository(ctx, store, slotsClient, namesClient, commitSvc, repository.CreateOptions{
		Name:       name,
		Directory:  *dirFlag,
		Content:    contentArg,
		CreateOnly: *createOnly,
		Encrypted:  *encrypted,
		Compressed: *compressed,
		Writable:   *writable,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating repository: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created repository %q (main slot: %s, initial commit: %s)\n", name, cfg.MainSlotID, rootCommit)
	if !*createOnly {
		fmt.Printf("Mounted workspace at %s/main\n", name)
	}
}

func runRepoChange(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository change", flag.ExitOnError)
	privateFlag := fs.Bool("private", false, "Create private change branch not published to Names service")
	upstreamFlag := fs.String("upstream", "main", "Upstream branch to branch from")
	tagFlag := fs.String("tag", "", "Storage write tag (default: 'originals')")

	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository change <name> [-private] [-upstream=main] [-tag=<tag>]\n")
		os.Exit(1)
	}

	changeName := fs.Arg(0)
	cwd, _ := os.Getwd()

	store, slotsClient, namesClient, commitSvc := initRepoClients(globalCfg, *tagFlag)
	ctx := context.Background()

	meta, err := repository.CreateChangeBranch(ctx, store, slotsClient, namesClient, commitSvc, repository.ChangeOptions{
		RepoRoot:       cwd,
		ChangeName:     changeName,
		Private:        *privateFlag,
		UpstreamBranch: *upstreamFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating change branch: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created change branch %q (slot: %s, base: %s)\n", meta.BranchName, meta.SlotID, meta.CommitHash)
	fmt.Printf("Workspace available at %s\n", meta.WorkspaceDir)
}

func runRepoStatus(globalCfg *config.InvariantConfig, args []string) {
	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	res, err := repository.GetStatus(ctx, store, slotsClient, commitSvc, cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting status: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("On branch %s (commit %s)\n", res.BranchName, res.HeadCommit)
	if len(res.Entries) == 0 {
		fmt.Println("nothing to commit, working tree clean")
		return
	}

	fmt.Println("Changes in working tree:")
	for _, e := range res.Entries {
		switch e.Status {
		case repository.StatusAdded:
			fmt.Printf("  new file:   %s\n", e.Path)
		case repository.StatusModified:
			fmt.Printf("  modified:   %s\n", e.Path)
		case repository.StatusDeleted:
			fmt.Printf("  deleted:    %s\n", e.Path)
		}
	}
}

func runRepoDiff(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository diff", flag.ExitOnError)
	statOnly := fs.Bool("stat", false, "Show file change and line count summary statistics")
	fs.Parse(args)

	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	commit1 := ""
	commit2 := ""
	if fs.NArg() > 0 {
		commit1 = fs.Arg(0)
	}
	if fs.NArg() > 1 {
		commit2 = fs.Arg(1)
	}

	diffStr, stat, err := repository.GetDiff(ctx, store, slotsClient, commitSvc, repository.DiffOptions{
		WorkspaceDir: cwd,
		Commit1:      commit1,
		Commit2:      commit2,
		StatOnly:     *statOnly,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error computing diff: %v\n", err)
		os.Exit(1)
	}

	if *statOnly {
		for _, d := range stat.Details {
			fmt.Println(d)
		}
		fmt.Printf(" %d files changed, %d insertions(+), %d deletions(-)\n", stat.FilesChanged, stat.Insertions, stat.Deletions)
		return
	}

	if diffStr != "" {
		fmt.Print(diffStr)
	}
}

func runRepoClean(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository clean", flag.ExitOnError)
	force := fs.Bool("f", false, "Force deletion of untracked files")
	removeDirs := fs.Bool("d", false, "Remove untracked directories as well")
	removeIgnored := fs.Bool("x", false, "Also remove ignored files")
	_ = removeIgnored

	fs.Parse(args)
	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	cleaned, err := repository.CleanWorkspace(ctx, store, slotsClient, commitSvc, repository.CleanOptions{
		WorkspaceDir:  cwd,
		Force:         *force,
		RemoveDirs:    *removeDirs,
		RemoveIgnored: *removeIgnored,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error cleaning workspace: %v\n", err)
		os.Exit(1)
	}

	if len(cleaned) == 0 {
		fmt.Println("Workspace clean: no untracked files.")
		return
	}

	for _, item := range cleaned {
		if *force {
			fmt.Printf("Removed %s\n", item)
		} else {
			fmt.Printf("Would remove %s (use -f to force)\n", item)
		}
	}
}

func runRepoCommit(globalCfg *config.InvariantConfig, args []string) {
	var msgFlags stringListFlag
	fs := flag.NewFlagSet("repository commit", flag.ExitOnError)
	fs.Var(&msgFlags, "m", "Commit message line (repeatable)")
	amend := fs.Bool("amend", false, "Amend the previous commit")
	squashFlag := fs.String("squash", "", "Fold changes into an earlier commit")
	tagFlag := fs.String("tag", "", "Storage write tag (default: 'originals', use 'any' for all servers)")

	fs.Parse(args)
	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, *tagFlag)
	ctx := context.Background()

	if *squashFlag != "" {
		newHead, err := repository.ExecuteSquashCommit(ctx, store, slotsClient, commitSvc, cwd, *squashFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error squashing commit: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Squashed changes into commit %s\n", newHead)
		return
	}

	c, hash, err := repository.ExecuteCommit(ctx, store, slotsClient, commitSvc, repository.CommitOptions{
		WorkspaceDir: cwd,
		Messages:     msgFlags,
		Amend:        *amend,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error committing: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[%s] %s\n", hash[:8], c.Message)
}

func runRepoSync(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository sync", flag.ExitOnError)
	continueFlag := fs.Bool("continue", false, "Continue sync after resolving conflicts")
	abortFlag := fs.Bool("abort", false, "Abort sync and restore pre-sync state")
	tagFlag := fs.String("tag", "", "Storage write tag (default: 'originals')")

	fs.Parse(args)
	cwd, _ := os.Getwd()
	store, slotsClient, namesClient, commitSvc := initRepoClients(globalCfg, *tagFlag)
	ctx := context.Background()

	newHead, conflicts, err := repository.ExecuteSync(ctx, store, slotsClient, namesClient, commitSvc, repository.SyncOptions{
		WorkspaceDir: cwd,
		Continue:     *continueFlag,
		Abort:        *abortFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error during sync: %v\n", err)
		os.Exit(1)
	}

	if len(conflicts) > 0 {
		fmt.Fprintf(os.Stderr, "CONFLICT: Automatic merge failed. Fix conflicts and run 'invariant repository sync --continue' (or '--abort').\n")
		for _, conf := range conflicts {
			fmt.Fprintf(os.Stderr, "  %s\n", conf)
		}
		os.Exit(1)
	}

	if *abortFlag {
		fmt.Println("Sync aborted. Workspace restored to pre-sync state.")
	} else {
		fmt.Printf("Successfully synced workspace to %s\n", newHead)
	}
}

func runRepoSubmit(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository submit", flag.ExitOnError)
	target := fs.String("target", "main", "Target branch to submit to")
	tagFlag := fs.String("tag", "", "Storage write tag (default: 'originals')")
	fs.Parse(args)

	cwd, _ := os.Getwd()
	if fs.NArg() > 0 {
		cwd, _ = filepath.Abs(fs.Arg(0))
	}

	store, slotsClient, namesClient, commitSvc := initRepoClients(globalCfg, *tagFlag)
	ctx := context.Background()

	resp, err := repository.ExecuteSubmit(ctx, store, slotsClient, namesClient, commitSvc, repository.SubmitOptions{
		WorkspaceDir: cwd,
		TargetBranch: *target,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error submitting change: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully submitted change to %s (HEAD: %s)\n", *target, resp.NewHeadCommit)
}

func runRepoLog(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository log", flag.ExitOnError)
	tree := fs.Bool("tree", false, "Show full DAG history tree")
	graph := fs.Bool("graph", false, "Show full DAG history tree")
	maxCount := fs.Int("n", 0, "Limit number of commits to show")
	fs.Parse(args)

	pathFilter := ""
	if fs.NArg() > 0 {
		pathFilter = fs.Arg(0)
	}

	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	entries, err := repository.GetLog(ctx, store, slotsClient, commitSvc, repository.LogOptions{
		WorkspaceDir: cwd,
		PathFilter:   pathFilter,
		Tree:         *tree || *graph,
		MaxCount:     *maxCount,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting log: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(repository.FormatLog(entries, *tree || *graph))
}

func runRepoShow(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository show", flag.ExitOnError)
	fs.Parse(args)

	target := ""
	if fs.NArg() > 0 {
		target = fs.Arg(0)
	}

	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	res, err := repository.GetShow(ctx, store, slotsClient, commitSvc, repository.ShowOptions{
		WorkspaceDir: cwd,
		Target:       target,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error showing target: %v\n", err)
		os.Exit(1)
	}

	if res.IsFileContent {
		os.Stdout.Write(res.FileContent)
	} else {
		fmt.Print(res.FormattedText)
	}
}

func runRepoRestore(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository restore", flag.ExitOnError)
	fromCommit := fs.String("source", "", "Source commit hash to restore from (default: HEAD)")
	fs.Parse(args)

	targetPath := ""
	if fs.NArg() > 0 {
		targetPath = fs.Arg(0)
	}

	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	restored, err := repository.RestoreFiles(ctx, store, slotsClient, commitSvc, repository.RestoreOptions{
		WorkspaceDir: cwd,
		Path:         targetPath,
		SourceCommit: *fromCommit,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error restoring files: %v\n", err)
		os.Exit(1)
	}

	if len(restored) > 0 {
		fmt.Printf("Restored %d file(s) from HEAD snapshot.\n", len(restored))
	} else {
		fmt.Println("No files restored.")
	}
}

func runRepoRevert(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository revert", flag.ExitOnError)
	tagFlag := fs.String("tag", "", "Storage write tag (default: 'originals')")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository revert <commit>\n")
		os.Exit(1)
	}

	targetHash := fs.Arg(0)
	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, *tagFlag)
	ctx := context.Background()

	res, err := repository.ExecuteRevert(ctx, store, slotsClient, commitSvc, repository.RevertOptions{
		WorkspaceDir: cwd,
		CommitHash:   targetHash,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reverting commit: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[%s] %s\n", res.RevertCommitHash[:8], res.NewCommit.Message)
}

func runRepoBlame(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository blame", flag.ExitOnError)
	commitFlag := fs.String("commit", "", "Commit hash to inspect")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository blame <file>\n")
		os.Exit(1)
	}

	filePath := fs.Arg(0)
	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	lines, err := repository.GetBlame(ctx, store, slotsClient, commitSvc, repository.BlameOptions{
		WorkspaceDir: cwd,
		FilePath:     filePath,
		CommitHash:   *commitFlag,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting blame: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(repository.FormatBlame(lines))
}

func runRepoGrep(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository grep", flag.ExitOnError)
	ignoreCase := fs.Bool("i", false, "Case-insensitive matching")
	lineNumbers := fs.Bool("n", true, "Show line numbers")
	commitFlag := fs.String("commit", "", "Commit hash to search in (default: HEAD)")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository grep [-i] [-n] <pattern> [<path>]\n")
		os.Exit(1)
	}

	pattern := fs.Arg(0)
	pathFilter := ""
	if fs.NArg() > 1 {
		pathFilter = fs.Arg(1)
	}

	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	matches, err := repository.GrepTree(ctx, store, slotsClient, commitSvc, repository.GrepOptions{
		WorkspaceDir: cwd,
		Pattern:      pattern,
		CommitHash:   *commitFlag,
		IgnoreCase:   *ignoreCase,
		LineNumbers:  *lineNumbers,
		PathFilter:   pathFilter,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running grep: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(repository.FormatGrepMatches(matches, *lineNumbers))
}

func runRepoStash(globalCfg *config.InvariantConfig, args []string) {
	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	subcmd := "push"
	subargs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		subcmd = args[0]
		subargs = args[1:]
	}

	switch subcmd {
	case "push":
		fs := flag.NewFlagSet("repository stash push", flag.ExitOnError)
		msg := fs.String("m", "", "Stash description message")
		fs.Parse(subargs)

		hash, err := repository.StashPush(ctx, store, slotsClient, commitSvc, cwd, *msg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error stashing changes: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Saved working directory and index state WIP on %s\n", hash[:min(8, len(hash))])

	case "pop":
		msg, err := repository.StashPop(ctx, store, slotsClient, commitSvc, cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error popping stash: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Applied stash: %s\n", msg)

	case "list":
		entries, err := repository.StashList(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing stash: %v\n", err)
			os.Exit(1)
		}
		fmt.Print(repository.FormatStashList(entries))

	case "drop":
		idx := 0
		if len(subargs) > 0 {
			var err error
			idx, err = strconv.Atoi(subargs[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid stash index: %s\n", subargs[0])
				os.Exit(1)
			}
		}
		if err := repository.StashDrop(cwd, idx); err != nil {
			fmt.Fprintf(os.Stderr, "Error dropping stash: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Dropped stash@{%d}\n", idx)

	default:
		fmt.Fprintf(os.Stderr, "Unknown stash command: %s\n", subcmd)
		os.Exit(1)
	}
}

func runRepoBisect(globalCfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository bisect <start|good|bad|reset|run> [options]\n")
		os.Exit(1)
	}

	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	switch args[0] {
	case "start":
		badCommit := ""
		goodCommit := ""
		if len(args) > 1 {
			badCommit = args[1]
		}
		if len(args) > 2 {
			goodCommit = args[2]
		}
		cand, rem, err := repository.BisectStart(ctx, store, slotsClient, commitSvc, cwd, badCommit, goodCommit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error starting bisect: %v\n", err)
			os.Exit(1)
		}
		if cand != "" {
			fmt.Printf("Bisecting: %d revisions left to test after this (candidate: %s)\n", rem, cand[:min(8, len(cand))])
		} else {
			fmt.Println("Bisect session started.")
		}

	case "bad":
		commitHash := ""
		if len(args) > 1 {
			commitHash = args[1]
		}
		cand, rem, found, err := repository.BisectMark(ctx, store, slotsClient, commitSvc, cwd, false, commitHash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marking bad commit: %v\n", err)
			os.Exit(1)
		}
		if found {
			fmt.Printf("%s is the first bad commit\n", cand)
		} else {
			fmt.Printf("Bisecting: %d revisions left to test after this (candidate: %s)\n", rem, cand[:min(8, len(cand))])
		}

	case "good":
		commitHash := ""
		if len(args) > 1 {
			commitHash = args[1]
		}
		cand, rem, found, err := repository.BisectMark(ctx, store, slotsClient, commitSvc, cwd, true, commitHash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marking good commit: %v\n", err)
			os.Exit(1)
		}
		if found {
			fmt.Printf("%s is the first bad commit\n", cand)
		} else {
			fmt.Printf("Bisecting: %d revisions left to test after this (candidate: %s)\n", rem, cand[:min(8, len(cand))])
		}

	case "reset":
		if err := repository.BisectReset(ctx, store, slotsClient, commitSvc, cwd); err != nil {
			fmt.Fprintf(os.Stderr, "Error resetting bisect: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Bisect session reset.")

	case "run":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "Usage: invariant repository bisect run <command> [args...]\n")
			os.Exit(1)
		}
		culprit, err := repository.BisectRun(ctx, store, slotsClient, commitSvc, cwd, args[1:])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error during bisect run: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s is the first bad commit\n", culprit)

	default:
		fmt.Fprintf(os.Stderr, "Unknown bisect command: %s\n", args[0])
		os.Exit(1)
	}
}

func runRepoRebase(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository rebase", flag.ExitOnError)
	interactive := fs.Bool("i", false, "Interactively edit, reorder, squash, or drop commits")
	fs.Parse(args)

	upstream := ""
	if fs.NArg() > 0 {
		upstream = fs.Arg(0)
	}

	if !*interactive {
		fmt.Println("Non-interactive rebase: use 'invariant repository sync' for syncing with upstream branch.")
		return
	}

	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	newHead, err := repository.ExecuteInteractiveRebase(ctx, store, slotsClient, commitSvc, cwd, upstream, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error during interactive rebase: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully rebased and updated branch to %s\n", newHead)
}

func runRepoCherryPick(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("repository cherry-pick", flag.ExitOnError)
	tagFlag := fs.String("tag", "", "Storage write tag (default: 'originals')")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository cherry-pick <branch|commit> [<end-commit>]\n")
		os.Exit(1)
	}

	target := fs.Arg(0)
	endCommit := ""
	if fs.NArg() > 1 {
		endCommit = fs.Arg(1)
	}

	cwd, _ := os.Getwd()
	store, slotsClient, namesClient, commitSvc := initRepoClients(globalCfg, *tagFlag)
	ctx := context.Background()

	created, err := repository.ExecuteCherryPick(ctx, store, slotsClient, namesClient, commitSvc, repository.CherryPickOptions{
		WorkspaceDir: cwd,
		Target:       target,
		EndCommit:    endCommit,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error during cherry-pick: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Cherry-picked %d commit(s). New HEAD: %s\n", len(created), created[len(created)-1])
}

func runRepoMount(globalCfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository mount <name> [<directory>]\n")
		os.Exit(1)
	}

	repoName := args[0]
	targetDir := ""
	if len(args) > 1 {
		targetDir = args[1]
	}

	store, slotsClient, namesClient, commitSvc := initRepoClients(globalCfg, "")
	ctx := context.Background()

	meta, err := repository.MountRepository(ctx, store, slotsClient, namesClient, commitSvc, repoName, targetDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error mounting repository: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Mounted repository %s (branch: %s) at %s\n", meta.RepoName, meta.BranchName, meta.WorkspaceDir)
}

func runRepoUnmount(globalCfg *config.InvariantConfig, args []string) {
	targetDir := "."
	if len(args) > 0 {
		targetDir = args[0]
	}

	if err := repository.UnmountRepository(targetDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error unmounting repository: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Unmounted repository workspace at %s\n", targetDir)
}
