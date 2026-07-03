package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"invariant/internal/config"
	"invariant/internal/discovery"
	"invariant/internal/gitscan"
	"invariant/internal/kv"
)

func runScanRepo(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("scan-repo", flag.ExitOnError)
	var owner string
	var repo string
	var token string
	var commit string
	var depth int
	var kvURL string
	var discoveryURL string
	var concurrency int
	var localPath string

	fs.StringVar(&owner, "owner", "", "GitHub owner/org name (required for remote scan)")
	fs.StringVar(&repo, "repo", "", "GitHub repository name (required for remote scan)")
	fs.StringVar(&token, "token", "", "GitHub personal access token (optional)")
	fs.StringVar(&commit, "commit", "", "Git commit SHA1 to start scanning from (required)")
	fs.IntVar(&depth, "depth", 1, "Ancestry depth limit for scanning commits (use -1 for unlimited)")
	fs.StringVar(&kvURL, "kv", "", "URL of the KV service")
	fs.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	fs.IntVar(&concurrency, "concurrency", 20, "Number of concurrent requests to GitHub/local processing")
	fs.StringVar(&localPath, "local", "", "Local repository path (optional, if specified scans locally instead of GitHub)")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant scan-repo --owner <owner> --repo <repo> --commit <commit-sha> [options]\n")
		fmt.Fprintf(os.Stderr, "       invariant scan-repo --local <path> --commit <commit-sha> [options]\n")
		fmt.Fprintf(os.Stderr, "Scans the Git repository starting from a commit SHA1 and indexes Git SHA1 to SHA256 blob mappings in the KV service.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if (localPath == "" && (owner == "" || repo == "")) || commit == "" {
		fmt.Fprintf(os.Stderr, "Error: either --local or both --owner and --repo are required, and --commit is required.\n\n")
		fs.Usage()
		os.Exit(1)
	}

	ctx := context.Background()

	// 1. Resolve KV service URL
	if kvURL == "" {
		if discoveryURL == "" && globalCfg != nil {
			discoveryURL = globalCfg.Discovery
		}
		if discoveryURL == "" {
			fmt.Fprintf(os.Stderr, "Error: Discovery service URL not configured. Provide via --discovery or configuration file.\n")
			os.Exit(1)
		}

		dClient := discovery.NewClient(discoveryURL, nil)
		svcs, err := dClient.Find(ctx, "kv-v1", 1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error querying discovery service: %v\n", err)
			os.Exit(1)
		}
		if len(svcs) == 0 {
			fmt.Fprintf(os.Stderr, "Error: no kv-v1 service found in discovery\n")
			os.Exit(1)
		}
		kvURL = svcs[0].Address
	}

	kvClient := kv.NewClient(kvURL, nil)

	var err error
	adapter := scannerAdapter{client: kvClient}
	if localPath != "" {
		err = gitscan.ScanLocal(ctx, adapter, localPath, commit, depth, concurrency, os.Stdout)
	} else {
		err = gitscan.ScanRemote(ctx, adapter, owner, repo, token, commit, depth, concurrency, os.Stdout)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning repository: %v\n", err)
		os.Exit(1)
	}
}

type scannerAdapter struct {
	client *kv.Client
}

func (a scannerAdapter) BatchGet(ctx context.Context, txID *uint64, keys []string) (interface{}, error) {
	return a.client.BatchGet(ctx, txID, keys)
}

func (a scannerAdapter) BatchPut(ctx context.Context, txID *uint64, kvs map[string][]byte) (uint64, error) {
	return a.client.BatchPut(ctx, txID, kvs)
}
