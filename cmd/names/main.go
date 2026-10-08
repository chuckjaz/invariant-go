package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/identity"
	"invariant/internal/itp"
	"invariant/internal/names"
	"invariant/internal/tags"
	"invariant/internal/trace"
)

func main() {
	var dir string
	flag.StringVar(&dir, "dir", "", "Base directory for file system names storage")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	tagFlags := tags.RegisterFlags()
	var listenIP bool
	flag.BoolVar(&listenIP, "listen-ip", false, "Listen on a host IP port instead of using ITP (Tailcat)")
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on when -listen-ip is enabled (0 for random available port)")
	var authToken string
	flag.StringVar(&authToken, "auth-token", "", "Bearer token for authenticating with discovery / control plane (defaults to INVARIANT_AUTH_TOKEN)")
	var keyFile string
	flag.StringVar(&keyFile, "key-file", "", "Path to private key file for ITP (defaults to ~/.invariant/keys/<service-id>.key)")
	var derpMapURL string
	flag.StringVar(&derpMapURL, "derp-map", "", "URL to fetch custom DERP map from for ITP")
	var upstreamURL string
	flag.StringVar(&upstreamURL, "upstream", "", "Upstream name service URL to delegate queries to")
	var snapshotInterval time.Duration
	flag.DurationVar(&snapshotInterval, "snapshot-interval", 1*time.Hour, "Interval between snapshots for file system storage")
	var enableTrace bool
	flag.BoolVar(&enableTrace, "trace", false, "Enable distributed tracing on this service")
	flag.Parse()

	if authToken == "" {
		authToken = os.Getenv("INVARIANT_AUTH_TOKEN")
	}

	var n names.Names
	if dir != "" {
		fsnd, err := names.NewFileSystemNames(dir, snapshotInterval)
		if err != nil {
			log.Fatalf("Failed to initialize file system names: %v", err)
		}
		defer fsnd.Close()
		n = fsnd
	} else {
		n = names.NewInMemoryNames()
	}

	id := n.(identity.Identity).ID()
	if keyFile == "" {
		keyFile = itp.DefaultKeyPath(id)
	}
	privKey, err := itp.LoadOrGenerateKey(keyFile)
	if err != nil {
		log.Fatalf("Failed to load or generate ITP key: %v", err)
	}

	httpClient := itp.NewHTTPClient(privKey.Private, authToken, itp.WithDERPMapURL(derpMapURL))

	if upstreamURL != "" {
		parent := names.NewClient(upstreamURL, httpClient)
		n = names.NewUpstreamNames(n, parent)
		log.Printf("Using Upstream name delegation pointing to %s", upstreamURL)
	}

	server := names.NewNamesServer(n)
	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
	}

	var dClient *discovery.Client
	var cp discovery.ControlPlane
	if discoveryURL != "" {
		dClient = discovery.NewClient(discoveryURL, httpClient)
		if authToken != "" {
			dClient = dClient.WithAuthToken(authToken)
		}
		cp = dClient
	}

	listener, err := itp.Listen(context.Background(), itp.ListenOptions{
		PrivateKey:   privKey,
		ListenIP:     listenIP,
		Port:         port,
		ControlPlane: cp,
		DERPMapURL:   derpMapURL,
	})
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	if dClient != nil {
		nodeIdent := discovery.NodeIdentity{}
		_ = dClient.RegisterClient(context.Background(), privKey.Private.Public().String(), nodeIdent)

		advertise := advertiseAddr
		if advertise == "" && !listenIP {
			advertise = listener.ITPAddr
		}

		err := discovery.AdvertiseAndRegister(
			context.Background(),
			dClient,
			id,
			advertise,
			listener.Port(),
			[]string{"names-v1"},
			tagFlags.Tags(),
			discovery.WithPublicKey(privKey.Private.Public().String()),
			discovery.WithIdentity(nodeIdent),
		)
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, id)
	}

	if listenIP {
		log.Printf("Names service listening on :%d (HTTP)...", listener.Port())
	} else {
		log.Printf("Names service listening on %s (ITP Address: %s, Public Key: %s)...", listener.ITPAddr, listener.ITPAddr, privKey.Private.Public())
	}
	if dir != "" {
		log.Printf("Using File System Names storage at %s", dir)
	} else {
		log.Printf("Using In-Memory Names storage")
	}
	log.Fatal(http.Serve(listener, server))
}
