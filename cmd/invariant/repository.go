package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"invariant/internal/config"
	"invariant/internal/discovery"
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
		fmt.Fprintf(os.Stderr, "  create      Create a new repository and initialize main branch\n")
		fmt.Fprintf(os.Stderr, "  change      Create a writable change branch workspace\n")
		fmt.Fprintf(os.Stderr, "  status      Show workspace working tree changes against HEAD commit\n")
		fmt.Fprintf(os.Stderr, "  diff        Show unified diffs and statistics\n")
		fmt.Fprintf(os.Stderr, "  clean       Purge untracked files from the workspace\n")
		fmt.Fprintf(os.Stderr, "  commit      Snapshot and commit changes to the branch slot\n")
		fmt.Fprintf(os.Stderr, "  sync        Rebase workspace onto upstream HEAD\n")
		fmt.Fprintf(os.Stderr, "  submit      Fast-forward/rebase change into upstream and retire workspace\n")
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
	default:
		fmt.Fprintf(os.Stderr, "Unknown repository command: %s\n", args[0])
		os.Exit(1)
	}
}

func initRepoClients(globalCfg *config.InvariantConfig) (storage.Storage, slots.Slots, names.Names, commit.Service) {
	discURL := "http://localhost:8080"
	if globalCfg != nil && globalCfg.Discovery != "" {
		discURL = globalCfg.Discovery
	}

	discClient := discovery.NewClient(discURL, nil)

	findService := func(kind string) string {
		id, err := discClient.Find(context.Background(), kind, "", 1)
		if err != nil || len(id) == 0 {
			return ""
		}
		return id[0].Address
	}

	sAddr, err := discovery.ResolveName(context.Background(), discClient, "storage-v1")
	if err != nil || sAddr == "" {
		sAddr = findService("storage-v1")
	}
	if sAddr == "" {
		sAddr = "http://localhost:8081"
	}
	storageClient := storage.NewClient(sAddr, nil)

	slotsAddr, err := discovery.ResolveName(context.Background(), discClient, "slots-v1")
	if err != nil || slotsAddr == "" {
		slotsAddr = findService("slots-v1")
	}
	if slotsAddr == "" {
		slotsAddr = "http://localhost:8082"
	}
	slotsClient := slots.NewClient(slotsAddr, nil)

	namesAddr, err := discovery.ResolveName(context.Background(), discClient, "names-v1")
	if err != nil || namesAddr == "" {
		namesAddr = findService("names-v1")
	}
	if namesAddr == "" {
		namesAddr = "http://localhost:8083"
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

	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository create <name> [<content>] [-d=<dir>] [-create-only] [-encrypt] [-compress] [-writable]\n")
		os.Exit(1)
	}

	name := fs.Arg(0)
	contentArg := ""
	if fs.NArg() > 1 {
		contentArg = fs.Arg(1)
	}

	store, slotsClient, namesClient, commitSvc := initRepoClients(globalCfg)
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

	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant repository change <name> [-private] [-upstream=main]\n")
		os.Exit(1)
	}

	changeName := fs.Arg(0)
	cwd, _ := os.Getwd()

	store, slotsClient, namesClient, commitSvc := initRepoClients(globalCfg)
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
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg)
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
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg)
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
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg)
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

	fs.Parse(args)
	cwd, _ := os.Getwd()
	store, slotsClient, _, commitSvc := initRepoClients(globalCfg)
	ctx := context.Background()

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

	fs.Parse(args)
	cwd, _ := os.Getwd()
	store, slotsClient, namesClient, commitSvc := initRepoClients(globalCfg)
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
	fs.Parse(args)

	cwd, _ := os.Getwd()
	if fs.NArg() > 0 {
		cwd, _ = filepath.Abs(fs.Arg(0))
	}

	store, slotsClient, namesClient, commitSvc := initRepoClients(globalCfg)
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
