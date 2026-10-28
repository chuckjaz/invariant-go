package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/chuckjaz/invariant-go/internal/config"
)

func extractLogFlag(args []string) (filtered []string, logVal bool, logSet bool) {
	filtered = make([]string, 0, len(args))
	for i, arg := range args {
		if arg == "--" {
			filtered = append(filtered, args[i:]...)
			break
		}
		if arg == "-log" || arg == "--log" {
			logVal = true
			logSet = true
			continue
		}
		if after, ok := strings.CutPrefix(arg, "-log="); ok {
			logVal = after == "1" || strings.EqualFold(after, "true")
			logSet = true
			continue
		}
		if after, ok := strings.CutPrefix(arg, "--log="); ok {
			logVal = after == "1" || strings.EqualFold(after, "true")
			logSet = true
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered, logVal, logSet
}

func fatalf(format string, args ...any) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	fmt.Fprintf(os.Stderr, format, args...)
	os.Exit(1)
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: invariant [-log] <command> [options]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  provision   Provision discovery and service tokens for a services.yaml configuration\n")
	fmt.Fprintf(os.Stderr, "  start       Start invariant services from configuration\n")
	fmt.Fprintf(os.Stderr, "  slot        Allocate a new slot from the slots service\n")
	fmt.Fprintf(os.Stderr, "  name        Register a name to a 32-byte hex value\n")
	fmt.Fprintf(os.Stderr, "  lookup      Lookup a name and print the resolved address\n")
	fmt.Fprintf(os.Stderr, "  mount       Mount the invariant file system using FUSE\n")
	fmt.Fprintf(os.Stderr, "  cache       Ensure all blocks for a mount are cached locally\n")
	fmt.Fprintf(os.Stderr, "  nfs         Start the invariant file system as an NFS Server\n")
	fmt.Fprintf(os.Stderr, "  upload      Upload a local directory as a file tree\n")
	fmt.Fprintf(os.Stderr, "  print       Print a block's contents to standard output\n")
	fmt.Fprintf(os.Stderr, "  systemd     Manage invariant services using systemd\n")
	fmt.Fprintf(os.Stderr, "  status      Query the discovery service and verify node health directly\n")
	fmt.Fprintf(os.Stderr, "  workspace   Manage layered workspaces\n")
	fmt.Fprintf(os.Stderr, "  scan-repo   Scan a Git repository and index mappings to KV service\n")
	fmt.Fprintf(os.Stderr, "  repository  Manage Invariant version control repositories\n")
	fmt.Fprintf(os.Stderr, "  login       Authenticate user with OAuth provider\n")
	fmt.Fprintf(os.Stderr, "  invite      Manage and redeem single-use control plane invitations\n")
	fmt.Fprintf(os.Stderr, "  token       Manage control plane access tokens\n")
	fmt.Fprintf(os.Stderr, "  service-key Manage service keys authorized by user account\n")
	os.Exit(1)
}

func main() {
	args, logFlag, logSet := extractLogFlag(os.Args[1:])
	if len(args) < 1 {
		usage()
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading configuration: %v\n", err)
		os.Exit(1)
	}
	if logSet {
		cfg.Log = logFlag
	}
	if !cfg.Log {
		log.SetOutput(io.Discard)
	}

	switch args[0] {
	case "provision":
		runProvision(cfg, args[1:])
	case "start":
		runStart(cfg, args[1:])
	case "slot":
		runSlot(cfg, args[1:])
	case "name":
		runName(cfg, args[1:])
	case "lookup":
		runLookup(cfg, args[1:])
	case "mount":
		runMount(cfg, args[1:])
	case "cache":
		runCache(cfg, args[1:])
	case "nfs":
		runNfs(cfg, args[1:])
	case "upload":
		runUpload(cfg, args[1:])
	case "print":
		runPrint(cfg, args[1:])
	case "systemd":
		runSystemd(cfg, args[1:])
	case "status":
		runStatus(cfg, args[1:])
	case "workspace":
		runWorkspace(cfg, args[1:])
	case "scan-repo":
		runScanRepo(cfg, args[1:])
	case "repository":
		runRepository(cfg, args[1:])
	case "login":
		runLogin(cfg, args[1:])
	case "invite":
		runInvite(cfg, args[1:])
	case "token":
		runToken(cfg, args[1:])
	case "service-key":
		runServiceKey(cfg, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown command %q\n", args[0])
		usage()
	}
}
