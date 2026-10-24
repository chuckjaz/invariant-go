package start

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	invconfig "github.com/chuckjaz/invariant-go/internal/config"
	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/itp"
)

// RunnerConfig provides configuration for the service runner.
type RunnerConfig struct {
	MaxBackoffDuration time.Duration // time before giving up with exponential backoff and using RetryInterval
	RetryInterval      time.Duration // interval to wait once MaxBackoffDuration is reached
	Config             *Config
}

// Default backoff configurations
const (
	InitialBackoff = 1 * time.Second
	MaxBackoffStep = 30 * time.Second // cap the step size of exponential backoff
)

// Runner manages the lifecycle of multiple service processes.
type Runner struct {
	rc        RunnerConfig
	baseDir   string
	provision sync.Mutex
}

// NewRunner creates a new Runner based on the provided configuration.
func NewRunner(rc RunnerConfig) (*Runner, error) {
	exePath, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to get executable path: %w", err)
	}
	baseDir := filepath.Dir(exePath)
	return &Runner{
		rc:      rc,
		baseDir: baseDir,
	}, nil
}

// Start launches all configured services and blocks until the context is canceled.
func (r *Runner) Start(ctx context.Context) {
	hasDiscovery := false
	for i := range r.rc.Config.Services {
		sc := r.rc.Config.Services[i]
		if filepath.Base(sc.Command) == "discovery" {
			hasDiscovery = true
			go r.runService(ctx, sc)
		}
	}

	if hasDiscovery {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}

	for i := range r.rc.Config.Services {
		sc := r.rc.Config.Services[i]
		if filepath.Base(sc.Command) == "discovery" {
			continue
		}
		go r.runService(ctx, sc)
	}
	<-ctx.Done()
}

func (r *Runner) runService(ctx context.Context, sc ServiceConfig) {
	var backoff time.Duration
	var firstCrashTime time.Time

	for {
		// Select delay if we are backing off
		if backoff > 0 {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
		}

		args := make([]string, 0, len(sc.Args)*2)
		for k, v := range sc.Args {
			args = append(args, fmt.Sprintf("--%s=%s", k, v))
		}

		cmdPath := filepath.Join(r.baseDir, filepath.Base(sc.Command))
		cmd := exec.CommandContext(ctx, cmdPath, args...)
		cmd.Dir = r.baseDir
		cmd.Env = os.Environ()
		for k, v := range sc.Environment {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
		if inviteCode := r.maybeProvisionServiceInvite(ctx, sc); inviteCode != "" {
			cmd.Env = append(cmd.Env, fmt.Sprintf("INVARIANT_INVITE=%s", inviteCode))
		}
		cmd.Stdout = &prefixWriter{cmd: cmd, name: sc.Command, out: os.Stdout}
		cmd.Stderr = &prefixWriter{cmd: cmd, name: sc.Command, out: os.Stderr}

		log.Printf("Starting service [%s] command: %s %v", sc.Command, cmdPath, args)
		startTime := time.Now()

		err := cmd.Run()

		if ctx.Err() != nil {
			return // Context canceled, shutting down
		}

		uptime := time.Since(startTime)
		log.Printf("Service [%s] exited strongly after %v: %v", sc.Command, uptime, err)

		if uptime > 30*time.Second {
			// Process lived for a while, reset backoff
			backoff = 0
			firstCrashTime = time.Time{}
			log.Printf("Service [%s] lived for %v, resetting backoff", sc.Command, uptime)
		}

		if firstCrashTime.IsZero() {
			firstCrashTime = time.Now()
		}

		// Calculate backoff
		if backoff == 0 {
			backoff = InitialBackoff
		} else {
			backoff *= 2
			if backoff > MaxBackoffStep {
				backoff = MaxBackoffStep
			}
		}

		if time.Since(firstCrashTime) > r.rc.MaxBackoffDuration {
			log.Printf("Service [%s] has been failing for over %v.", sc.Command, r.rc.MaxBackoffDuration)
			log.Printf("Waiting %v interval before attempting to restart [%s] again", r.rc.RetryInterval, sc.Command)
			backoff = r.rc.RetryInterval
			firstCrashTime = time.Time{} // Reset the crash time counter so it can exponential backoff again after the long wait, or just stay on long wait?
			// To match requirements, we'll try again after the interval, back to exponential backoff
		} else {
			log.Printf("Restarting service [%s] in %v (exponential back-off)", sc.Command, backoff)
		}
	}
}

func (r *Runner) maybeProvisionServiceInvite(ctx context.Context, sc ServiceConfig) string {
	svcName := filepath.Base(sc.Command)
	if svcName == "discovery" {
		return ""
	}
	discoveryURL := sc.Args["discovery"]
	if discoveryURL == "" {
		return ""
	}
	if sc.Args["invite"] != "" || sc.Args["service-key"] != "" {
		return ""
	}
	if sc.Environment != nil {
		if sc.Environment["INVARIANT_INVITE"] != "" || sc.Environment["INVARIANT_ACCESS_TOKEN"] != "" || sc.Environment["INVARIANT_SERVICE_KEY"] != "" {
			return ""
		}
	}

	serviceID := sc.Args["id"]
	if serviceID == "" {
		serviceID = svcName
	}
	tokenFile := sc.Args["token-file"]
	if tokenFile == "" {
		tokenFile = itp.DefaultTokenPath(serviceID)
	}
	if tok, err := itp.LoadAccessToken(tokenFile); err == nil && tok != "" {
		return ""
	}

	r.provision.Lock()
	defer r.provision.Unlock()

	pk, err := itp.LoadOrGenerateKey(itp.DefaultKeyPath("client"))
	if err != nil {
		return ""
	}

	adminToken := r.resolveOrRedeemAdminToken(ctx, discoveryURL, pk.Private.Public().String())
	if adminToken == "" {
		return ""
	}

	httpClient := itp.NewHTTPClient(pk.Private, adminToken)
	client := discovery.NewClient(discoveryURL, httpClient).WithAccessToken(adminToken)

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	inv, err := client.CreateInvitation(reqCtx, svcName, discovery.RoleService, discovery.NodeIdentity{}, discovery.DefaultInvitationTTL)
	if err != nil {
		log.Printf("Runner could not auto-provision invitation for [%s]: %v", svcName, err)
		return ""
	}
	log.Printf("Provisioned single-use service invitation %s for [%s]", inv.ID, svcName)
	return inv.Code
}

func (r *Runner) resolveOrRedeemAdminToken(ctx context.Context, discoveryURL, pubKey string) string {
	if tok := os.Getenv("INVARIANT_ACCESS_TOKEN"); tok != "" {
		return tok
	}
	if tok, err := itp.LoadAccessToken(itp.DefaultTokenPath("client")); err == nil && tok != "" {
		return tok
	}
	if tok := os.Getenv("INVARIANT_OAUTH_TOKEN"); tok != "" {
		return tok
	}

	var candidateFiles []string
	for _, svc := range r.rc.Config.Services {
		if filepath.Base(svc.Command) == "discovery" {
			if f := svc.Args["bootstrap-invite-file"]; f != "" {
				candidateFiles = append(candidateFiles, f)
			}
			if d := svc.Args["dir"]; d != "" {
				candidateFiles = append(candidateFiles, filepath.Join(d, "bootstrap.invite"))
			}
		}
	}
	if keysDir, err := invconfig.KeysDir(); err == nil {
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
		return ""
	}

	pk, err := itp.LoadOrGenerateKey(itp.DefaultKeyPath("client"))
	if err != nil {
		return ""
	}
	httpClient := itp.NewHTTPClient(pk.Private, "")
	client := discovery.NewClient(discoveryURL, httpClient)

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	rec, err := client.RedeemInvitation(reqCtx, bootstrapCode, pubKey, discovery.NodeIdentity{Name: "runner-admin"})
	if err != nil {
		return ""
	}
	_ = itp.SaveAccessToken(itp.DefaultTokenPath("client"), rec.Token)
	return rec.Token
}

type prefixWriter struct {
	cmd  *exec.Cmd
	name string
	out  io.Writer
	line []byte
}

func (w *prefixWriter) Write(p []byte) (n int, err error) {
	for _, b := range p {
		w.line = append(w.line, b)
		if b == '\n' {
			pid := -1
			if w.cmd != nil && w.cmd.Process != nil {
				pid = w.cmd.Process.Pid
			}
			fmt.Fprintf(w.out, "[%s:%d] %s", w.name, pid, string(w.line))
			w.line = w.line[:0]
		}
	}
	return len(p), nil
}
