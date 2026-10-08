package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/distribute"
	"invariant/internal/itp"
	"invariant/internal/tags"
	"invariant/internal/trace"
)

func generateID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	var id string
	flag.StringVar(&id, "id", "", "ID of the distribute service (32-byte hex). Randomly generated if not provided.")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	tagFlags := tags.RegisterFlags()
	var repFactor int
	flag.IntVar(&repFactor, "N", 3, "Replication factor for blocks")
	var destination string
	flag.StringVar(&destination, "destination", "", "Destination storage service ID to backup blocks to")
	var backupRate float64
	flag.Float64Var(&backupRate, "backup-rate", 0, "Rate limit in MB/hour for destination backups (0 for unlimited)")
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
	var name string
	flag.StringVar(&name, "name", "", "Name to register with the names service")
	var enableTrace bool
	flag.BoolVar(&enableTrace, "trace", false, "Enable distributed tracing on this service")
	flag.Parse()

	if authToken == "" {
		authToken = os.Getenv("INVARIANT_AUTH_TOKEN")
	}

	if id == "" {
		id = generateID()
	}

	if keyFile == "" {
		keyFile = itp.DefaultKeyPath(id)
	}
	privKey, err := itp.LoadOrGenerateKey(keyFile)
	if err != nil {
		log.Fatalf("Failed to load or generate ITP key: %v", err)
	}

	httpClient := itp.NewHTTPClient(privKey.Private, authToken, itp.WithDERPMapURL(derpMapURL))

	var disc *discovery.Client
	var cp discovery.ControlPlane
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, httpClient)
		if authToken != "" {
			disc = disc.WithAuthToken(authToken)
		}
		cp = disc
	}

	if destination != "" {
		if disc == nil {
			log.Fatalf("Discovery service required to resolve destination %q", destination)
		}
		var err error
		destination, err = discovery.ResolveName(context.Background(), disc, destination)
		if err != nil {
			log.Fatalf("Failed to resolve destination name %q: %v", destination, err)
		}
	}

	d := distribute.NewInMemoryDistribute(disc, repFactor, 3, destination, backupRate)
	if disc != nil {
		d.StartSync(10 * time.Second)
	}

	server := distribute.NewDistributeServer(id, d)
	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
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

	if disc != nil {
		nodeIdent := discovery.NodeIdentity{
			Name: name,
		}
		_ = disc.RegisterClient(context.Background(), privKey.Private.Public().String(), nodeIdent)

		advertise := advertiseAddr
		if advertise == "" && !listenIP {
			advertise = listener.ITPAddr
		}

		err := discovery.AdvertiseAndRegister(
			context.Background(),
			disc,
			server.ID(),
			advertise,
			listener.Port(),
			[]string{"distribute-v1", "notify-v1"},
			tagFlags.Tags(),
			discovery.WithPublicKey(privKey.Private.Public().String()),
			discovery.WithIdentity(nodeIdent),
		)
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, server.ID())
	}

	if name != "" {
		go func() {
			err := discovery.RegisterName(context.Background(), disc, name, server.ID(), []string{"distribute-v1", "notify-v1"})
			if err != nil {
				log.Printf("Failed to register name %q: %v", name, err)
			} else {
				log.Printf("Registered name %q for ID %s", name, server.ID())
			}
		}()
	}

	if listenIP {
		log.Printf("Distribute service (ID %s) listening on :%d (HTTP)...", server.ID(), listener.Port())
	} else {
		log.Printf("Distribute service (ID %s) listening on %s (ITP Address: %s, Public Key: %s)...", server.ID(), listener.ITPAddr, listener.ITPAddr, privKey.Private.Public())
	}
	log.Printf("Using In-Memory distribute storage")

	log.Fatal(http.Serve(listener, server))
}
