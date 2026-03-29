package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"invariant/internal/config"
	"invariant/internal/start"
)

const serviceTemplate = `[Unit]
Description=Invariant {{.Name}} Service
After=network.target
{{if .Requires}}Requires={{.Requires}}{{end}}
{{if .Wants}}Wants={{.Wants}}{{end}}

[Service]
Type=simple
User={{.User}}
Group={{.Group}}
ExecStart={{.ExecStart}}{{range .Args}} {{.}}{{end}}{{range $k, $v := .Env}}
Environment="{{$k}}={{$v}}"{{end}}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`

type serviceData struct {
	Name      string
	Requires  string
	Wants     string
	User      string
	Group     string
	ExecStart string
	Args      []string
	Env       map[string]string
}

func runSystemd(globalCfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		log.Fatalf("systemd requires a subcommand: install, update, uninstall, log, start, stop, files")
	}

	subCmd := args[0]
	fs := flag.NewFlagSet("systemd "+subCmd, flag.ExitOnError)
	var configPath string
	fs.StringVar(&configPath, "config", "services.yaml", "Path to the YAML configuration file")
	var keysDir string
	fs.StringVar(&keysDir, "keys-dir", "", "Override the directory containing environment keys (useful when running via sudo)")

	var runUser, runGroup string
	if subCmd == "install" || subCmd == "update" || subCmd == "uninstall" || subCmd == "files" {
		fs.StringVar(&runUser, "user", "invariant", "User to run services as")
		fs.StringVar(&runGroup, "group", "invariant", "Group to run services as")
	}

	var outDir string
	if subCmd == "files" {
		fs.StringVar(&outDir, "out", ".", "Directory to output service files")
	}

	var startServices bool
	if subCmd == "install" || subCmd == "update" {
		fs.BoolVar(&startServices, "start", false, "Start services after install/update")
	}

	fs.Parse(args[1:])

	switch subCmd {
	case "install":
		installServices(configPath, keysDir, runUser, runGroup, startServices)
	case "update":
		installServices(configPath, keysDir, runUser, runGroup, startServices) // update and install logic are identical for file generation
	case "uninstall":
		uninstallServices(configPath, keysDir, runUser, runGroup)
	case "log":
		logServices(configPath, keysDir, fs.Args())
	case "start", "stop":
		controlServices(subCmd, configPath, keysDir, fs.Args())
	case "files":
		generateFiles(configPath, keysDir, runUser, runGroup, outDir)
	default:
		log.Fatalf("Unknown systemd subcommand %q", subCmd)
	}
}

func installServices(configPath, keysDir, runUser, runGroup string, startServices bool) {
	// Create user/group if needed
	if err := ensureUserGroup(runUser, runGroup); err != nil {
		log.Fatalf("Failed to ensure user/group: %v", err)
	}

	u, err := user.Lookup(runUser)
	if err != nil {
		log.Fatalf("Failed to lookup user %s: %v", runUser, err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	binDir := filepath.Join(u.HomeDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		log.Fatalf("Failed to create bin dir: %v", err)
	}
	os.Chown(binDir, uid, gid)

	cfg, err := start.LoadConfig(configPath, keysDir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	exePath, err := os.Executable()
	if err != nil {
		log.Fatalf("Failed to get executable path: %v", err)
	}
	cmdBaseDir := filepath.Dir(exePath)

	tmpl, err := template.New("service").Parse(serviceTemplate)
	if err != nil {
		log.Fatalf("Failed to parse template: %v", err)
	}

	counts := make(map[string]int)
	var generatedServices []string

	for _, sc := range cfg.Services {
		name := extractServiceName(sc, counts)
		serviceUnitName := fmt.Sprintf("invariant-%s.service", name)

		srcBin := filepath.Join(cmdBaseDir, filepath.Base(sc.Command))
		dstBin := filepath.Join(binDir, filepath.Base(sc.Command))

		if err := copyBinary(srcBin, dstBin, uid, gid); err != nil {
			log.Fatalf("Failed to copy binary %s -> %s: %v", srcBin, dstBin, err)
		}

		req, wants := buildDependencies(sc)

		var cmdArgs []string
		for k, v := range sc.Args {
			cmdArgs = append(cmdArgs, fmt.Sprintf("--%s=%s", k, v))
		}

		data := serviceData{
			Name:      name,
			Requires:  req,
			Wants:     wants,
			User:      runUser,
			Group:     runGroup,
			ExecStart: dstBin,
			Args:      cmdArgs,
			Env:       sc.Environment,
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			log.Fatalf("Failed to template service %s: %v", name, err)
		}

		unitPath := filepath.Join("/etc/systemd/system", serviceUnitName)
		log.Printf("Writing systemd unit: %s", unitPath)
		if err := os.WriteFile(unitPath, buf.Bytes(), 0644); err != nil {
			log.Fatalf("Failed to write unit file %s: %v. Are you running as root?", unitPath, err)
		}

		generatedServices = append(generatedServices, serviceUnitName)
	}

	log.Println("Reloading systemd daemon...")
	systemctl("daemon-reload")

	if startServices {
		for _, s := range generatedServices {
			systemctl("enable", "--now", s)
		}
	}
}

func uninstallServices(configPath, keysDir, runUser, runGroup string) {
	cfg, err := start.LoadConfig(configPath, keysDir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	counts := make(map[string]int)
	for _, sc := range cfg.Services {
		name := extractServiceName(sc, counts)
		serviceUnitName := fmt.Sprintf("invariant-%s.service", name)

		log.Printf("Stopping and disabling %s", serviceUnitName)
		systemctl("stop", serviceUnitName)
		systemctl("disable", serviceUnitName)

		unitPath := filepath.Join("/etc/systemd/system", serviceUnitName)
		if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
			log.Printf("Warning: failed to remove %s: %v", unitPath, err)
		}
	}

	systemctl("daemon-reload")

	if runUser == "invariant" && runGroup == "invariant" {
		log.Println("Removing user and group 'invariant'")
		exec.Command("userdel", "-r", "invariant").Run()
		exec.Command("groupdel", "invariant").Run()
	}
}

func logServices(configPath, keysDir string, args []string) {
	cfg, err := start.LoadConfig(configPath, keysDir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	var targetName string
	if len(args) > 0 {
		targetName = args[0]
	}

	counts := make(map[string]int)
	for _, sc := range cfg.Services {
		name := extractServiceName(sc, counts)
		if targetName == "" || name == targetName || sc.Command == targetName {
			serviceUnitName := fmt.Sprintf("invariant-%s.service", name)
			cmd := exec.Command("journalctl", "-u", serviceUnitName, "-f")
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Run()
			return
		}
	}
	log.Fatalf("Service not found in configuration.")
}

func controlServices(action string, configPath string, keysDir string, args []string) {
	cfg, err := start.LoadConfig(configPath, keysDir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	var targetName string
	if len(args) > 0 {
		targetName = args[0]
	}

	counts := make(map[string]int)
	var units []string
	for _, sc := range cfg.Services {
		name := extractServiceName(sc, counts)
		if targetName == "" || name == targetName || sc.Command == targetName {
			units = append(units, fmt.Sprintf("invariant-%s.service", name))
		}
	}

	if len(units) == 0 {
		log.Fatalf("No matching services found to %s.", action)
	}

	log.Printf("%s-ing %d services...", action, len(units))
	systemctl(append([]string{action}, units...)...)
}

func generateFiles(configPath, keysDir, runUser, runGroup, outDir string) {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		log.Fatalf("Failed to create out dir: %v", err)
	}

	cfg, err := start.LoadConfig(configPath, keysDir)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	tmpl, err := template.New("service").Parse(serviceTemplate)
	if err != nil {
		log.Fatalf("Failed to parse template: %v", err)
	}

	binDir := fmt.Sprintf("/home/%s/bin", runUser)
	if runUser == "root" {
		binDir = "/root/bin"
	}
	if u, err := user.Lookup(runUser); err == nil {
		binDir = filepath.Join(u.HomeDir, "bin")
	}

	counts := make(map[string]int)

	for _, sc := range cfg.Services {
		name := extractServiceName(sc, counts)
		serviceUnitName := fmt.Sprintf("invariant-%s.service", name)

		dstBin := filepath.Join(binDir, filepath.Base(sc.Command))

		req, wants := buildDependencies(sc)

		var cmdArgs []string
		for k, v := range sc.Args {
			cmdArgs = append(cmdArgs, fmt.Sprintf("--%s=%s", k, v))
		}

		data := serviceData{
			Name:      name,
			Requires:  req,
			Wants:     wants,
			User:      runUser,
			Group:     runGroup,
			ExecStart: dstBin,
			Args:      cmdArgs,
			Env:       sc.Environment,
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			log.Fatalf("Failed to template service %s: %v", name, err)
		}

		unitPath := filepath.Join(outDir, serviceUnitName)
		log.Printf("Writing systemd unit: %s", unitPath)
		if err := os.WriteFile(unitPath, buf.Bytes(), 0644); err != nil {
			log.Fatalf("Failed to write unit file %s: %v", unitPath, err)
		}
	}
}

func extractServiceName(sc start.ServiceConfig, counts map[string]int) string {
	name := sc.Args["name"]
	if name == "" {
		dir := sc.Args["dir"]
		if dir != "" {
			name = filepath.Base(dir)
		}
	}
	if name == "" || name == "." || name == "/" {
		name = filepath.Base(sc.Command)
		counts[name]++
		if counts[name] > 1 {
			name = fmt.Sprintf("%s-%d", name, counts[name])
		}
	}
	name = strings.ReplaceAll(name, "*", "any")
	name = strings.ReplaceAll(name, "/", "-")
	return name
}

func buildDependencies(sc start.ServiceConfig) (string, string) {
	var r []string
	var w []string

	if _, ok := sc.Args["discovery"]; ok {
		r = append(r, "invariant-discovery.service")
	}

	if _, ok := sc.Args["name"]; ok && sc.Command != "names" {
		w = append(w, "invariant-names.service")
	}

	if val, ok := sc.Args["distribute"]; ok {
		for v := range strings.SplitSeq(val, ",") {
			w = append(w, fmt.Sprintf("invariant-%s.service", strings.TrimSpace(v)))
		}
	}

	if val, ok := sc.Args["notify"]; ok {
		for v := range strings.SplitSeq(val, ",") {
			w = append(w, fmt.Sprintf("invariant-%s.service", strings.TrimSpace(v)))
		}
	}

	return strings.Join(r, " "), strings.Join(w, " ")
}

func ensureUserGroup(username, groupname string) error {
	_, err := user.LookupGroup(groupname)
	if err != nil {
		log.Printf("Creating group %s...", groupname)
		cmd := exec.Command("groupadd", groupname)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create group: %s", out)
		}
	}

	_, err = user.Lookup(username)
	if err != nil {
		log.Printf("Creating user %s...", username)
		cmd := exec.Command("useradd", "-m", "-r", "-g", groupname, username)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create user: %s", out)
		}
	}
	return nil
}

func copyBinary(src, dst string, uid, gid int) error {
	in, err := os.Open(src)
	if err != nil {
		// Log warning or error but continue if source does not exist?
		// "The commands that would be used by start should be copied"
		// If invariant is missing a binary it needs, it will fail during start.
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("create dest: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	if err := os.Chmod(dst, 0755); err != nil {
		return err
	}
	if err := os.Chown(dst, uid, gid); err != nil {
		return err
	}

	return nil
}

func systemctl(args ...string) error {
	fmt.Printf("systemctl %s\n", strings.Join(args, " "))
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
