package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"invariant/internal/discovery"
	"invariant/internal/distribute"
	"invariant/internal/identity"
	"invariant/internal/notify"
	"invariant/internal/storage"
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
	var s3Bucket string
	flag.StringVar(&s3Bucket, "s3-bucket", "", "AWS S3 bucket name for storage")
	var s3Prefix string
	flag.StringVar(&s3Prefix, "s3-prefix", "", "AWS S3 prefix for storage keys")
	var discoveryURL string
	flag.StringVar(&discoveryURL, "discovery", "", "URL of the discovery service")
	var advertiseAddr string
	flag.StringVar(&advertiseAddr, "advertise", "", "Address to advertise to the discovery service")
	var distributeArg string
	flag.StringVar(&distributeArg, "distribute", "", "ID or Name of the distribute service to register with")
	var notifyIDs string
	flag.StringVar(&notifyIDs, "notify", "", "Comma-separated list of IDs implementing the Notify protocol")
	var notifyBatchSize int
	flag.IntVar(&notifyBatchSize, "notify-batch-size", 10000, "Number of block addresses to send per request")
	var notifyBatchDuration time.Duration
	flag.DurationVar(&notifyBatchDuration, "notify-duration", 1*time.Second, "Maximum duration to wait before sending a batch of new block notifications")
	var port int
	flag.IntVar(&port, "port", 0, "Port to listen on (0 for random available port)")
	var name string
	flag.StringVar(&name, "name", "", "Name to register with the names service")
	flag.Parse()

	var s storage.Storage
	if s3Bucket != "" {
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

	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen on %s: %v", addr, err)
	}

	actualPort := listener.Addr().(*net.TCPAddr).Port

	var dClient *discovery.Client
	if discoveryURL != "" {
		id := s.(identity.Identity).ID()
		dClient = discovery.NewClient(discoveryURL, nil)

		// Configure the storage server to use discovery for fetching
		server.WithDiscovery(dClient)

		err := discovery.AdvertiseAndRegister(context.Background(), dClient, id, advertiseAddr, actualPort, []string{"storage-v1"})
		if err != nil {
			log.Fatalf("Failed to register with discovery service: %v", err)
		}
		log.Printf("Registered with discovery service %s as %s", discoveryURL, id)

		if name != "" {
			go func() {
				err := discovery.RegisterName(context.Background(), dClient, name, id, []string{"storage-v1"})
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

			notifyClients = append(notifyClients, notify.NewClient(desc.Address, nil))
		}
	}

	if distributeArg != "" {
		if discoveryURL == "" {
			log.Fatalf("Discovery service is required to use the -distribute flag")
		}

		dClient := discovery.NewClient(discoveryURL, nil)
		var distID string

		// If it's a 64-character hex string, it's an ID. Otherwise, resolve it via names service.
		distID, err = resolveWithRetry(dClient, distributeArg, 5, 2*time.Second)
		if err != nil {
			log.Fatalf("Warning: Could not resolve distribute name %v", err)
		}

		desc, ok := dClient.Get(context.Background(), distID)
		if !ok {
			log.Fatalf("Could not find distribute service %s in discovery", distID)
		}

		distClient := distribute.NewClient(desc.Address, nil)
		id := s.(identity.Identity).ID()
		if err := distClient.Register(id); err != nil {
			log.Fatalf("Failed to register with distribute service %s: %v", distID, err)
		}
		log.Printf("Registered with distribute service %s at %s", distID, desc.Address)

		notifyClients = append(notifyClients, notify.NewClient(desc.Address, nil))
	}

	if len(notifyClients) > 0 {
		server.StartNotification(context.Background(), notifyClients, notifyBatchSize, notifyBatchDuration)
	}

	log.Printf("Listening on :%d...", actualPort)
	if s3Bucket != "" {
		log.Printf("Using S3 storage at bucket %s (prefix: %s)", s3Bucket, s3Prefix)
	} else if dir != "" {
		log.Printf("Using File System storage at %s", dir)
	} else {
		log.Printf("Using In-Memory storage")
	}
	log.Fatal(http.Serve(listener, server))
}
