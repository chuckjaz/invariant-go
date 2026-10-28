package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/identity"
	"github.com/chuckjaz/invariant-go/internal/itp"
	"github.com/chuckjaz/invariant-go/internal/names"
	"github.com/chuckjaz/invariant-go/internal/tags"
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
	var upstreamURL string
	flag.StringVar(&upstreamURL, "upstream", "", "Upstream name service URL to delegate queries to")
	var snapshotInterval time.Duration
	flag.DurationVar(&snapshotInterval, "snapshot-interval", 1*time.Hour, "Interval between snapshots for file system storage")
	var enableTrace bool
	flag.BoolVar(&enableTrace, "trace", false, "Enable distributed tracing on this service")
	var enableLog bool
	flag.BoolVar(&enableLog, "log", false, "Enable logging output")
	flag.Parse()

	if !enableLog {
		log.SetOutput(io.Discard)
	}

	if serviceKey == "" {
		serviceKey = os.Getenv("INVARIANT_SERVICE_KEY")
	}
	if provisionInvite != "" {
		inviteCode = provisionInvite
	}

	var n names.Names
	if dir != "" {
		fsnd, err := names.NewFileSystemNames(dir, snapshotInterval)
		if err != nil {
			fatalf("Failed to initialize file system names: %v", err)
		}
		defer fsnd.Close()
		n = fsnd
	} else {
		n = names.NewInMemoryNames()
	}

	id := n.(identity.Identity).ID()
	keyID := id
	if dir == "" {
		keyID = "names"
	}
	keyFile = itp.ServiceKeyPath(dir, keyID, keyFile)
	privKey, err := itp.LoadOrGenerateKey(keyFile)
	if err != nil {
		fatalf("Failed to load or generate ITP key: %v", err)
	}
	discoveryURL = itp.ResolveDiscoveryURL(dir, keyID, discoveryURL, inviteCode)

	resolvedToken, err := itp.ResolveOrRedeemToken(context.Background(), itp.ServiceAuthOptions{
		DiscoveryURL: discoveryURL,
		Dir:          dir,
		ServiceID:    keyID,
		ServiceName:  "names",
		ExplicitKey:  serviceKey,
		InviteCode:   inviteCode,
		TokenFile:    tokenFile,
		PrivateKey:   privKey,
		DERPMapURL:   derpMapURL,
	})
	if err != nil {
		fatalf("Failed to authenticate with discovery service: %v", err)
	}
	if provisionInvite != "" {
		log.Printf("Successfully provisioned service token to %s", itp.ServiceTokenPath(dir, keyID, tokenFile))
		return
	}
	serviceKey = resolvedToken

	httpClient := itp.NewHTTPClient(privKey.Private, serviceKey, itp.WithDERPMapURL(derpMapURL))

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
		if serviceKey != "" {
			dClient = dClient.WithServiceKey(serviceKey)
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
		fatalf("Failed to listen: %v", err)
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
			fatalf("Failed to register with discovery service: %v", err)
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
	fatal(http.Serve(listener, server))
}
