package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/itp"
	"invariant/internal/trace"
)

func main() {
	var listenIP bool
	flag.BoolVar(&listenIP, "listen-ip", false, "Listen on a host IP port instead of using ITP (Tailcat)")
	var port int
	flag.IntVar(&port, "port", 3003, "Port to listen on when -listen-ip is enabled")
	var authToken string
	flag.StringVar(&authToken, "auth-token", "", "Bearer token for authenticating discovery and control plane requests (defaults to INVARIANT_AUTH_TOKEN)")
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
	var enableTrace bool
	flag.BoolVar(&enableTrace, "trace", false, "Enable distributed tracing on this service")
	flag.Parse()

	if authToken == "" {
		authToken = os.Getenv("INVARIANT_AUTH_TOKEN")
	}

	var localD discovery.Discovery
	if dir != "" {
		fsd, err := discovery.NewFileSystemDiscovery(dir, snapshotInterval)
		if err != nil {
			log.Fatalf("Failed to initialize file system discovery: %v", err)
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
		if authToken != "" {
			parent = parent.WithAuthToken(authToken)
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
	if authToken != "" {
		server.WithAuthToken(authToken)
	}
	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
	}

	var listener *itp.Listener
	var err error
	if listenIP {
		listener, err = itp.Listen(context.Background(), itp.ListenOptions{
			ListenIP: true,
			Port:     port,
		})
		if err != nil {
			log.Fatalf("Failed to listen on IP port %d: %v", port, err)
		}
		defer listener.Close()
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
			log.Fatalf("Failed to load or generate ITP key: %v", err)
		}
		listener, err = itp.Listen(context.Background(), itp.ListenOptions{
			PrivateKey: privKey,
			DERPMapURL: derpMapURL,
		})
		if err != nil {
			log.Fatalf("Failed to listen on ITP: %v", err)
		}
		defer listener.Close()
		log.Printf("Discovery service listening on ITP address %s (Public Key: %s)...", listener.ITPAddr, listener.PublicKey)
	}

	log.Fatal(http.Serve(listener, server))
}
