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
	"path/filepath"
	"strings"
	"time"

	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/discovery/backup"
	"github.com/chuckjaz/invariant-go/internal/itp"
	"github.com/chuckjaz/invariant-go/internal/slots"
	"github.com/chuckjaz/invariant-go/internal/storage"
	"github.com/chuckjaz/invariant-go/internal/trace"
)

func fatalf(format string, args ...any) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	fmt.Fprintf(os.Stderr, format, args...)
	os.Exit(1)
}

func fatal(args ...any) {
	fmt.Fprintln(os.Stderr, args...)
	os.Exit(1)
}

func main() {
	var listenIP bool
	flag.BoolVar(&listenIP, "listen-ip", false, "Listen on a host IP port instead of using ITP (Tailcat)")
	var port int
	flag.IntVar(&port, "port", 3003, "Port to listen on when -listen-ip is enabled")
	var serviceKey string
	flag.StringVar(&serviceKey, "service-key", "", "Static service key for authenticating services (defaults to INVARIANT_SERVICE_KEY)")
	var serviceKeysFile string
	flag.StringVar(&serviceKeysFile, "service-keys-file", "", "Path to file containing newline-delimited pre-authorized service keys")
	var oauthUserInfoURL string
	flag.StringVar(&oauthUserInfoURL, "oauth-userinfo-url", "", "URL of OAuth2/OIDC UserInfo endpoint to validate user bearer tokens")
	var storageURL string
	flag.StringVar(&storageURL, "storage", "", "Storage service address for CAS journal backup (must have 'originals' tag)")
	var slotsURL string
	flag.StringVar(&slotsURL, "slots", "", "Slots service address for journal backup slot pointer")
	var slotID string
	flag.StringVar(&slotID, "slot-id", "discovery-journal", "Slot ID for journal backup")
	var keyFile string
	flag.StringVar(&keyFile, "key-file", "", "Path to private key file for ITP (defaults to ~/.invariant/keys/discovery.key)")
	var derpMapURL string
	flag.StringVar(&derpMapURL, "derp-map", "", "URL to fetch custom DERP map from for ITP")
	var dir string
	flag.StringVar(&dir, "dir", "", "Base directory for file system discovery storage")
	var upstreamURL string
	flag.StringVar(&upstreamURL, "upstream", "", "Upstream discovery service URL to delegate queries to")
	var snapshotInterval time.Duration
	flag.DurationVar(&snapshotInterval, "snapshot-interval", 1*time.Hour, "Interval between snapshots for file system storage")
	var healthInterval time.Duration
	flag.DurationVar(&healthInterval, "health-interval", 30*time.Second, "Interval for active health checks")
	var healthTimeout time.Duration
	flag.DurationVar(&healthTimeout, "health-timeout", 5*time.Minute, "Time before a continuously unhealthy node is evicted")
	var allowAnonymous bool
	flag.BoolVar(&allowAnonymous, "allow-anonymous", false, "Allow unauthenticated requests when no credentials are provided")
	var bootstrapInviteFile string
	flag.StringVar(&bootstrapInviteFile, "bootstrap-invite-file", "", "Path to write initial bootstrap admin invitation code (defaults to <dir>/bootstrap.invite or ~/.invariant/keys/bootstrap.invite)")
	var bootstrapTTL time.Duration
	flag.DurationVar(&bootstrapTTL, "bootstrap-ttl", 1*time.Hour, "TTL for the initial bootstrap admin invitation")
	var enableTrace bool
	flag.BoolVar(&enableTrace, "trace", false, "Enable distributed tracing on this service")
	var enableLog bool
	flag.BoolVar(&enableLog, "log", true, "Enable logging output")
	flag.Parse()

	if !enableLog {
		log.SetOutput(io.Discard)
	}

	if serviceKey == "" {
		serviceKey = os.Getenv("INVARIANT_SERVICE_KEY")
	}
	if oauthUserInfoURL == "" {
		oauthUserInfoURL = os.Getenv("INVARIANT_OAUTH_USERINFO_URL")
	}

	var localD discovery.Discovery
	var backupSink *backup.ChainedBackup

	if dir != "" {
		fsd, err := discovery.NewFileSystemDiscovery(dir, snapshotInterval)
		if err != nil {
			fatalf("Failed to initialize file system discovery: %v", err)
		}

		if storageURL != "" && slotsURL != "" {
			storageClient := storage.NewClient(storageURL, nil)
			slotsClient := slots.NewClient(slotsURL, nil)
			backupDir := filepath.Join(dir, "backup")

			var backupErr error
			backupSink, backupErr = backup.NewChainedBackup(backup.BackupOptions{
				BaseDir:    backupDir,
				Storage:    storageClient,
				Slots:      slotsClient,
				SlotID:     slotID,
				MaxEntries: 100,
			})
			if backupErr != nil {
				log.Printf("Warning: failed to initialize journal backup: %v", backupErr)
			} else {
				// Replay historical entries from slot / CAS storage
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				if err := backupSink.Replay(ctx, fsd); err != nil {
					log.Printf("Warning: journal backup replay returned: %v", err)
				}
				cancel()

				fsd.WithEventSink(backupSink)
				defer backupSink.Close()
				log.Printf("Enabled periodic journal backup to storage %s and slot %s", storageURL, slotID)
			}
		}

		if healthInterval > 0 {
			fsd = fsd.WithHealthTracking(healthInterval, healthTimeout)
		}
		defer fsd.Close()
		localD = fsd
	} else {
		imd := discovery.NewInMemoryDiscovery()
		if healthInterval > 0 {
			imd = imd.WithHealthTracking(healthInterval, healthTimeout)
		}
		localD = imd
	}

	var d discovery.Discovery
	if upstreamURL != "" {
		parent := discovery.NewClient(upstreamURL, nil)
		if serviceKey != "" {
			parent = parent.WithServiceKey(serviceKey)
		}
		d = discovery.NewUpstreamDiscovery(localD, parent)
		log.Printf("Using Upstream discovery delegation pointing to %s", upstreamURL)
	} else {
		d = localD
		if dir != "" {
			log.Printf("Using standalone File System discovery storage at %s", dir)
		} else {
			log.Printf("Using standalone In-Memory discovery storage")
		}
	}

	server := discovery.NewDiscoveryServer(d)
	if cp, ok := localD.(discovery.ControlPlane); ok {
		server.WithControlPlane(cp)
	}
	if skm, ok := localD.(discovery.ServiceKeyManager); ok {
		server.WithServiceKeyManager(skm)
	}
	if im, ok := localD.(discovery.InvitationManager); ok {
		server.WithInvitationManager(im)
	}
	server.WithRequireAuth(!allowAnonymous)

	if serviceKey != "" {
		server.WithServiceKeys(serviceKey)
	}
	if serviceKeysFile != "" {
		if file, err := os.Open(serviceKeysFile); err == nil {
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line != "" && !strings.HasPrefix(line, "#") {
					server.WithServiceKeys(line)
				}
			}
			file.Close()
		} else {
			fatalf("Failed to open service keys file %s: %v", serviceKeysFile, err)
		}
	}
	if oauthUserInfoURL != "" {
		server.WithOAuthAuthenticator(discovery.NewUserInfoAuthenticator(oauthUserInfoURL, nil, 5*time.Minute))
		log.Printf("Configured OAuth UserInfo authentication against %s", oauthUserInfoURL)
	}
	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
	}

	var listener *itp.Listener
	var err error
	var controlPlaneURL string
	if listenIP {
		listener, err = itp.Listen(context.Background(), itp.ListenOptions{
			ListenIP: true,
			Port:     port,
		})
		if err != nil {
			fatalf("Failed to listen on IP port %d: %v", port, err)
		}
		defer listener.Close()
		controlPlaneURL = fmt.Sprintf("http://127.0.0.1:%d", listener.Port())
		log.Printf("Discovery service listening on :%d (HTTP)...", listener.Port())
	} else {
		if keyFile == "" {
			if dir != "" {
				keyFile = filepath.Join(dir, "discovery.key")
			} else {
				keyFile = itp.DefaultKeyPath("discovery")
			}
		}
		privKey, err := itp.LoadOrGenerateKey(keyFile)
		if err != nil {
			fatalf("Failed to load or generate ITP key: %v", err)
		}
		listener, err = itp.Listen(context.Background(), itp.ListenOptions{
			PrivateKey: privKey,
			DERPMapURL: derpMapURL,
		})
		if err != nil {
			fatalf("Failed to listen on ITP: %v", err)
		}
		defer listener.Close()
		server.WithPeerKeyResolver(listener)
		controlPlaneURL = listener.ITPAddr
		log.Printf("Discovery service listening on ITP address %s (Public Key: %s)...", listener.ITPAddr, listener.PublicKey)
	}

	server.WithControlPlaneURL(controlPlaneURL)
	if cpSetter, ok := localD.(interface{ WithControlPlaneURL(string) }); ok {
		cpSetter.WithControlPlaneURL(controlPlaneURL)
	}

	if !allowAnonymous {
		if im, ok := localD.(discovery.InvitationManager); ok {
			invitePath := bootstrapInviteFile
			if invitePath == "" {
				if dir != "" {
					invitePath = filepath.Join(dir, "bootstrap.invite")
				} else {
					home, _ := os.UserHomeDir()
					if home == "" {
						home = "."
					}
					invitePath = filepath.Join(home, ".invariant", "keys", "bootstrap.invite")
				}
			}
			inv, created, err := discovery.EnsureBootstrapInvitation(context.Background(), im, bootstrapTTL, invitePath, controlPlaneURL)
			if err != nil {
				log.Printf("Warning: failed to ensure bootstrap admin invitation: %v", err)
			} else if inv != nil {
				if created {
					log.Printf("Generated bootstrap admin invitation (expires %s): %s (saved to %s)", inv.ExpiresAt.Format(time.RFC3339), inv.Code, invitePath)
				} else {
					log.Printf("Active bootstrap admin invitation (expires %s): %s (saved to %s)", inv.ExpiresAt.Format(time.RFC3339), inv.Code, invitePath)
				}
			}
		}
	}

	fatal(http.Serve(listener, server))
}
