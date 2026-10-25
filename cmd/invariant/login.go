package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chuckjaz/invariant-go/internal/config"
	"github.com/chuckjaz/invariant-go/internal/discovery"
)

// OAuthTokenFile stores credentials saved to ~/.invariant/oauth_token.json.
type OAuthTokenFile struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry"`
	Name         string    `json:"name,omitempty"`
	Email        string    `json:"email,omitempty"`
}

// OAuthTokenPath returns the file path for storing the OAuth user token.
func OAuthTokenPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "oauth_token.json"), nil
}

// SaveOAuthToken writes token data to ~/.invariant/oauth_token.json.
func SaveOAuthToken(t *OAuthTokenFile) error {
	path, err := OAuthTokenPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// LoadOAuthTokenFile reads the full OAuthTokenFile from ~/.invariant/oauth_token.json
// or the INVARIANT_OAUTH_TOKEN environment variable.
func LoadOAuthTokenFile() (*OAuthTokenFile, error) {
	if env := strings.TrimSpace(os.Getenv("INVARIANT_OAUTH_TOKEN")); env != "" {
		return &OAuthTokenFile{
			AccessToken: env,
			TokenType:   "Bearer",
		}, nil
	}
	path, err := OAuthTokenPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t OAuthTokenFile
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// LoadOAuthToken reads the OAuth access token from ~/.invariant/oauth_token.json,
// or INVARIANT_OAUTH_TOKEN environment variable.
func LoadOAuthToken() (string, error) {
	t, err := LoadOAuthTokenFile()
	if err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

const (
	defaultGoogleClientID     = "624751386960-o472j5n2rhib1odebc13lkqeptddp6dt.apps.googleusercontent.com"
	defaultGoogleClientSecret = "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"
	defaultGoogleAuthURL      = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultGoogleTokenURL     = "https://oauth2.googleapis.com/token"
	defaultGoogleUserInfoURL  = "https://openidconnect.googleapis.com/v1/userinfo"
)

type oauthLoginOptions struct {
	clientID     string
	clientSecret string
	authURL      string
	tokenURL     string
	userInfoURL  string
	manualToken  string
	httpClient   *http.Client
}

func resolveOAuthOptions(opts oauthLoginOptions) oauthLoginOptions {
	if opts.clientID == "" {
		opts.clientID = os.Getenv("INVARIANT_OAUTH_CLIENT_ID")
		if opts.clientID == "" {
			opts.clientID = defaultGoogleClientID
		}
	}
	if opts.clientSecret == "" {
		opts.clientSecret = os.Getenv("INVARIANT_OAUTH_CLIENT_SECRET")
		if opts.clientSecret == "" {
			opts.clientSecret = defaultGoogleClientSecret
		}
	}
	if opts.authURL == "" {
		opts.authURL = os.Getenv("INVARIANT_OAUTH_AUTH_URL")
		if opts.authURL == "" {
			opts.authURL = defaultGoogleAuthURL
		}
	}
	if opts.tokenURL == "" {
		opts.tokenURL = os.Getenv("INVARIANT_OAUTH_TOKEN_URL")
		if opts.tokenURL == "" {
			opts.tokenURL = defaultGoogleTokenURL
		}
	}
	if opts.userInfoURL == "" {
		opts.userInfoURL = os.Getenv("INVARIANT_OAUTH_USERINFO_URL")
		if opts.userInfoURL == "" {
			opts.userInfoURL = defaultGoogleUserInfoURL
		}
	}
	return opts
}

func validateOAuthCredentials(ctx context.Context, accessToken, userInfoURL string, httpClient *http.Client) (*discovery.NodeIdentity, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("empty OAuth access token")
	}
	if userInfoURL == "" {
		userInfoURL = os.Getenv("INVARIANT_OAUTH_USERINFO_URL")
		if userInfoURL == "" {
			userInfoURL = defaultGoogleUserInfoURL
		}
	}
	auth := discovery.NewUserInfoAuthenticator(userInfoURL, httpClient, 5*time.Minute)
	ident, err := auth.Authenticate(ctx, accessToken)
	if err != nil {
		return nil, fmt.Errorf("OAuth credential validation failed: %w", err)
	}
	if ident == nil || strings.TrimSpace(ident.Email) == "" {
		return nil, fmt.Errorf("OAuth provider did not return a verified email address")
	}
	return ident, nil
}

func performOAuthLogin(ctx context.Context, opts oauthLoginOptions) (*OAuthTokenFile, *discovery.NodeIdentity, error) {
	opts = resolveOAuthOptions(opts)

	// 1. Explicit manual token
	if strings.TrimSpace(opts.manualToken) != "" {
		ident, err := validateOAuthCredentials(ctx, opts.manualToken, opts.userInfoURL, opts.httpClient)
		if err != nil {
			return nil, nil, err
		}
		tf := &OAuthTokenFile{
			AccessToken: strings.TrimSpace(opts.manualToken),
			TokenType:   "Bearer",
			Name:        ident.Name,
			Email:       ident.Email,
		}
		if err := SaveOAuthToken(tf); err != nil {
			return nil, nil, fmt.Errorf("error saving OAuth token: %w", err)
		}
		return tf, ident, nil
	}

	// 2. Obtain fresh OAuth token via browser flow
	if opts.authURL == "" || opts.tokenURL == "" {
		return nil, nil, fmt.Errorf("no OAuth authorization server configured (--auth-url / INVARIANT_OAUTH_AUTH_URL)")
	}
	tf, err := obtainBrowserOAuthToken(opts.clientID, opts.clientSecret, opts.authURL, opts.tokenURL)
	if err != nil {
		return nil, nil, err
	}

	ident, err := validateOAuthCredentials(ctx, tf.AccessToken, opts.userInfoURL, opts.httpClient)
	if err != nil {
		return nil, nil, err
	}
	tf.Email = ident.Email
	if tf.Name == "" {
		tf.Name = ident.Name
	}
	if err := SaveOAuthToken(&tf); err != nil {
		return nil, nil, fmt.Errorf("error saving OAuth token: %w", err)
	}
	return &tf, ident, nil
}

func requireLoggedInOAuth(ctx context.Context, opts oauthLoginOptions) (*OAuthTokenFile, *discovery.NodeIdentity, error) {
	opts = resolveOAuthOptions(opts)

	if strings.TrimSpace(opts.manualToken) != "" {
		ident, err := validateOAuthCredentials(ctx, opts.manualToken, opts.userInfoURL, opts.httpClient)
		if err != nil {
			return nil, nil, err
		}
		tf := &OAuthTokenFile{
			AccessToken: strings.TrimSpace(opts.manualToken),
			TokenType:   "Bearer",
			Name:        ident.Name,
			Email:       ident.Email,
		}
		return tf, ident, nil
	}

	tf, err := LoadOAuthTokenFile()
	if err != nil || tf == nil || strings.TrimSpace(tf.AccessToken) == "" {
		return nil, nil, fmt.Errorf("not logged in: run 'invariant login' first to obtain OAuth credentials")
	}

	tok := strings.TrimSpace(tf.AccessToken)
	if strings.HasPrefix(tok, "inv_at_") {
		return nil, nil, fmt.Errorf("not logged in with OAuth: run 'invariant login' first")
	}
	if !tf.Expiry.IsZero() && time.Now().After(tf.Expiry) {
		return nil, nil, fmt.Errorf("OAuth credentials have expired: run 'invariant login' first")
	}

	ident, err := validateOAuthCredentials(ctx, tok, opts.userInfoURL, opts.httpClient)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to validate saved OAuth credentials (run 'invariant login' again): %w", err)
	}
	tf.Email = ident.Email
	if tf.Name == "" {
		tf.Name = ident.Name
	}
	_ = SaveOAuthToken(tf)
	return tf, ident, nil
}

// openBrowser launches the default system browser for the given URL.
func openBrowser(targetURL string) error {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", targetURL).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL).Start()
	case "darwin":
		err = exec.Command("open", targetURL).Start()
	default:
		err = fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return err
}

func runLogin(cfg *config.InvariantConfig, args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	var manualToken string
	fs.StringVar(&manualToken, "token", "", "Manually specify an OAuth access token")
	var clientID string
	fs.StringVar(&clientID, "client-id", "", "OAuth Client ID")
	var clientSecret string
	fs.StringVar(&clientSecret, "client-secret", "", "OAuth Client Secret")
	var authURL string
	fs.StringVar(&authURL, "auth-url", "", "OAuth Authorization URL")
	var tokenURL string
	fs.StringVar(&tokenURL, "token-url", "", "OAuth Token URL")
	var userInfoURL string
	fs.StringVar(&userInfoURL, "userinfo-url", "", "OAuth UserInfo URL")

	_ = fs.Parse(args)

	oauthOpts := oauthLoginOptions{
		clientID:     clientID,
		clientSecret: clientSecret,
		authURL:      authURL,
		tokenURL:     tokenURL,
		userInfoURL:  userInfoURL,
		manualToken:  manualToken,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	tf, ident, err := performOAuthLogin(ctx, oauthOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
		os.Exit(1)
	}

	path, _ := OAuthTokenPath()
	fmt.Printf("\nLogin successful! Verified OAuth user %s (%s). Credentials saved to %s\n", ident.Name, tf.Email, path)
}

type oauthTokenResponse struct {
	OAuthTokenFile
	ExpiresIn int `json:"expires_in"`
}

func (r *oauthTokenResponse) toTokenFile() OAuthTokenFile {
	tf := r.OAuthTokenFile
	if tf.Expiry.IsZero() && r.ExpiresIn > 0 {
		tf.Expiry = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	return tf
}

func obtainBrowserOAuthToken(clientID, clientSecret, authURL, tokenURL string) (OAuthTokenFile, error) {
	// PKCE
	verifierBytes := make([]byte, 32)
	rand.Read(verifierBytes)
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])

	stateBytes := make([]byte, 16)
	rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	// Listen on dynamic localhost port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return OAuthTokenFile{}, fmt.Errorf("failed to start local listener: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d", port)

	params := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid https://www.googleapis.com/auth/userinfo.email"},
		"state":                 {state},
		"access_type":           {"offline"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	authReqURL := fmt.Sprintf("%s?%s", authURL, params.Encode())

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "State mismatch", http.StatusBadRequest)
			errCh <- fmt.Errorf("state mismatch")
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "Missing authorization code", http.StatusBadRequest)
			errCh <- fmt.Errorf("missing code: %s", q.Get("error"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h1>Authentication Successful!</h1><p>You can now close this browser window and head back to the terminal.</p></body></html>")
		codeCh <- code
	})

	srv := &http.Server{Handler: mux}
	go srv.Serve(listener)
	defer srv.Shutdown(context.Background())

	fmt.Printf("Opening system browser to authenticate...\n")
	if err := openBrowser(authReqURL); err != nil {
		fmt.Printf("Could not open browser automatically. Please click or visit:\n\n  %s\n\n", authReqURL)
	} else {
		fmt.Printf("If the browser did not open automatically, please visit:\n\n  %s\n\n", authReqURL)
	}
	fmt.Printf("Waiting for authorization callback on :%d...\n", port)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return OAuthTokenFile{}, fmt.Errorf("authentication failed: %w", err)
	case <-time.After(3 * time.Minute):
		return OAuthTokenFile{}, fmt.Errorf("login timed out after 3 minutes")
	}

	// Exchange code for token
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {clientID},
		"code_verifier": {verifier},
	}
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return OAuthTokenFile{}, fmt.Errorf("token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return OAuthTokenFile{}, fmt.Errorf("token exchange returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return OAuthTokenFile{}, fmt.Errorf("failed to decode token response: %w", err)
	}

	return tokenResp.toTokenFile(), nil
}
