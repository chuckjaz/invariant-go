package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/chuckjaz/invariant-go/internal/config"
	"github.com/chuckjaz/invariant-go/internal/discovery"
)

func runServiceKey(cfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant service-key <subcommand> [options]\n\n")
		fmt.Fprintf(os.Stderr, "Subcommands:\n")
		fmt.Fprintf(os.Stderr, "  create <name>   Create a new service key authorized by your user account\n")
		fmt.Fprintf(os.Stderr, "  list            List active service keys\n")
		fmt.Fprintf(os.Stderr, "  revoke <key>    Revoke a service key\n")
		os.Exit(1)
	}

	switch args[0] {
	case "create":
		runServiceKeyCreate(cfg, args[1:])
	case "list", "ls":
		runServiceKeyList(cfg, args[1:])
	case "revoke", "rm", "delete":
		runServiceKeyRevoke(cfg, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand %q\n", args[0])
		os.Exit(1)
	}
}

func getAuthenticatedClient(cfg *config.InvariantConfig) (*discovery.Client, string) {
	token := resolveUserToken(cfg)
	if token == "" {
		fmt.Fprintf(os.Stderr, "Error: You must be logged in to manage service keys.\nRun 'invariant login' first or set INVARIANT_OAUTH_TOKEN.\n")
		os.Exit(1)
	}

	discoveryURL := cfg.Discovery
	if discoveryURL == "" {
		discoveryURL = "http://127.0.0.1:3003"
	}

	client := newDiscoveryClient(discoveryURL, cfg)
	client.WithUserToken(token)
	return client, token
}

func runServiceKeyCreate(cfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("service-key create", flag.ExitOnError)
	var expiresStr string
	fs.StringVar(&expiresStr, "expires", "", "Expiration duration (e.g. 720h, 30d)")
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL")
	_ = fs.Parse(args)

	nameArgs := fs.Args()
	if len(nameArgs) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant service-key create <name> [--expires <duration>]\n")
		os.Exit(1)
	}
	name := nameArgs[0]

	if discoveryURL != "" {
		cfg.Discovery = discoveryURL
	}

	client, _ := getAuthenticatedClient(cfg)

	var expiresAt *time.Time
	if expiresStr != "" {
		dur, err := time.ParseDuration(expiresStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid expiration duration %q: %v\n", expiresStr, err)
			os.Exit(1)
		}
		t := time.Now().UTC().Add(dur)
		expiresAt = &t
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rec, err := client.CreateServiceKey(ctx, name, discovery.NodeIdentity{}, expiresAt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create service key: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Created service key successfully!")
	fmt.Printf("  ID:         %s\n", rec.ID)
	fmt.Printf("  Name:       %s\n", rec.Name)
	fmt.Printf("  Key:        %s\n", rec.Key)
	if rec.CreatedBy.Email != "" {
		fmt.Printf("  Created By: %s <%s>\n", rec.CreatedBy.Name, rec.CreatedBy.Email)
	} else if rec.CreatedBy.Name != "" {
		fmt.Printf("  Created By: %s\n", rec.CreatedBy.Name)
	}
	fmt.Printf("  Created At: %s\n", rec.CreatedAt.Format(time.RFC3339))
	if rec.ExpiresAt != nil {
		fmt.Printf("  Expires At: %s\n", rec.ExpiresAt.Format(time.RFC3339))
	}
	fmt.Println("\nIMPORTANT: Save this secret key now. It will not be shown again.")
}

func runServiceKeyList(cfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("service-key list", flag.ExitOnError)
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL")
	_ = fs.Parse(args)

	if discoveryURL != "" {
		cfg.Discovery = discoveryURL
	}

	client, _ := getAuthenticatedClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	keys, err := client.ListServiceKeys(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list service keys: %v\n", err)
		os.Exit(1)
	}

	if len(keys) == 0 {
		fmt.Println("No service keys found.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tKEY\tCREATED BY\tCREATED AT\tSTATUS")
	for _, k := range keys {
		status := "Active"
		if k.Revoked {
			status = "Revoked"
		} else if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
			status = "Expired"
		}

		creator := k.CreatedBy.Name
		if k.CreatedBy.Email != "" {
			creator = k.CreatedBy.Email
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			k.ID, k.Name, k.Key, creator, k.CreatedAt.Format("2006-01-02 15:04"), status)
	}
	w.Flush()
}

func runServiceKeyRevoke(cfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("service-key revoke", flag.ExitOnError)
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL")
	_ = fs.Parse(args)

	keyArgs := fs.Args()
	if len(keyArgs) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant service-key revoke <key-or-id>\n")
		os.Exit(1)
	}
	targetKey := keyArgs[0]

	if discoveryURL != "" {
		cfg.Discovery = discoveryURL
	}

	client, _ := getAuthenticatedClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.RevokeServiceKey(ctx, targetKey, discovery.NodeIdentity{}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to revoke service key %q: %v\n", targetKey, err)
		os.Exit(1)
	}

	fmt.Printf("Service key %q revoked successfully.\n", targetKey)
}
