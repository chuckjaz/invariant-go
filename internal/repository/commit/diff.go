package commit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"invariant/internal/content"
	"invariant/internal/filetree"
	"invariant/internal/slots"
	"invariant/internal/storage"
)

// LineDiff computes unified diff hunks between two slices of text lines.
func LineDiff(oldLines, newLines []string, oldName, newName string) (string, int, int) {
	// Simple Myers diff algorithm for unified diff generation
	n := len(oldLines)
	m := len(newLines)

	// Quick check for identical content
	if n == m {
		identical := true
		for i := 0; i < n; i++ {
			if oldLines[i] != newLines[i] {
				identical = false
				break
			}
		}
		if identical {
			return "", 0, 0
		}
	}

	// Longest Common Subsequence (LCS) matrix
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}

	for i := 0; i < n; i++ {
		for j := 0; j < m; j++ {
			if oldLines[i] == newLines[j] {
				lcs[i+1][j+1] = lcs[i][j] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i+1][j+1] = lcs[i+1][j]
			} else {
				lcs[i+1][j+1] = lcs[i][j+1]
			}
		}
	}

	// Backtrack to build edit script
	type edit struct {
		op   byte // ' ', '+', '-'
		line string
	}

	var edits []edit
	i, j := n, m
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && oldLines[i-1] == newLines[j-1] {
			edits = append(edits, edit{op: ' ', line: oldLines[i-1]})
			i--
			j--
		} else if j > 0 && (i == 0 || lcs[i][j-1] >= lcs[i-1][j]) {
			edits = append(edits, edit{op: '+', line: newLines[j-1]})
			j--
		} else if i > 0 && (j == 0 || lcs[i][j-1] < lcs[i-1][j]) {
			edits = append(edits, edit{op: '-', line: oldLines[i-1]})
			i--
		}
	}

	// Reverse edits to get forward order
	for k := 0; k < len(edits)/2; k++ {
		opp := len(edits) - 1 - k
		edits[k], edits[opp] = edits[opp], edits[k]
	}

	insertions := 0
	deletions := 0
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("--- a/%s\n", oldName))
	sb.WriteString(fmt.Sprintf("+++ b/%s\n", newName))
	sb.WriteString(fmt.Sprintf("@@ -1,%d +1,%d @@\n", n, m))

	for _, e := range edits {
		if e.op == '+' {
			insertions++
			sb.WriteString("+" + e.line + "\n")
		} else if e.op == '-' {
			deletions++
			sb.WriteString("-" + e.line + "\n")
		} else {
			sb.WriteString(" " + e.line + "\n")
		}
	}

	return sb.String(), insertions, deletions
}

// FlatFileTreeEntry represents a flattened path in a commit tree.
type FlatFileTreeEntry struct {
	Path    string
	Kind    filetree.EntryKind
	Address string
	Size    uint64
}

// FlattenFileTree recursively walks a Directory from CAS and returns a map of path -> entry.
func FlattenFileTree(ctx context.Context, rootAddr string, store storage.Storage, slotsClient slots.Slots) (map[string]FlatFileTreeEntry, error) {
	result := make(map[string]FlatFileTreeEntry)
	if rootAddr == "" {
		return result, nil
	}

	var walk func(addr string, prefix string) error
	walk = func(addr string, prefix string) error {
		link := content.ContentLink{Address: addr}
		r, err := content.Read(link, store, slotsClient)
		if err != nil {
			return err
		}
		defer r.Close()

		data, err := io.ReadAll(r)
		if err != nil {
			return err
		}

		var dir filetree.Directory
		if err := dir.UnmarshalJSON(data); err != nil {
			return err
		}

		for _, entry := range dir {
			name := entry.GetName()
			path := name
			if prefix != "" {
				path = prefix + "/" + name
			}

			kind := entry.GetKind()
			if kind == filetree.DirectoryKind {
				dirEntry, ok := entry.(*filetree.DirectoryEntry)
				if ok && dirEntry.Content.Address != "" {
					if err := walk(dirEntry.Content.Address, path); err != nil {
						return err
					}
				}
			} else {
				var addr string
				var sz uint64
				if fileEntry, ok := entry.(*filetree.FileEntry); ok {
					addr = fileEntry.Content.Address
					sz = fileEntry.Size
				}
				result[path] = FlatFileTreeEntry{
					Path:    path,
					Kind:    kind,
					Address: addr,
					Size:    sz,
				}
			}
		}
		return nil
	}

	if err := walk(rootAddr, ""); err != nil {
		return nil, fmt.Errorf("failed to flatten file tree at %s: %w", rootAddr, err)
	}
	return result, nil
}

// ReadFileLines reads the string lines of a blob from CAS storage.
func ReadFileLines(ctx context.Context, addr string, store storage.Storage, slotsClient slots.Slots) ([]string, error) {
	if addr == "" {
		return []string{}, nil
	}
	link := content.ContentLink{Address: addr}
	r, err := content.Read(link, store, slotsClient)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines, nil
}

// CompareTrees calculates unified diffs and statistics between two directory root addresses.
func CompareTrees(ctx context.Context, fromAddr, toAddr string, store storage.Storage, slotsClient slots.Slots) (string, DiffStat, error) {
	fromEntries, err := FlattenFileTree(ctx, fromAddr, store, slotsClient)
	if err != nil {
		return "", DiffStat{}, err
	}
	toEntries, err := FlattenFileTree(ctx, toAddr, store, slotsClient)
	if err != nil {
		return "", DiffStat{}, err
	}

	pathsMap := make(map[string]bool)
	for p := range fromEntries {
		pathsMap[p] = true
	}
	for p := range toEntries {
		pathsMap[p] = true
	}

	var allPaths []string
	for p := range pathsMap {
		allPaths = append(allPaths, p)
	}
	// Sort paths alphabetically
	for i := 0; i < len(allPaths); i++ {
		for j := i + 1; j < len(allPaths); j++ {
			if allPaths[i] > allPaths[j] {
				allPaths[i], allPaths[j] = allPaths[j], allPaths[i]
			}
		}
	}

	var diffBuf bytes.Buffer
	stat := DiffStat{}

	for _, path := range allPaths {
		fromE, inFrom := fromEntries[path]
		toE, inTo := toEntries[path]

		if inFrom && inTo {
			if fromE.Address == toE.Address {
				continue
			}
			fromLines, err := ReadFileLines(ctx, fromE.Address, store, slotsClient)
			if err != nil {
				return "", DiffStat{}, err
			}
			toLines, err := ReadFileLines(ctx, toE.Address, store, slotsClient)
			if err != nil {
				return "", DiffStat{}, err
			}
			d, ins, del := LineDiff(fromLines, toLines, path, path)
			if d != "" {
				diffBuf.WriteString(d)
				stat.FilesChanged++
				stat.Insertions += ins
				stat.Deletions += del
				stat.Details = append(stat.Details, fmt.Sprintf("%s | %d (+%d -%d)", path, ins+del, ins, del))
			}
		} else if inFrom && !inTo {
			// Deleted file
			fromLines, err := ReadFileLines(ctx, fromE.Address, store, slotsClient)
			if err != nil {
				return "", DiffStat{}, err
			}
			d, ins, del := LineDiff(fromLines, []string{}, path, "/dev/null")
			diffBuf.WriteString(d)
			stat.FilesChanged++
			stat.Insertions += ins
			stat.Deletions += del
			stat.Details = append(stat.Details, fmt.Sprintf("%s | %d (-%d)", path, del, del))
		} else if !inFrom && inTo {
			// Added file
			toLines, err := ReadFileLines(ctx, toE.Address, store, slotsClient)
			if err != nil {
				return "", DiffStat{}, err
			}
			d, ins, del := LineDiff([]string{}, toLines, "/dev/null", path)
			diffBuf.WriteString(d)
			stat.FilesChanged++
			stat.Insertions += ins
			stat.Deletions += del
			stat.Details = append(stat.Details, fmt.Sprintf("%s | %d (+%d)", path, ins, ins))
		}
	}

	return diffBuf.String(), stat, nil
}
