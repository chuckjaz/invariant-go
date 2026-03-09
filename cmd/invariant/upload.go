package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"invariant/internal/config"
	"invariant/internal/content"
	"invariant/internal/discovery"
	"invariant/internal/filetree"
	"invariant/internal/finder"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

func parseIgnoreFile(path string) (filetree.IgnoreRules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var rules []string
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules = append(rules, line)
	}
	return rules, nil
}

func runUpload(globalCfg *config.InvariantConfig, args []string) {
	fsFlags := flag.NewFlagSet("upload", flag.ExitOnError)
	var discoveryURL string
	var slotID string
	var prevFlag string
	fsFlags.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	fsFlags.StringVar(&slotID, "slot", "", "Optional 32-byte hex Slot ID to update after successful upload")
	fsFlags.StringVar(&prevFlag, "prev", "", "Optional 32-byte hex previous Slot address (required if not locally cached)")
	var compress bool
	var encrypt bool
	var keyPolicyStr string
	var keyStr string
	fsFlags.BoolVar(&compress, "compress", false, "Compress the uploaded content")
	fsFlags.BoolVar(&encrypt, "encrypt", false, "Encrypt the uploaded content")
	fsFlags.StringVar(&keyPolicyStr, "key-policy", "RandomPerBlock", "Encryption key policy (RandomPerBlock, RandomAllKey, Deterministic, SuppliedAllKey)")
	fsFlags.StringVar(&keyStr, "key", "", "32-byte hex-encoded key (required if key-policy is SuppliedAllKey)")

	fsFlags.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant upload [options] [directory]\n\n")
		fsFlags.PrintDefaults()
	}
	fsFlags.Parse(args)

	dirPath := "."
	if fsFlags.NArg() > 0 {
		dirPath = fsFlags.Arg(0)
	}

	if discoveryURL == "" && globalCfg != nil {
		discoveryURL = globalCfg.Discovery
	}
	if discoveryURL == "" {
		fmt.Fprintf(os.Stderr, "Discovery URL is required\n")
		os.Exit(1)
	}

	var dClient discovery.Discovery
	dClient = discovery.NewClient(discoveryURL, nil)

	findService := func(kind string) string {
		id, err := dClient.Find(kind, 1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not find %s service: %v\n", kind, err)
			os.Exit(1)
		}
		if len(id) == 0 {
			fmt.Fprintf(os.Stderr, "Could not find %s service\n", kind)
			os.Exit(1)
		}
		return id[0].Address
	}

	finderAddr := findService("finder-v1")
	finderClient := finder.NewClient(finderAddr, nil)
	storageClient := storage.NewAggregateClient(finderClient, dClient, 3, 1000)

	var previousAddress string
	var privKeyHex []byte

	if slotID != "" {
		if len(slotID) != 64 {
			fmt.Fprintf(os.Stderr, "Error: --slot must be a 64-character (32-byte) hex string\n")
			os.Exit(1)
		}

		globalDir, err := config.ConfigDir()
		if err == nil {
			prevPath := filepath.Join(globalDir, "slots", fmt.Sprintf("%s.prev", slotID))
			if data, err := os.ReadFile(prevPath); err == nil {
				previousAddress = strings.TrimSpace(string(data))
			}
		}

		if previousAddress == "" {
			if prevFlag != "" {
				previousAddress = prevFlag
			} else {
				fmt.Fprintf(os.Stderr, "Error: previous slot address not found locally. Please provide it via --prev\n")
				os.Exit(1)
			}
		}

		keysDir, err := config.KeysDir()
		if err == nil {
			keyPath := filepath.Join(keysDir, fmt.Sprintf("%s.key", slotID))
			if data, err := os.ReadFile(keyPath); err == nil {
				privKeyHex = data
			}
		}
	}

	absPath, err := filepath.Abs(dirPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid directory path: %v\n", err)
		os.Exit(1)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot stat directory: %v\n", err)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Target is not a directory: %v\n", absPath)
		os.Exit(1)
	}

	rules, err := parseIgnoreFile(filepath.Join(absPath, ".gitignore"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to read .gitignore: %v\n", err)
	}

	var opts content.WriterOptions
	if compress {
		opts.CompressAlgorithm = "gzip"
	}
	if encrypt {
		opts.EncryptAlgorithm = "aes-256-cbc"

		switch keyPolicyStr {
		case "RandomPerBlock":
			opts.KeyPolicy = content.RandomPerBlock
		case "RandomAllKey":
			opts.KeyPolicy = content.RandomAllKey
		case "Deterministic":
			opts.KeyPolicy = content.Deterministic
		case "SuppliedAllKey":
			opts.KeyPolicy = content.SuppliedAllKey
			if keyStr == "" {
				fmt.Fprintf(os.Stderr, "Error: --key is required when --key-policy is SuppliedAllKey\n")
				os.Exit(1)
			}

			importHex, err := hex.DecodeString(keyStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing --key: %v\n", err)
				os.Exit(1)
			}
			if len(importHex) != 32 {
				fmt.Fprintf(os.Stderr, "Error: --key must be a 32-byte hex-encoded string (got %d bytes)\n", len(importHex))
				os.Exit(1)
			}
			opts.SuppliedKey = importHex
		default:
			fmt.Fprintf(os.Stderr, "Error: unsupported key-policy '%s'\n", keyPolicyStr)
			os.Exit(1)
		}
	}

	rootEntry, err := processDirectory(absPath, absPath, storageClient, rules, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Upload failed: %v\n", err)
		os.Exit(1)
	}

	out, err := json.MarshalIndent(rootEntry.Content, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal output: %v\n", err)
		os.Exit(1)
	}

	if slotID != "" {
		slotsAddr := findService("slots-v1")
		slotsClient := slots.NewClient(slotsAddr, nil)

		err = slotsClient.Update(slotID, rootEntry.Content.Address, previousAddress, privKeyHex)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to update slot: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Updated slot %s from %s to %s\n", slotID, previousAddress, rootEntry.Content.Address)

		globalDir, err := config.ConfigDir()
		if err == nil {
			slotsDir := filepath.Join(globalDir, "slots")
			os.MkdirAll(slotsDir, 0755)

			prevPath := filepath.Join(slotsDir, fmt.Sprintf("%s.prev", slotID))
			err = os.WriteFile(prevPath, []byte(previousAddress), 0644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to write previous value to %s: %v\n", prevPath, err)
			}
		}
	}

	fmt.Printf("%s\n", out)
}

func processDirectory(rootPath, currentPath string, store storage.Storage, rules filetree.IgnoreRules, opts content.WriterOptions) (*filetree.DirectoryEntry, error) {
	entries, err := os.ReadDir(currentPath)
	if err != nil {
		return nil, err
	}

	var dir filetree.Directory

	for _, d := range entries {
		relPath, _ := filepath.Rel(rootPath, filepath.Join(currentPath, d.Name()))

		if rules.Matches(relPath, d.IsDir()) {
			continue
		}

		if d.IsDir() {
			subDirEntry, err := processDirectory(rootPath, filepath.Join(currentPath, d.Name()), store, rules, opts)
			if err != nil {
				return nil, err
			}
			if subDirEntry != nil {
				dir = append(dir, subDirEntry)
			}
		} else if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(filepath.Join(currentPath, d.Name()))
			if err != nil {
				return nil, err
			}
			symlinkEntry := &filetree.SymbolicLinkEntry{
				BaseEntry: filetree.BaseEntry{
					Kind: filetree.SymbolicLinkKind,
					Name: d.Name(),
				},
				Target: target,
			}
			dir = append(dir, symlinkEntry)
		} else if d.Type().IsRegular() {
			fileEntry, err := processFile(filepath.Join(currentPath, d.Name()), d.Name(), store, opts)
			if err != nil {
				return nil, err
			}
			dir = append(dir, fileEntry)
		}
	}

	data, err := json.Marshal(dir)
	if err != nil {
		return nil, err
	}

	link, err := content.Write(strings.NewReader(string(data)), store, opts)
	if err != nil {
		return nil, err
	}

	return &filetree.DirectoryEntry{
		BaseEntry: filetree.BaseEntry{
			Kind: filetree.DirectoryKind,
			Name: filepath.Base(currentPath),
		},
		Content: link,
		Size:    uint64(len(data)),
	}, nil
}

func processFile(filePath, name string, store storage.Storage, opts content.WriterOptions) (*filetree.FileEntry, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	link, err := content.Write(file, store, opts)
	if err != nil {
		return nil, err
	}

	modeStr := fmt.Sprintf("%04o", info.Mode().Perm())

	return &filetree.FileEntry{
		BaseEntry: filetree.BaseEntry{
			Kind: filetree.FileKind,
			Name: name,
			Mode: &modeStr,
		},
		Content: link,
		Size:    uint64(info.Size()),
	}, nil
}
