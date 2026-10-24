package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/finder"
	"github.com/chuckjaz/invariant-go/internal/itp"
	"github.com/chuckjaz/invariant-go/internal/tags"
	"github.com/chuckjaz/invariant-go/internal/trace"
)

func generateID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func main() {
	var id string
	flag.StringVar(&id, "id", "", "ID of the finder service (32-byte hex). Ramdomly generated if not provided.")
	var dir string
	flag.StringVar(&dir, "dir", "", "Directory for persisting finder state (e.g. ID)")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	tagFlags := tags.RegisterFlags()
	var listenIP bool
	flag.BoolVar(&listenIP, "listen-ip", false, "Listen on a host IP port instead of using ITP (Tailcat)")
	var port int
	flag.IntVar(&port, "port", 3004, "Port to listen on when -listen-ip is enabled (using 3004 by default)")
	var serviceKey string
	flag.StringVar(&serviceKey, "service-key", "", "Service key for authenticating with discovery / control plane (defaults to INVARIANT_SERVICE_KEY)")
	var keyFile string
	flag.StringVar(&keyFile, "key-file", "", "Path to private key file for ITP (defaults to ~/.invariant/keys/<service-id>.key)")
	var derpMapURL string
	flag.StringVar(&derpMapURL, "derp-map", "", "URL to fetch custom DERP map from for ITP")
	var name string
	flag.StringVar(&name, "name", "", "Name to register with the names service")
	var enableTrace bool
	flag.BoolVar(&enableTrace, "trace", false, "Enable distributed tracing on this service")
	flag.Parse()

	if serviceKey == "" {
		serviceKey = os.Getenv("INVARIANT_SERVICE_KEY")
	}

	if id == "" {
		if dir != "" {
			os.MkdirAll(dir, 0755)
			idPath := filepath.Join(dir, "id")
			if data, err := os.ReadFile(idPath); err == nil && len(data) == 64 {
				id = string(data)
			} else {
				id = generateID()
				if err := os.WriteFile(idPath, []byte(id), 0644); err != nil {
					log.Printf("Failed to write ID file: %v", err)
				}
			}
		} else {
			id = generateID()
		}
	}

	f, err := finder.NewMemoryFinder(id)
	if err != nil {
		log.Fatalf("Failed to create finder: %v", err)
	}

	if keyFile == "" {
		keyFile = itp.DefaultKeyPath(id)
	}
	privKey, err := itp.LoadOrGenerateKey(keyFile)
	if err != nil {
		log.Fatalf("Failed to load or generate ITP key: %v", err)
	}

	httpClient := itp.NewHTTPClient(privKey.Private, serviceKey, itp.WithDERPMapURL(derpMapURL))

	var disc *discovery.Client
	var cp discovery.ControlPlane
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, httpClient)
		if serviceKey != "" {
			disc = disc.WithServiceKey(serviceKey)
		}
		cp = disc
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
			id,
			advertise,
			listener.Port(),
			[]string{"finder-v1", "notify-v1"},
			tagFlags.Tags(),
			discovery.WithPublicKey(privKey.Private.Public().String()),
			discovery.WithIdentity(nodeIdent),
		)
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, id)
	}

	if name != "" {
		go func() {
			err := discovery.RegisterName(context.Background(), disc, name, id, []string{"finder-v1", "notify-v1"})
			if err != nil {
				log.Printf("Failed to register name %q: %v", name, err)
			} else {
				log.Printf("Registered name %q for ID %s", name, id)
			}
		}()
	}

	server := finder.NewFinderServer(f, disc)
	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
	}

	if listenIP {
		log.Printf("Finder service (ID %s) listening on :%d (HTTP)...", id, listener.Port())
	} else {
		log.Printf("Finder service (ID %s) listening on %s (ITP Address: %s, Public Key: %s)...", id, listener.ITPAddr, listener.ITPAddr, privKey.Private.Public())
	}
	log.Printf("Using In-Memory routing and storage mapping")

	log.Fatal(http.Serve(listener, server))
}
