// Package main provides the command-line utility for the slots service.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/itp"
	"invariant/internal/notify"
	"invariant/internal/slots"
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
	flag.StringVar(&id, "id", "", "ID of the slots service (32-byte hex). Randomly generated if not provided.")
	var dir string
	flag.StringVar(&dir, "dir", "", "Base directory for file system slots storage")
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
	var snapshotInterval time.Duration
	flag.DurationVar(&snapshotInterval, "snapshot-interval", 1*time.Hour, "Interval between snapshots for file system storage")
	var notifyIDs string
	flag.StringVar(&notifyIDs, "notify", "", "Comma-separated list of IDs implementing the Notify protocol (e.g. finder)")
	var notifyBatchSize int
	flag.IntVar(&notifyBatchSize, "notify-batch-size", 10000, "Number of slot IDs to send per request")
	var notifyBatchDuration time.Duration
	flag.DurationVar(&notifyBatchDuration, "notify-duration", 1*time.Second, "Maximum duration to wait before sending a batch of new slot notifications")
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

	var s slots.Slots
	if dir != "" {
		fss, err := slots.NewFileSystemSlots(dir, snapshotInterval)
		if err != nil {
			log.Fatalf("Failed to initialize file system slots: %v", err)
		}
		defer fss.Close()
		s = fss
	} else {
		s = slots.NewMemorySlots(id)
	}

	if keyFile == "" {
		keyFile = itp.DefaultKeyPath(s.ID())
	}
	privKey, err := itp.LoadOrGenerateKey(keyFile)
	if err != nil {
		log.Fatalf("Failed to load or generate ITP key: %v", err)
	}

	httpClient := itp.NewHTTPClient(privKey.Private, authToken, itp.WithDERPMapURL(derpMapURL))

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
		nodeIdent := discovery.NodeIdentity{
			Name: name,
		}
		_ = dClient.RegisterClient(context.Background(), privKey.Private.Public().String(), nodeIdent)

		advertise := advertiseAddr
		if advertise == "" && !listenIP {
			advertise = listener.ITPAddr
		}

		err := discovery.AdvertiseAndRegister(
			context.Background(),
			dClient,
			s.ID(),
			advertise,
			listener.Port(),
			[]string{"slots-v1"},
			tagFlags.Tags(),
			discovery.WithPublicKey(privKey.Private.Public().String()),
			discovery.WithIdentity(nodeIdent),
		)
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, s.ID())

		if name != "" {
			go func() {
				err := discovery.RegisterName(context.Background(), dClient, name, s.ID(), []string{"slots-v1"})
				if err != nil {
					log.Printf("Failed to register name %q: %v", name, err)
				} else {
					log.Printf("Registered name %q for ID %s", name, s.ID())
				}
			}()
		}
	} else if name != "" {
		log.Fatalf("Cannot register name without a valid discovery service")
	}

	server := slots.NewServer(s)
	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
	}

	var notifyClients []slots.NotifyClient
	if dClient != nil {
		for nid := range strings.SplitSeq(notifyIDs, ",") {
			nid = strings.TrimSpace(nid)
			if nid == "" {
				continue
			}

			resolvedID, err := discovery.ResolveName(context.Background(), dClient, nid)
			if err != nil {
				log.Fatalf("Could not resolve notify name/id %s: %v", nid, err)
				continue
			}

			desc, ok := dClient.Get(context.Background(), resolvedID)
			if !ok {
				log.Fatalf("Could not find address for Notify service %s", resolvedID)
				continue
			}

			notifyClients = append(notifyClients, notify.NewClient(desc.Address, httpClient))
		}
	} else if notifyIDs != "" {
		log.Fatalf("a discovery service is required to use the -notify flag")
	}

	if len(notifyClients) > 0 {
		server.StartNotification(context.Background(), notifyClients, notifyBatchSize, notifyBatchDuration)
	}

	if listenIP {
		log.Printf("Slots service (ID %s) listening on :%d (HTTP)...", s.ID(), listener.Port())
	} else {
		log.Printf("Slots service (ID %s) listening on %s (ITP Address: %s, Public Key: %s)...", s.ID(), listener.ITPAddr, listener.ITPAddr, privKey.Private.Public())
	}
	if dir != "" {
		log.Printf("Using File System Slots storage at %s", dir)
	} else {
		log.Printf("Using In-Memory Slots storage")
	}

	log.Fatal(http.Serve(listener, server))
}
