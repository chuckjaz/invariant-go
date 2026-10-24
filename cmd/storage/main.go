package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/distribute"
	"github.com/chuckjaz/invariant-go/internal/identity"
	"github.com/chuckjaz/invariant-go/internal/itp"
	"github.com/chuckjaz/invariant-go/internal/kv"
	"github.com/chuckjaz/invariant-go/internal/notify"
	"github.com/chuckjaz/invariant-go/internal/storage"
	gitstorage "github.com/chuckjaz/invariant-go/internal/storage/git"
	"github.com/chuckjaz/invariant-go/internal/tags"
	"github.com/chuckjaz/invariant-go/internal/trace"
)

func resolveWithRetry(dClient *discovery.Client, name string, retries int, delay time.Duration) (string, error) {
	var id string
	var err error
	for i := range retries {
		id, err = discovery.ResolveName(context.Background(), dClient, name)
		if err == nil {
			return id, nil
		}
		if i < retries-1 {
			time.Sleep(delay)
		}
	}
	return "", err
}

func main() {
	var dir string
	flag.StringVar(&dir, "dir", "", "Base directory for file system storage")
	var gitDir string
	flag.StringVar(&gitDir, "git-dir", "", "Path to a local Git repository to serve blobs from directly")
	var gitScanCommit string
	flag.StringVar(&gitScanCommit, "git-scan-commit", "HEAD", "Git commit to scan on startup when using -git-dir")
	var gitScanDepth int
	flag.IntVar(&gitScanDepth, "git-scan-depth", -1, "Depth of commit history to scan on startup (-1 for full history)")
	var gitScanConcurrency int
	flag.IntVar(&gitScanConcurrency, "git-scan-concurrency", 20, "Concurrency for scanning git repository on startup")
	var s3Bucket string
	flag.StringVar(&s3Bucket, "s3-bucket", "", "AWS S3 bucket name for storage")
	var s3Prefix string
	flag.StringVar(&s3Prefix, "s3-prefix", "", "AWS S3 prefix for storage keys")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	tagFlags := tags.RegisterFlags()
	var distributeArg string
	flag.StringVar(&distributeArg, "distribute", "", "ID or Name of the distribute service to register with")
	var notifyIDs string
	flag.StringVar(&notifyIDs, "notify", "", "Comma-separated list of IDs implementing the Notify protocol")
	var notifyBatchSize int
	flag.IntVar(&notifyBatchSize, "notify-batch-size", 10000, "Number of block addresses to send per request")
	var notifyBatchDuration time.Duration
	flag.DurationVar(&notifyBatchDuration, "notify-duration", 1*time.Second, "Maximum duration to wait before sending a batch of new block notifications")
	var listenIP bool
	flag.BoolVar(&listenIP, "listen-ip", false, "Listen on a host IP port instead of using ITP (Tailcat)")
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on when -listen-ip is enabled (0 for random available port)")
	var serviceKey string
	flag.StringVar(&serviceKey, "service-key", "", "Access token or service key for authenticating with discovery / control plane (defaults to INVARIANT_ACCESS_TOKEN or INVARIANT_SERVICE_KEY)")
	var inviteCode string
	flag.StringVar(&inviteCode, "invite", "", "One-time invitation code to redeem for a discovery access token (defaults to INVARIANT_INVITE)")
	var tokenFile string
	flag.StringVar(&tokenFile, "token-file", "", "Path to persist redeemed discovery access token (defaults to ~/.invariant/keys/<service-id>.token)")
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

	var s storage.Storage
	if gitDir != "" {
		var kvClient kv.BatchKeyValueStore
		if discoveryURL != "" {
			dClient := discovery.NewClient(discoveryURL, nil)
			if serviceKey != "" {
				dClient = dClient.WithServiceKey(serviceKey)
			}
			kvAddr, err := discovery.ResolveName(context.Background(), dClient, "kv-v1")
			if err != nil || kvAddr == "" {
				ids, _ := dClient.Find(context.Background(), "kv-v1", "", 1)
				if len(ids) > 0 {
					kvAddr = ids[0].Address
				}
			}
			if kvAddr != "" {
				kvClient = kv.NewClient(kvAddr, nil)
			}
		}
		if kvClient == nil {
			kvClient = kv.NewMemoryKeyValueStore()
		}

		var err error
		s, err = gitstorage.NewGitStorage(gitDir, kvClient, gitstorage.GitStorageOptions{
			ScanCommit:      gitScanCommit,
			ScanDepth:       gitScanDepth,
			ScanConcurrency: gitScanConcurrency,
		})
		if err != nil {
			log.Fatalf("Failed to initialize Git storage: %v", err)
		}
	} else if s3Bucket != "" {
		var err error
		s, err = storage.NewS3Storage(context.Background(), s3Bucket, s3Prefix)
		if err != nil {
			log.Fatalf("Failed to initialize S3 storage: %v", err)
		}
	} else if dir != "" {
		s = storage.NewFileSystemStorage(dir)
	} else {
		s = storage.NewInMemoryStorage()
	}

	server := storage.NewStorageServer(s)
	id := s.(identity.Identity).ID()

	if keyFile == "" {
		keyFile = itp.DefaultKeyPath(id)
	}
	privKey, err := itp.LoadOrGenerateKey(keyFile)
	if err != nil {
		log.Fatalf("Failed to load or generate ITP key: %v", err)
	}

	resolvedToken, err := itp.ResolveOrRedeemToken(context.Background(), itp.ServiceAuthOptions{
		DiscoveryURL: discoveryURL,
		ServiceID:    id,
		ServiceName:  name,
		ExplicitKey:  serviceKey,
		InviteCode:   inviteCode,
		TokenFile:    tokenFile,
		PrivateKey:   privKey,
		DERPMapURL:   derpMapURL,
	})
	if err != nil {
		log.Fatalf("Failed to authenticate with discovery service: %v", err)
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
		log.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	if dClient != nil {
		nodeIdent := discovery.NodeIdentity{
			Name: name,
		}
		_ = dClient.RegisterClient(context.Background(), privKey.Private.Public().String(), nodeIdent)

		// Configure the storage server to use discovery for fetching
		server.WithDiscovery(dClient)

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
			[]string{"storage-v1", "batch-storage-v1"},
			tagFlags.Tags(),
			discovery.WithPublicKey(privKey.Private.Public().String()),
			discovery.WithIdentity(nodeIdent),
		)
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, id)

		if name != "" {
			go func() {
				err := discovery.RegisterName(context.Background(), dClient, name, id, []string{"storage-v1", "batch-storage-v1"})
				if err != nil {
					log.Printf("Failed to register name %q: %v", name, err)
				} else {
					log.Printf("Registered name %q for ID %s", name, id)
				}
			}()
		}
	} else if name != "" {
		log.Fatalf("a discovery service with a registered names service is required for the service to be named.")
	}

	var notifyClients []storage.NotifyClient
	if dClient != nil {
		for hid := range strings.SplitSeq(notifyIDs, ",") {
			hid = strings.TrimSpace(hid)
			if hid == "" {
				continue
			}

			hid, err = resolveWithRetry(dClient, hid, 5, 2*time.Second)
			if err != nil {
				log.Fatalf("Could not resolve notify name/id %s: %v", hid, err)
				continue
			}

			desc, ok := dClient.Get(context.Background(), hid)
			if !ok {
				log.Fatalf("Could not find address for Notify service ID %s", hid)
				continue
			}

			notifyClients = append(notifyClients, notify.NewClient(desc.Address, httpClient))
		}
	}

	if distributeArg != "" {
		if discoveryURL == "" {
			log.Fatalf("Discovery service is required to use the -distribute flag")
		}

		// If it's a 64-character hex string, it's an ID. Otherwise, resolve it via names service.
		distID, err := resolveWithRetry(dClient, distributeArg, 5, 2*time.Second)
		if err != nil {
			log.Fatalf("Warning: Could not resolve distribute name %v", err)
		}

		desc, ok := dClient.Get(context.Background(), distID)
		if !ok {
			log.Fatalf("Could not find distribute service %s in discovery", distID)
		}

		distClient := distribute.NewClient(desc.Address, httpClient)
		if err := distClient.Register(id); err != nil {
			log.Fatalf("Failed to register with distribute service %s: %v", distID, err)
		}
		log.Printf("Registered with distribute service %s at %s", distID, desc.Address)

		notifyClients = append(notifyClients, notify.NewClient(desc.Address, httpClient))
	}

	if len(notifyClients) > 0 {
		server.StartNotification(context.Background(), notifyClients, notifyBatchSize, notifyBatchDuration)
	}

	if enableTrace {
		server.WithTracer(trace.NewTracer(10000))
	}

	if listenIP {
		log.Printf("Listening on :%d (HTTP)...", listener.Port())
	} else {
		log.Printf("Listening on %s (ITP Address: %s, Public Key: %s)...", listener.ITPAddr, listener.ITPAddr, privKey.Private.Public())
	}
	if s3Bucket != "" {
		log.Printf("Using S3 storage at bucket %s (prefix: %s)", s3Bucket, s3Prefix)
	} else if dir != "" {
		log.Printf("Using File System storage at %s", dir)
	} else {
		log.Printf("Using In-Memory storage")
	}
	log.Fatal(http.Serve(listener, server))
}
