package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/chuckjaz/invariant-go/internal/distribute"
	"github.com/chuckjaz/invariant-go/internal/itp"
	"github.com/chuckjaz/invariant-go/internal/tags"
	"github.com/chuckjaz/invariant-go/internal/trace"
)

func generateID() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

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
	var id string
	flag.StringVar(&id, "id", "", "ID of the distribute service (32-byte hex). Randomly generated if not provided.")
	var dir string
	flag.StringVar(&dir, "dir", "", "Directory for persisting distribute state and credentials")
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
	var name string
	flag.StringVar(&name, "name", "", "Name to register with the names service")
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

	explicitID := id
	if id == "" {
		if dir != "" {
			_ = os.MkdirAll(dir, 0755)
			idPath := filepath.Join(dir, "id")
			if data, err := os.ReadFile(idPath); err == nil && len(data) == 64 {
				id = string(data)
			} else {
				id = generateID()
				_ = os.WriteFile(idPath, []byte(id), 0644)
			}
		} else {
			id = generateID()
		}
	}

	keyID := id
	if dir == "" && explicitID == "" {
		keyID = "distribute"
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
		ServiceName:  name,
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

	var disc *discovery.Client
	var cp discovery.ControlPlane
	if discoveryURL != "" {
		disc = discovery.NewClient(discoveryURL, httpClient)
		if serviceKey != "" {
			disc = disc.WithServiceKey(serviceKey)
		}
		cp = disc
	}

	if destination != "" {
		if disc == nil {
			fatalf("Discovery service required to resolve destination %q", destination)
		}
		var err error
		destination, err = discovery.ResolveName(context.Background(), disc, destination)
		if err != nil {
			fatalf("Failed to resolve destination name %q: %v", destination, err)
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
		fatalf("Failed to listen: %v", err)
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
			fatalf("Failed to register with discovery service: %v", err)
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

	fatal(http.Serve(listener, server))
}
