package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/chuckjaz/invariant-go/internal/content"
	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/files"
	"github.com/chuckjaz/invariant-go/internal/finder"
	"github.com/chuckjaz/invariant-go/internal/itp"
	"github.com/chuckjaz/invariant-go/internal/slots"
	"github.com/chuckjaz/invariant-go/internal/storage"
	"github.com/chuckjaz/invariant-go/internal/trace"
)

func main() {
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var dir string
	flag.StringVar(&dir, "dir", "", "Directory for persisting files service credentials")
	var rootAddr string
	flag.StringVar(&rootAddr, "root", "", "Root block or slot address")
	var slot string
	flag.StringVar(&slot, "slot", "", "Whether the root address refers to a slot")
	var listenIP bool
	flag.BoolVar(&listenIP, "listen-ip", false, "Listen on a host IP port instead of using ITP (Tailcat)")
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on when -listen-ip is enabled (0 for random available port)")
	var serviceKey string
	flag.StringVar(&serviceKey, "service-key", "", "Access token or service key for authenticating with discovery / control plane")
	var inviteCode string
	flag.StringVar(&inviteCode, "invite", "", "One-time invitation code to redeem for a discovery access token (defaults to INVARIANT_INVITE)")
	var provisionInvite string
	flag.StringVar(&provisionInvite, "provision", "", "Redeem an invitation code, store the service token in the configuration directory, and exit")
	var tokenFile string
	flag.StringVar(&tokenFile, "token-file", "", "Path to persist redeemed discovery access token")
	var keyFile string
	flag.StringVar(&keyFile, "key-file", "", "Path to private key file for ITP")
	var derpMapURL string
	flag.StringVar(&derpMapURL, "derp-map", "", "URL to fetch custom DERP map from for ITP")
	var enableTrace bool
	flag.BoolVar(&enableTrace, "trace", false, "Enable distributed tracing on this service")
	flag.Parse()

	if serviceKey == "" {
		serviceKey = os.Getenv("INVARIANT_SERVICE_KEY")
	}
	if provisionInvite != "" {
		inviteCode = provisionInvite
	}

	discoveryURL = itp.ResolveDiscoveryURL(dir, "files", discoveryURL, inviteCode)
	if discoveryURL == "" {
		log.Fatalf("Discovery URL is required")
	}

	keyFile = itp.ServiceKeyPath(dir, "files", keyFile)
	privKey, err := itp.LoadOrGenerateKey(keyFile)
	if err != nil {
		log.Fatalf("Failed to load or generate ITP key: %v", err)
	}

	resolvedToken, err := itp.ResolveOrRedeemToken(context.Background(), itp.ServiceAuthOptions{
		DiscoveryURL: discoveryURL,
		Dir:          dir,
		ServiceID:    "files",
		ServiceName:  "files",
		ExplicitKey:  serviceKey,
		InviteCode:   inviteCode,
		TokenFile:    tokenFile,
		PrivateKey:   privKey,
		DERPMapURL:   derpMapURL,
	})
	if err != nil {
		log.Fatalf("Failed to authenticate with discovery service: %v", err)
	}
	if provisionInvite != "" {
		log.Printf("Successfully provisioned service token to %s", itp.ServiceTokenPath(dir, "files", tokenFile))
		return
	}
	serviceKey = resolvedToken

	httpClient := itp.NewHTTPClient(privKey.Private, serviceKey, itp.WithDERPMapURL(derpMapURL))

	dClient := discovery.NewClient(discoveryURL, httpClient)
	if serviceKey != "" {
		dClient = dClient.WithServiceKey(serviceKey)
	}

	rootIsSlot := false
	if slot != "" {
		rootAddr = slot
		rootIsSlot = true
	}

	findService := func(kind string) string {
		id, err := dClient.Find(context.Background(), kind, "", 1)
		if err != nil {
			log.Fatalf("Could not find %s service: %v", kind, err)
		}
		if len(id) == 0 {
			log.Fatalf("Could not find %s service", kind)
		}
		return id[0].Address
	}

	finderAddr := findService("finder-v1")
	finderClient := finder.NewClient(finderAddr, httpClient)
	storageClient := storage.NewAggregateClient(finderClient, dClient, 3, 1000)
	slotsAddr := findService("slots-v1")
	slotsClient := slots.NewClient(slotsAddr, httpClient)

	opts := files.Options{
		Storage: storageClient,
		Slots:   slotsClient,
		RootLink: content.ContentLink{
			Address: rootAddr,
			Slot:    rootIsSlot,
		},
		AutoSyncTimeout:  time.Minute,
		SlotPollInterval: 5 * time.Minute,
	}

	f, err := files.NewInMemoryFiles(opts)
	if err != nil {
		log.Fatalf("Failed to initialize files service: %v", err)
	}
	defer f.Close()

	server := files.NewServer(f)
	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
	}

	listener, err := itp.Listen(context.Background(), itp.ListenOptions{
		PrivateKey:   privKey,
		ListenIP:     listenIP,
		Port:         port,
		ControlPlane: dClient,
		DERPMapURL:   derpMapURL,
	})
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	if listenIP {
		log.Printf("Files service listening on :%d (HTTP)...", listener.Port())
	} else {
		log.Printf("Files service listening on %s (ITP Address: %s, Public Key: %s)...", listener.ITPAddr, listener.ITPAddr, privKey.Private.Public())
	}
	log.Fatal(http.Serve(listener, server.Handler()))
}
