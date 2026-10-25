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
	defaultGoogleDeviceURL    = "https://oauth2.googleapis.com/device/code"
	defaultGoogleUserInfoURL  = "https://openidconnect.googleapis.com/v1/userinfo"
)

type oauthLoginOptions struct {
	clientID     string
	clientSecret string
	authURL      string
	tokenURL     string
	deviceURL    string
	userInfoURL  string
	manualToken  string
	deviceFlow   bool
	reuseSaved   bool
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
	if opts.deviceURL == "" {
		opts.deviceURL = os.Getenv("INVARIANT_OAUTH_DEVICE_URL")
		if opts.deviceURL == "" {
			opts.deviceURL = defaultGoogleDeviceURL
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

func ensureValidatedOAuth(ctx context.Context, opts oauthLoginOptions) (*OAuthTokenFile, *discovery.NodeIdentity, error) {
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

	// 2. Reuse saved OAuth token if valid (when redeeming an invitation without forcing a new flow)
	if opts.reuseSaved && !opts.deviceFlow {
		if tf, err := LoadOAuthTokenFile(); err == nil && tf != nil {
			tok := strings.TrimSpace(tf.AccessToken)
			notExpired := tf.Expiry.IsZero() || time.Now().Before(tf.Expiry)
			if tok != "" && !strings.HasPrefix(tok, "inv_at_") && notExpired {
				if ident, err := validateOAuthCredentials(ctx, tok, opts.userInfoURL, opts.httpClient); err == nil {
					tf.Email = ident.Email
					if tf.Name == "" {
						tf.Name = ident.Name
					}
					_ = SaveOAuthToken(tf)
					return tf, ident, nil
				}
			}
		}
	}

	// 3. Obtain fresh OAuth token via device or browser flow
	var tf OAuthTokenFile
	var err error
	if opts.deviceFlow {
		if opts.deviceURL == "" || opts.tokenURL == "" {
			return nil, nil, fmt.Errorf("--device-url and --token-url are required for device flow (or set INVARIANT_OAUTH_DEVICE_URL)")
		}
		tf, err = obtainDeviceOAuthToken(opts.clientID, opts.clientSecret, opts.deviceURL, opts.tokenURL)
	} else {
		if opts.authURL == "" || opts.tokenURL == "" {
			return nil, nil, fmt.Errorf("no OAuth authorization server configured (--auth-url / INVARIANT_OAUTH_AUTH_URL)")
		}
		tf, err = obtainBrowserOAuthToken(opts.clientID, opts.clientSecret, opts.authURL, opts.tokenURL)
	}
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
	var invite string
	fs.StringVar(&invite, "invite", "", "Redeem an invitation code or file from the Discovery service")
	var discoveryURL string
	fs.StringVar(&discoveryURL, "discovery", "", "Discovery service URL (used with --invite)")
	var nodeName string
	fs.StringVar(&nodeName, "name", "", "Node/user name when redeeming an invitation")
	var manualToken string
	fs.StringVar(&manualToken, "token", "", "Manually specify an OAuth or access token")
	var deviceFlow bool
	fs.BoolVar(&deviceFlow, "device", false, "Use OAuth 2.0 Device Authorization Grant (for headless environments)")
	var clientID string
	fs.StringVar(&clientID, "client-id", "", "OAuth Client ID")
	var clientSecret string
	fs.StringVar(&clientSecret, "client-secret", "", "OAuth Client Secret")
	var authURL string
	fs.StringVar(&authURL, "auth-url", "", "OAuth Authorization URL")
	var tokenURL string
	fs.StringVar(&tokenURL, "token-url", "", "OAuth Token URL")
	var deviceURL string
	fs.StringVar(&deviceURL, "device-url", "", "OAuth Device Authorization URL")
	var userInfoURL string
	fs.StringVar(&userInfoURL, "userinfo-url", "", "OAuth UserInfo URL")

	_ = fs.Parse(args)

	oauthOpts := oauthLoginOptions{
		clientID:     clientID,
		clientSecret: clientSecret,
		authURL:      authURL,
		tokenURL:     tokenURL,
		deviceURL:    deviceURL,
		userInfoURL:  userInfoURL,
		manualToken:  manualToken,
		deviceFlow:   deviceFlow,
	}

	if invite == "" {
		invite = os.Getenv("INVARIANT_INVITE")
	}
	if invite != "" {
		oauthOpts.reuseSaved = true
		runInviteRedeemFlowWithOAuth(cfg, invite, discoveryURL, nodeName, oauthOpts)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	tf, ident, err := ensureValidatedOAuth(ctx, oauthOpts)
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

func obtainDeviceOAuthToken(clientID, clientSecret, deviceURL, tokenURL string) (OAuthTokenFile, error) {
	data := url.Values{
		"client_id": {clientID},
		"scope":     {"openid profile email"},
	}

	resp, err := http.PostForm(deviceURL, data)
	if err != nil {
		return OAuthTokenFile{}, fmt.Errorf("device authorization failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return OAuthTokenFile{}, fmt.Errorf("device authorization failed (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var deviceResp struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&deviceResp); err != nil {
		return OAuthTokenFile{}, fmt.Errorf("failed to decode device response: %w", err)
	}

	urlToOpen := deviceResp.VerificationURIComplete
	if urlToOpen == "" {
		urlToOpen = deviceResp.VerificationURI
	}

	fmt.Printf("\nTo log in, please visit: %s\nand enter the code: %s\n\nWaiting for authorization...\n",
		urlToOpen, deviceResp.UserCode)

	interval := max(time.Duration(deviceResp.Interval)*time.Second, 5*time.Second)
	expires := time.Now().Add(time.Duration(deviceResp.ExpiresIn) * time.Second)

	for time.Now().Before(expires) {
		time.Sleep(interval)

		pollData := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {deviceResp.DeviceCode},
			"client_id":   {clientID},
		}
		if clientSecret != "" {
			pollData.Set("client_secret", clientSecret)
		}

		pollResp, err := http.PostForm(tokenURL, pollData)
		if err != nil {
			continue
		}

		if pollResp.StatusCode == http.StatusOK {
			var tokenResp oauthTokenResponse
			_ = json.NewDecoder(pollResp.Body).Decode(&tokenResp)
			pollResp.Body.Close()
			return tokenResp.toTokenFile(), nil
		}

		var errResp struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(pollResp.Body).Decode(&errResp)
		pollResp.Body.Close()

		if errResp.Error == "authorization_pending" {
			continue
		} else if errResp.Error == "slow_down" {
			interval += 5 * time.Second
		} else {
			return OAuthTokenFile{}, fmt.Errorf("authentication error: %s", errResp.Error)
		}
	}

	return OAuthTokenFile{}, fmt.Errorf("device authorization timed out")
}
