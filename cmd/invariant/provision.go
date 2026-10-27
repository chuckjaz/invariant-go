package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chuckjaz/invariant-go/internal/config"
	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/itp"
	"github.com/chuckjaz/invariant-go/internal/start"
)

var itpAddrRegex = regexp.MustCompile(`itp://[a-zA-Z0-9]+`)

func runProvision(globalCfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("provision", flag.ExitOnError)
	var configPath string
	fs.StringVar(&configPath, "config", "services.yaml", "Path to the YAML configuration file")
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL override")
	var binDir string
	fs.StringVar(&binDir, "bin-dir", "", "Directory containing service binaries (defaults to directory of invariant executable)")
	var installMissing bool
	fs.BoolVar(&installMissing, "install", false, "Install missing service commands with 'go install' if 'go' is on PATH")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: invariant provision [options]\n\n")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	svcCfg, err := start.LoadConfig(configPath, "")
	if err != nil {
		log.Fatalf("Failed to load configuration %s: %v", configPath, err)
	}

	if binDir == "" {
		exePath, err := os.Executable()
		if err != nil {
			log.Fatalf("Failed to get executable path: %v", err)
		}
		binDir = filepath.Dir(exePath)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := provisionServices(ctx, globalCfg, svcCfg, binDir, discoveryURL, installMissing); err != nil {
		log.Fatalf("Provisioning failed: %v", err)
	}
	log.Println("All services in configuration have valid service tokens.")
}

func provisionServices(ctx context.Context, globalCfg *config.InvariantConfig, svcCfg *start.Config, binDir, discoveryURLOverride string, installMissing bool) error {
	var discoverySvc *start.ServiceConfig
	for i := range svcCfg.Services {
		if filepath.Base(svcCfg.Services[i].Command) == "inv-discovery" {
			discoverySvc = &svcCfg.Services[i]
			break
		}
	}

	discCtx, stopDiscovery := context.WithCancel(ctx)
	defer stopDiscovery()

	var detectedDiscURL string
	var discCmd *exec.Cmd
	if discoverySvc != nil {
		var err error
		discCmd, detectedDiscURL, err = startDiscoveryForProvision(discCtx, *discoverySvc, binDir, installMissing)
		if err != nil {
			return fmt.Errorf("failed to start inv-discovery service: %w", err)
		}
		defer func() {
			stopDiscovery()
			if discCmd != nil && discCmd.Process != nil {
				_ = discCmd.Wait()
			}
		}()
	}

	discoveryURL := discoveryURLOverride
	if discoveryURL == "" && detectedDiscURL != "" {
		discoveryURL = detectedDiscURL
	}
	if discoveryURL == "" && discoverySvc != nil {
		if d := discoverySvc.Args["dir"]; d != "" {
			if data, err := os.ReadFile(filepath.Join(d, "bootstrap.invite")); err == nil {
				if _, cpURL := discovery.ParseInvitationCode(string(data)); cpURL != "" {
					discoveryURL = cpURL
				}
			}
		}
	}
	if discoveryURL == "" {
		for _, sc := range svcCfg.Services {
			if filepath.Base(sc.Command) != "inv-discovery" && sc.Args["discovery"] != "" {
				discoveryURL = sc.Args["discovery"]
				break
			}
		}
	}
	if discoveryURL == "" && globalCfg != nil && globalCfg.Discovery != "" {
		discoveryURL = globalCfg.Discovery
	}
	if discoveryURL == "" {
		discoveryURL = "http://127.0.0.1:3003"
	}

	pk := getClientKey()
	derpMapURL := ""
	if globalCfg != nil {
		derpMapURL = globalCfg.DERPMapURL
	}
	probeHTTP := itp.NewHTTPClient(pk.Private, "", itp.WithDERPMapURL(derpMapURL))
	if err := waitForDiscoveryReady(ctx, discoveryURL, probeHTTP); err != nil {
		return fmt.Errorf("discovery service at %s did not become ready: %w", discoveryURL, err)
	}

	adminToken, err := ensureProvisionAdminToken(ctx, globalCfg, discoverySvc, discoveryURL, probeHTTP)
	if err != nil {
		return fmt.Errorf("failed to obtain admin token for discovery: %w", err)
	}

	adminHTTP := itp.NewHTTPClient(pk.Private, adminToken, itp.WithDERPMapURL(derpMapURL))
	adminClient := discovery.NewClient(discoveryURL, adminHTTP).WithAccessToken(adminToken)

	for _, sc := range svcCfg.Services {
		svcName := filepath.Base(sc.Command)
		if svcName == "inv-discovery" {
			continue
		}

		tokenPath, existingTok := resolveServiceTokenForConfig(sc)
		if existingTok != "" {
			if ok, _, err := adminClient.VerifyAccessToken(ctx, existingTok); err == nil && ok {
				log.Printf("Service [%s] already has a valid service token (%s)", svcName, tokenPath)
				continue
			}
		}

		inv, err := adminClient.CreateInvitation(ctx, svcName, discovery.RoleService, discovery.NodeIdentity{}, discovery.DefaultInvitationTTL)
		if err != nil {
			return fmt.Errorf("failed to create service invitation for [%s]: %w", svcName, err)
		}
		invCode := discovery.FormatInvitationCode(inv.Code, discoveryURL)
		log.Printf("Created single-use service invitation %s for [%s]", inv.ID, svcName)

		if err := runServiceWithProvisionFlag(ctx, sc, binDir, invCode, installMissing); err != nil {
			return fmt.Errorf("failed to provision service [%s]: %w", svcName, err)
		}

		tokenPath, newTok := resolveServiceTokenForConfig(sc)
		if newTok == "" {
			return fmt.Errorf("service [%s] exited cleanly with --provision, but no token was found at %s", svcName, tokenPath)
		}
		if ok, _, err := adminClient.VerifyAccessToken(ctx, newTok); err != nil || !ok {
			return fmt.Errorf("service [%s] token at %s failed verification against discovery: %v", svcName, tokenPath, err)
		}
		log.Printf("Validated provisioned service token for [%s] at %s", svcName, tokenPath)
	}

	return nil
}

func resolveOrInstallCommand(ctx context.Context, command, binDir string, install bool) (string, error) {
	cmdName := filepath.Base(strings.TrimSpace(command))
	if p := findExecutable(cmdName, binDir); p != "" {
		return p, nil
	}

	if !install {
		return "", fmt.Errorf("command %q not found in %s or PATH (use --install to install via 'go install')", cmdName, binDir)
	}

	goBin, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("command %q not found and 'go' executable not found in PATH: %w", cmdName, err)
	}

	pkg := fmt.Sprintf("github.com/chuckjaz/invariant-go/cmd/%s@latest", cmdName)
	log.Printf("Command %q not found; installing latest version with: %s install %s", cmdName, goBin, pkg)
	installCmd := exec.CommandContext(ctx, goBin, "install", pkg)
	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	if err := installCmd.Run(); err != nil {
		return "", fmt.Errorf("failed to install %s via 'go install': %w", pkg, err)
	}

	if p := findExecutable(cmdName, binDir); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("installed %s, but binary %q could not be located in PATH or GOPATH/bin", pkg, cmdName)
}

func findExecutable(cmdName, binDir string) string {
	if binDir != "" {
		p := filepath.Join(binDir, cmdName)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath(cmdName); err == nil {
		return p
	}
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		p := filepath.Join(gobin, cmdName)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		p := filepath.Join(home, "go", "bin", cmdName)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

func startDiscoveryForProvision(ctx context.Context, sc start.ServiceConfig, binDir string, installMissing bool) (*exec.Cmd, string, error) {
	cmdPath, err := resolveOrInstallCommand(ctx, sc.Command, binDir, installMissing)
	if err != nil {
		return nil, "", err
	}

	args := make([]string, 0, len(sc.Args)*2+1)
	for k, v := range sc.Args {
		args = append(args, fmt.Sprintf("--%s=%s", k, v))
	}
	if _, hasLog := sc.Args["log"]; !hasLog {
		args = append(args, "--log")
	}

	cmd := exec.CommandContext(ctx, cmdPath, args...)
	cmd.Dir = binDir
	cmd.Env = os.Environ()
	for k, v := range sc.Environment {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	pr, pw := io.Pipe()
	cmd.Stdout = io.MultiWriter(log.Writer(), pw)
	cmd.Stderr = io.MultiWriter(log.Writer(), pw)

	urlCh := make(chan string, 1)
	var once sync.Once
	go func() {
		scanner := bufio.NewScanner(pr)
		for scanner.Scan() {
			line := scanner.Text()
			if m := itpAddrRegex.FindString(line); m != "" {
				once.Do(func() {
					urlCh <- m
				})
			} else if strings.Contains(line, "Discovery service listening on :") {
				port := sc.Args["port"]
				if port == "" {
					port = "3003"
				}
				once.Do(func() {
					urlCh <- fmt.Sprintf("http://127.0.0.1:%s", port)
				})
			}
		}
	}()

	log.Printf("Starting discovery service for provisioning: %s %v", cmdPath, args)
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return nil, "", err
	}

	var detectedURL string
	if sc.Args["listen-ip"] == "true" {
		port := sc.Args["port"]
		if port == "" {
			port = "3003"
		}
		detectedURL = fmt.Sprintf("http://127.0.0.1:%s", port)
	}

	select {
	case u := <-urlCh:
		detectedURL = u
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return cmd, "", ctx.Err()
	}

	return cmd, detectedURL, nil
}

func waitForDiscoveryReady(ctx context.Context, discoveryURL string, httpClient *http.Client) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(discoveryURL, "/")+"/id", nil)
		if err == nil {
			resp, err := httpClient.Do(req)
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return fmt.Errorf("timed out waiting for %s/id", discoveryURL)
}

func ensureProvisionAdminToken(ctx context.Context, globalCfg *config.InvariantConfig, discoverySvc *start.ServiceConfig, discoveryURL string, unauthHTTP *http.Client) (string, error) {
	pk := getClientKey()
	derpMapURL := ""
	if globalCfg != nil {
		derpMapURL = globalCfg.DERPMapURL
	}

	if existing := resolveUserToken(globalCfg); existing != "" {
		authHTTP := itp.NewHTTPClient(pk.Private, existing, itp.WithDERPMapURL(derpMapURL))
		c := discovery.NewClient(discoveryURL, authHTTP).WithAccessToken(existing)
		if ok, _, err := c.VerifyAccessToken(ctx, existing); err == nil && ok {
			return existing, nil
		}
	}

	var candidateFiles []string
	if discoverySvc != nil {
		if f := discoverySvc.Args["bootstrap-invite-file"]; f != "" {
			candidateFiles = append(candidateFiles, f)
		}
		if d := discoverySvc.Args["dir"]; d != "" {
			candidateFiles = append(candidateFiles, filepath.Join(d, "bootstrap.invite"))
		}
	}
	if keysDir, err := config.KeysDir(); err == nil {
		candidateFiles = append(candidateFiles, filepath.Join(keysDir, "bootstrap.invite"))
	}

	var bootstrapCode string
	for _, path := range candidateFiles {
		if data, err := os.ReadFile(path); err == nil {
			code := strings.TrimSpace(string(data))
			if code != "" {
				bootstrapCode = code
				break
			}
		}
	}
	if bootstrapCode == "" {
		return "", fmt.Errorf("no valid admin token or bootstrap.invite file found; run 'invariant login' and 'invariant invite redeem <code-or-file>' first")
	}

	userName := os.Getenv("USER")
	if userName == "" {
		userName = "admin"
	}

	unauthClient := discovery.NewClient(discoveryURL, unauthHTTP)
	rec, err := unauthClient.RedeemInvitation(ctx, bootstrapCode, pk.Private.Public().String(), discovery.NodeIdentity{Name: userName})
	if err != nil {
		return "", fmt.Errorf("failed to redeem bootstrap invitation: %w", err)
	}

	tokenPath := itp.DefaultTokenPath("client")
	if err := itp.SaveAccessToken(tokenPath, rec.Token); err != nil {
		return "", fmt.Errorf("failed to save admin access token to %s: %w", tokenPath, err)
	}
	_ = itp.SaveAccessToken(itp.DefaultDiscoveryURLPath("client"), discoveryURL)
	log.Printf("Redeemed bootstrap admin invitation and saved admin token to %s", tokenPath)
	return rec.Token, nil
}

func resolveServiceTokenForConfig(sc start.ServiceConfig) (string, string) {
	svcName := filepath.Base(sc.Command)
	dir := sc.Args["dir"]
	if dir == "" && svcName == "inv-kv" {
		dir = "kv-journal"
	}

	serviceID := sc.Args["id"]
	if serviceID == "" && dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, "id")); err == nil && len(strings.TrimSpace(string(data))) == 64 {
			serviceID = strings.TrimSpace(string(data))
		}
	}
	if serviceID == "" {
		serviceID = svcName
	}

	tokenPath := itp.ServiceTokenPath(dir, serviceID, sc.Args["token-file"])
	if tok, err := itp.LoadAccessToken(tokenPath); err == nil && tok != "" {
		return tokenPath, tok
	}
	defPath := itp.DefaultTokenPath(serviceID)
	if tok, err := itp.LoadAccessToken(defPath); err == nil && tok != "" {
		return defPath, tok
	}
	return tokenPath, ""
}

func runServiceWithProvisionFlag(ctx context.Context, sc start.ServiceConfig, binDir, inviteCode string, installMissing bool) error {
	cmdPath, err := resolveOrInstallCommand(ctx, sc.Command, binDir, installMissing)
	if err != nil {
		return err
	}

	args := make([]string, 0, len(sc.Args)*2+2)
	for k, v := range sc.Args {
		if k == "provision" {
			continue
		}
		args = append(args, fmt.Sprintf("--%s=%s", k, v))
	}
	args = append(args, fmt.Sprintf("--provision=%s", inviteCode))

	cmd := exec.CommandContext(ctx, cmdPath, args...)
	cmd.Dir = binDir
	cmd.Env = os.Environ()
	for k, v := range sc.Environment {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("Provisioning service [%s]: %s %v", sc.Command, cmdPath, args)
	return cmd.Run()
}
