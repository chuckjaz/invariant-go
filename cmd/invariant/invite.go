package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chuckjaz/invariant-go/internal/config"
	"github.com/chuckjaz/invariant-go/internal/discovery"
	"github.com/chuckjaz/invariant-go/internal/itp"
)

func runInvite(cfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant invite <subcommand> [options]\n\n")
		fmt.Fprintf(os.Stderr, "Subcommands:\n")
		fmt.Fprintf(os.Stderr, "  create <name>      Create a single-use invitation code (--role user|service|admin)\n")
		fmt.Fprintf(os.Stderr, "  redeem <invite>    Redeem an invitation code and save the resulting access token\n")
		fmt.Fprintf(os.Stderr, "  list               List invitations\n")
		fmt.Fprintf(os.Stderr, "  revoke <code|id>   Revoke an unused invitation\n")
		os.Exit(1)
	}

	switch args[0] {
	case "create":
		runInviteCreate(cfg, args[1:])
	case "redeem":
		runInviteRedeem(cfg, args[1:])
	case "list", "ls":
		runInviteList(cfg, args[1:])
	case "revoke", "rm", "delete":
		runInviteRevoke(cfg, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand %q\n", args[0])
		os.Exit(1)
	}
}

func runToken(cfg *config.InvariantConfig, args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant token <subcommand> [options]\n\n")
		fmt.Fprintf(os.Stderr, "Subcommands:\n")
		fmt.Fprintf(os.Stderr, "  list               List active and revoked access tokens\n")
		fmt.Fprintf(os.Stderr, "  revoke <token|id>  Revoke an access token and evict its WireGuard peer key\n")
		os.Exit(1)
	}

	switch args[0] {
	case "list", "ls":
		runTokenList(cfg, args[1:])
	case "revoke", "rm", "delete":
		runTokenRevoke(cfg, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand %q\n", args[0])
		os.Exit(1)
	}
}

func runInviteCreate(cfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("invite create", flag.ExitOnError)
	var roleStr string
	fs.StringVar(&roleStr, "role", "user", "Role to grant upon redemption: user, service, or admin")
	var nameFlag string
	fs.StringVar(&nameFlag, "name", "", "Name for the invited user or service")
	var emailFlag string
	fs.StringVar(&emailFlag, "email", "", "Email address for user invitations (must match OAuth2 email on login)")
	var ttl time.Duration
	fs.DurationVar(&ttl, "ttl", discovery.DefaultInvitationTTL, "Invitation expiration duration (e.g. 15m, 1h)")
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL")
	_ = fs.Parse(args)

	name := strings.TrimSpace(nameFlag)
	email := strings.TrimSpace(emailFlag)
	nameArgs := fs.Args()
	if name == "" && len(nameArgs) > 0 {
		name = strings.TrimSpace(nameArgs[0])
	}
	if name == "" && email != "" {
		name = email
	}
	if name == "" {
		fmt.Fprintf(os.Stderr, "Usage: invariant invite create [--role user|service|admin] [--name <name>] [--email <email>] [--ttl 15m] [<name>]\n")
		os.Exit(1)
	}

	role := discovery.TokenRole(strings.ToLower(strings.TrimSpace(roleStr)))
	if !discovery.ValidRole(role) {
		fmt.Fprintf(os.Stderr, "Invalid role %q: must be user, service, or admin\n", roleStr)
		os.Exit(1)
	}

	if email == "" && role != discovery.RoleService && strings.Contains(name, "@") {
		email = name
	}
	if role == discovery.RoleUser && email == "" {
		fmt.Fprintf(os.Stderr, "Error: --email is required when creating a user invitation\n")
		os.Exit(1)
	}

	if discoveryURL != "" {
		cfg.Discovery = discoveryURL
	}

	client, _ := getAuthenticatedClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	inv, err := client.CreateInvitation(ctx, name, role, discovery.NodeIdentity{}, ttl, email)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create invitation: %v\n", err)
		os.Exit(1)
	}

	invCode := discovery.FormatInvitationCode(inv.Code, cfg.Discovery)
	fmt.Println("Created single-use invitation successfully!")
	fmt.Printf("  ID:            %s\n", inv.ID)
	fmt.Printf("  Name:          %s\n", inv.Name)
	if inv.Email != "" {
		fmt.Printf("  Email:         %s\n", inv.Email)
	}
	fmt.Printf("  Role:          %s\n", inv.Role)
	if inv.ControlPlaneURL != "" {
		fmt.Printf("  Control Plane: %s\n", inv.ControlPlaneURL)
	}
	fmt.Printf("  Invitation:    %s\n", invCode)
	fmt.Printf("  Created At:    %s\n", inv.CreatedAt.Format(time.RFC3339))
	fmt.Printf("  Expires At:    %s\n", inv.ExpiresAt.Format(time.RFC3339))
	if inv.Role == discovery.RoleService {
		fmt.Printf("\nProvision the service with:\n  --provision %s\n", invCode)
	} else {
		fmt.Printf("\nRedeem with:\n  invariant login --invite %s\n", invCode)
	}
}

func runInviteRedeem(cfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("invite redeem", flag.ExitOnError)
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL")
	var nodeName string
	fs.StringVar(&nodeName, "name", "", "Node/user name")
	var manualToken string
	fs.StringVar(&manualToken, "token", "", "Manually specify an OAuth access token")
	var userInfoURL string
	fs.StringVar(&userInfoURL, "userinfo-url", "", "OAuth UserInfo URL")
	_ = fs.Parse(args)

	rem := fs.Args()
	if len(rem) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant invite redeem [--discovery <url>] [--name <name>] <code-or-file>\n")
		os.Exit(1)
	}

	runInviteRedeemFlowWithOAuth(cfg, rem[0], discoveryURL, nodeName, oauthLoginOptions{
		manualToken: manualToken,
		userInfoURL: userInfoURL,
		reuseSaved:  true,
	})
}

func runInviteRedeemFlow(cfg *config.InvariantConfig, inviteCodeOrFile, discoveryURL, nodeName string) {
	runInviteRedeemFlowWithOAuth(cfg, inviteCodeOrFile, discoveryURL, nodeName, oauthLoginOptions{
		reuseSaved: true,
	})
}

func runInviteRedeemFlowWithOAuth(cfg *config.InvariantConfig, inviteCodeOrFile, discoveryURL, nodeName string, oauthOpts oauthLoginOptions) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	rec, tokenPath, err := redeemInvitationWithOAuth(ctx, cfg, inviteCodeOrFile, discoveryURL, nodeName, oauthOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to redeem invitation: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Invitation redeemed successfully!")
	fmt.Printf("  Token ID:   %s\n", rec.ID)
	fmt.Printf("  Name:       %s\n", rec.Name)
	if rec.Email != "" {
		fmt.Printf("  Email:      %s\n", rec.Email)
	}
	fmt.Printf("  Role:       %s\n", rec.Role)
	fmt.Printf("  Public Key: %s\n", rec.PublicKey)
	fmt.Printf("  Saved To:   %s\n", tokenPath)
}

func redeemInvitationWithOAuth(ctx context.Context, cfg *config.InvariantConfig, inviteCodeOrFile, discoveryURL, nodeName string, oauthOpts oauthLoginOptions) (*discovery.AccessTokenRecord, string, error) {
	code := strings.TrimSpace(inviteCodeOrFile)
	if !strings.HasPrefix(code, "inv_invite_") {
		if data, err := os.ReadFile(code); err == nil {
			code = strings.TrimSpace(string(data))
		}
	}
	if code == "" {
		return nil, "", fmt.Errorf("invitation code is empty")
	}

	// Validate OAuth credentials first so we have a verified user email before redeeming the invitation
	tf, ident, err := ensureValidatedOAuth(ctx, oauthOpts)
	if err != nil {
		return nil, "", fmt.Errorf("OAuth authentication required to redeem invitation: %w", err)
	}

	_, embeddedURL := discovery.ParseInvitationCode(code)
	if discoveryURL == "" && embeddedURL != "" {
		discoveryURL = embeddedURL
	}
	if discoveryURL == "" && cfg != nil {
		discoveryURL = cfg.Discovery
	}
	if discoveryURL == "" {
		discoveryURL = "http://127.0.0.1:3003"
	}

	if nodeName == "" {
		nodeName = ident.Name
	}
	if nodeName == "" {
		nodeName = os.Getenv("USER")
		if nodeName == "" {
			nodeName = "cli"
		}
	}

	pk := getClientKey()
	derpMapURL := ""
	if cfg != nil {
		derpMapURL = cfg.DERPMapURL
	}
	httpClient := itp.NewHTTPClient(pk.Private, tf.AccessToken, itp.WithDERPMapURL(derpMapURL))
	client := discovery.NewClient(discoveryURL, httpClient).WithUserToken(tf.AccessToken)

	redeemCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	rec, err := client.RedeemInvitation(redeemCtx, code, pk.Private.Public().String(), discovery.NodeIdentity{
		Name:  nodeName,
		Email: ident.Email,
	})
	if err != nil {
		return nil, "", err
	}

	if rec.Email != "" && !strings.EqualFold(strings.TrimSpace(rec.Email), strings.TrimSpace(ident.Email)) {
		return nil, "", fmt.Errorf("%w: invitation email %q does not match OAuth email %q", discovery.ErrInvitationEmailMismatch, rec.Email, ident.Email)
	}

	tokenPath := itp.DefaultTokenPath("client")
	if err := itp.SaveAccessToken(tokenPath, rec.Token); err != nil {
		return nil, "", fmt.Errorf("error saving access token to %s: %w", tokenPath, err)
	}
	cpURL := rec.ControlPlaneURL
	if cpURL == "" {
		cpURL = discoveryURL
	}
	if cpURL != "" {
		_ = itp.SaveAccessToken(itp.DefaultDiscoveryURLPath("client"), cpURL)
	}

	return rec, tokenPath, nil
}

func runInviteList(cfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("invite list", flag.ExitOnError)
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL")
	_ = fs.Parse(args)

	if discoveryURL != "" {
		cfg.Discovery = discoveryURL
	}

	client, _ := getAuthenticatedClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	invites, err := client.ListInvitations(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list invitations: %v\n", err)
		os.Exit(1)
	}

	if len(invites) == 0 {
		fmt.Println("No invitations found.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tROLE\tCREATED AT\tEXPIRES AT\tSTATUS")
	for _, inv := range invites {
		status := "Active"
		if inv.Revoked {
			status = "Revoked"
		} else if inv.Used {
			status = "Redeemed"
		} else if time.Now().After(inv.ExpiresAt) {
			status = "Expired"
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			inv.ID, inv.Name, inv.Role,
			inv.CreatedAt.Format("2006-01-02 15:04"),
			inv.ExpiresAt.Format("2006-01-02 15:04"),
			status)
	}
	w.Flush()
}

func runInviteRevoke(cfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("invite revoke", flag.ExitOnError)
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL")
	_ = fs.Parse(args)

	rem := fs.Args()
	if len(rem) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant invite revoke <code-or-id>\n")
		os.Exit(1)
	}
	target := rem[0]

	if discoveryURL != "" {
		cfg.Discovery = discoveryURL
	}

	client, _ := getAuthenticatedClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.RevokeInvitation(ctx, target, discovery.NodeIdentity{}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to revoke invitation %q: %v\n", target, err)
		os.Exit(1)
	}

	fmt.Printf("Invitation %q revoked successfully.\n", target)
}

func runTokenList(cfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("token list", flag.ExitOnError)
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL")
	_ = fs.Parse(args)

	if discoveryURL != "" {
		cfg.Discovery = discoveryURL
	}

	client, _ := getAuthenticatedClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tokens, err := client.ListAccessTokens(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list access tokens: %v\n", err)
		os.Exit(1)
	}

	if len(tokens) == 0 {
		fmt.Println("No access tokens found.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tROLE\tPUBLIC KEY\tCREATED AT\tSTATUS")
	for _, tok := range tokens {
		status := "Active"
		if tok.Revoked {
			status = "Revoked"
		} else if tok.ExpiresAt != nil && time.Now().After(*tok.ExpiresAt) {
			status = "Expired"
		}

		pubKey := tok.PublicKey
		if len(pubKey) > 20 {
			pubKey = pubKey[:20] + "..."
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			tok.ID, tok.Name, tok.Role, pubKey,
			tok.CreatedAt.Format("2006-01-02 15:04"),
			status)
	}
	w.Flush()
}

func runTokenRevoke(cfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("token revoke", flag.ExitOnError)
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL")
	_ = fs.Parse(args)

	rem := fs.Args()
	if len(rem) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: invariant token revoke <token-or-id>\n")
		os.Exit(1)
	}
	target := rem[0]

	if discoveryURL != "" {
		cfg.Discovery = discoveryURL
	}

	client, _ := getAuthenticatedClient(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.RevokeAccessToken(ctx, target, discovery.NodeIdentity{}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to revoke access token %q: %v\n", target, err)
		os.Exit(1)
	}

	fmt.Printf("Access token %q revoked and WireGuard peer evicted successfully.\n", target)
}
