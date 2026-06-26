package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type Module struct {
	Path string `json:"Path"`
	Main bool   `json:"Main"`
	Dir  string `json:"Dir"`
}

type Package struct {
	Dir        string   `json:"Dir"`
	ImportPath string   `json:"ImportPath"`
	Name       string   `json:"Name"`
	GoFiles    []string `json:"GoFiles"`
	Imports    []string `json:"Imports"`
	Module     *Module  `json:"Module"`
	Standard   bool     `json:"Standard"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <go-target> <output-file>\n", os.Args[0])
		os.Exit(1)
	}
	targetPkg := os.Args[1]
	outputFile := os.Args[2]

	// 1. Run 'go list' with dependencies
	cmd := exec.Command("go", "list", "-json", "-deps", targetPkg)
	stdout, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running go list: %v\n", err)
		os.Exit(1)
	}

	// Because 'go list -json' outputs a concatenated list of JSON objects,
	// we insert commas between them and wrap the output in a JSON array for proper unmarshaling.
	stdout = bytes.ReplaceAll(stdout, []byte("}\n{"), []byte("},\n{"))
	stdout = bytes.ReplaceAll(stdout, []byte("}\r\n{"), []byte("},\r\n{"))
	jsonArray := append([]byte("["), stdout...)
	jsonArray = append(jsonArray, ']')

	var pkgs []Package
	if err := json.Unmarshal(jsonArray, &pkgs); err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding JSON: %v\n", err)
		os.Exit(1)
	}

	// Find the main target package (or fallback to the last package)
	var mainPkg *Package
	for i := range pkgs {
		if pkgs[i].Name == "main" {
			mainPkg = &pkgs[i]
			break
		}
	}
	if mainPkg == nil && len(pkgs) > 0 {
		mainPkg = &pkgs[len(pkgs)-1]
	}

	if mainPkg == nil {
		fmt.Fprintln(os.Stderr, "No packages found.")
		os.Exit(1)
	}

	// Determine the target name in the dyndep file.
	// Typically, go binaries built with ninja are target path bin/<import-path-base>.
	// If it is a main package, use bin/<base-name>. Otherwise, use target name from outputFile.
	var targetName string
	if mainPkg.Name == "main" {
		targetName = "bin/" + filepath.Base(mainPkg.ImportPath)
	} else {
		targetName = strings.TrimSuffix(filepath.Base(outputFile), filepath.Ext(outputFile))
	}

	// Collect and resolve paths for all local dependency source files
	var deps []string
	for _, pkg := range pkgs {
		// Only include local packages that belong to the main module
		if pkg.Standard || pkg.Module == nil || !pkg.Module.Main {
			continue
		}

		relDir, err := filepath.Rel(pkg.Module.Dir, pkg.Dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving path for package %s: %v\n", pkg.ImportPath, err)
			os.Exit(1)
		}

		for _, f := range pkg.GoFiles {
			deps = append(deps, filepath.Join(relDir, f))
		}
	}

	// Sort the dependencies for deterministic output
	sort.Strings(deps)

	// 2. Generate the Ninja dyndep file
	file, err := os.Create(outputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating dyndep file: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	// 3. Write dyndep syntax
	// ninja_dyndep_version specifies version 1.0
	fmt.Fprintln(file, "ninja_dyndep_version = 1.0")
	fmt.Fprintf(file, "\nbuild %s: dyndep |", targetName)
	for _, dep := range deps {
		fmt.Fprintf(file, " %s", dep)
	}
	fmt.Fprintln(file)

	fmt.Printf("Successfully generated %s for target %s\n", outputFile, targetName)
}
