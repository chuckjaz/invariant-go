// Package main provides the command-line utility for the slots service.
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
	"strings"
	"time"

	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/itp"
	"github.com/chuckjaz/invariant-go/internal/notify"
	"github.com/chuckjaz/invariant-go/internal/slots"
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
	var enableLog bool
	flag.BoolVar(&enableLog, "log", true, "Enable logging output")
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
		id = generateID()
	}

	var s slots.Slots
	if dir != "" {
		fss, err := slots.NewFileSystemSlots(dir, snapshotInterval)
		if err != nil {
			fatalf("Failed to initialize file system slots: %v", err)
		}
		defer fss.Close()
		s = fss
	} else {
		s = slots.NewMemorySlots(id)
	}

	keyID := s.ID()
	if dir == "" && explicitID == "" {
		keyID = "slots"
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

	server := slots.NewServer(s)
	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
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

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- http.Serve(listener, server)
	}()

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
			fatalf("Failed to register with discovery service: %v", err)
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
		fatalf("Cannot register name without a valid discovery service")
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
				fatalf("Could not resolve notify name/id %s: %v", nid, err)
				continue
			}

			desc, ok := dClient.Get(context.Background(), resolvedID)
			if !ok {
				fatalf("Could not find address for Notify service %s", resolvedID)
				continue
			}

			notifyClients = append(notifyClients, notify.NewClient(desc.Address, httpClient))
		}
	} else if notifyIDs != "" {
		fatalf("a discovery service is required to use the -notify flag")
	}

	if len(notifyClients) > 0 {
		server.StartNotification(context.Background(), notifyClients, notifyBatchSize, notifyBatchDuration)
	}

	fatal(<-serveErr)
}
