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
	args := os.Args[1:]
	isTest := false
	if len(args) > 0 && args[0] == "-test" {
		isTest = true
		args = args[1:]
	}
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s [-test] [flags...] <go-target> <output-file>\n", os.Args[0])
		os.Exit(1)
	}

	outputFile := args[len(args)-1]
	targetArgs := args[:len(args)-1]

	// 1. Run 'go list' with dependencies
	goArgs := []string{"list", "-json"}
	if isTest {
		goArgs = append(goArgs, "-test")
	}
	goArgs = append(goArgs, "-deps")
	goArgs = append(goArgs, targetArgs...)

	cmd := exec.Command("go", goArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		if stderr.Len() > 0 {
			fmt.Fprintf(os.Stderr, "Error running go list: %v\n%s", err, stderr.String())
		} else {
			fmt.Fprintf(os.Stderr, "Error running go list: %v\n", err)
		}
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
	if isTest {
		targetName = strings.TrimSuffix(outputFile, filepath.Ext(outputFile)) + ".passed"
	} else if mainPkg.Name == "main" {
		targetName = "bin/" + filepath.Base(mainPkg.ImportPath)
	} else {
		targetName = strings.TrimSuffix(filepath.Base(outputFile), filepath.Ext(outputFile))
	}

	// Collect and resolve paths for all local dependency source files
	depMap := make(map[string]bool)
	for _, pkg := range pkgs {
		// Only include local packages that belong to the main module
		if pkg.Standard || pkg.Module == nil || !pkg.Module.Main {
			continue
		}

		for _, f := range pkg.GoFiles {
			absFile := f
			if !filepath.IsAbs(absFile) {
				absFile = filepath.Join(pkg.Dir, f)
			}
			relFile, err := filepath.Rel(pkg.Module.Dir, absFile)
			if err != nil {
				continue
			}
			if strings.HasPrefix(relFile, "..") {
				continue
			}
			depMap[relFile] = true
		}
	}

	// Extract unique dependencies from the map and sort them for deterministic output
	var deps []string
	for dep := range depMap {
		deps = append(deps, dep)
	}
	sort.Strings(deps)

	// 2. Generate the Ninja dyndep file
	if err := os.MkdirAll(filepath.Dir(outputFile), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating output directory: %v\n", err)
		os.Exit(1)
	}

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
}
