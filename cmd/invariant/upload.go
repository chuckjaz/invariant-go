package main

import (
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
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
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
	fsFlags.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")

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

	rootEntry, err := processDirectory(absPath, absPath, storageClient, rules)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Upload failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%s\n", rootEntry.Content.Address)
}

func processDirectory(rootPath, currentPath string, store storage.Storage, rules filetree.IgnoreRules) (*filetree.DirectoryEntry, error) {
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
			subDirEntry, err := processDirectory(rootPath, filepath.Join(currentPath, d.Name()), store, rules)
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
			fileEntry, err := processFile(filepath.Join(currentPath, d.Name()), d.Name(), store)
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

	link, err := content.Write(strings.NewReader(string(data)), store, content.WriterOptions{})
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

func processFile(filePath, name string, store storage.Storage) (*filetree.FileEntry, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	link, err := content.Write(file, store, content.WriterOptions{})
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
