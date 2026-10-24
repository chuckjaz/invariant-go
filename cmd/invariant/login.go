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

// LoadOAuthToken reads the OAuth access token from ~/.invariant/oauth_token.json,
// or INVARIANT_OAUTH_TOKEN environment variable.
func LoadOAuthToken() (string, error) {
	if env := os.Getenv("INVARIANT_OAUTH_TOKEN"); env != "" {
		return env, nil
	}
	path, err := OAuthTokenPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var t OAuthTokenFile
	if err := json.Unmarshal(data, &t); err != nil {
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
)

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
	fs.StringVar(&manualToken, "token", "", "Manually specify an OAuth user access token")
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

	_ = fs.Parse(args)

	if clientID == "" {
		clientID = os.Getenv("INVARIANT_OAUTH_CLIENT_ID")
		if clientID == "" {
			clientID = defaultGoogleClientID
		}
	}
	if clientSecret == "" {
		clientSecret = os.Getenv("INVARIANT_OAUTH_CLIENT_SECRET")
		if clientSecret == "" {
			clientSecret = defaultGoogleClientSecret
		}
	}
	if authURL == "" {
		authURL = os.Getenv("INVARIANT_OAUTH_AUTH_URL")
		if authURL == "" {
			authURL = defaultGoogleAuthURL
		}
	}
	if tokenURL == "" {
		tokenURL = os.Getenv("INVARIANT_OAUTH_TOKEN_URL")
		if tokenURL == "" {
			tokenURL = defaultGoogleTokenURL
		}
	}
	if deviceURL == "" {
		deviceURL = os.Getenv("INVARIANT_OAUTH_DEVICE_URL")
		if deviceURL == "" {
			deviceURL = defaultGoogleDeviceURL
		}
	}

	// 1. Manual token specification
	if manualToken != "" {
		tokenData := &OAuthTokenFile{
			AccessToken: manualToken,
			TokenType:   "Bearer",
		}
		if err := SaveOAuthToken(tokenData); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving token: %v\n", err)
			os.Exit(1)
		}
		path, _ := OAuthTokenPath()
		fmt.Printf("Successfully saved user token to %s\n", path)
		return
	}

	// 2. Device authorization flow
	if deviceFlow {
		if deviceURL == "" || tokenURL == "" {
			fmt.Fprintf(os.Stderr, "Error: --device-url and --token-url are required for device flow (or set INVARIANT_OAUTH_DEVICE_URL)\n")
			os.Exit(1)
		}
		runDeviceLogin(clientID, clientSecret, deviceURL, tokenURL)
		return
	}

	// 3. Interactive browser flow
	if authURL == "" || tokenURL == "" {
		// If OAuth server not specified, prompt for manual token input
		fmt.Printf("No OAuth authorization server configured (--auth-url / INVARIANT_OAUTH_AUTH_URL).\n")
		fmt.Printf("Enter your OAuth access token directly: ")
		var inputToken string
		fmt.Scanln(&inputToken)
		inputToken = strings.TrimSpace(inputToken)
		if inputToken == "" {
			fmt.Fprintf(os.Stderr, "No token provided.\n")
			os.Exit(1)
		}
		tokenData := &OAuthTokenFile{
			AccessToken: inputToken,
			TokenType:   "Bearer",
		}
		if err := SaveOAuthToken(tokenData); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving token: %v\n", err)
			os.Exit(1)
		}
		path, _ := OAuthTokenPath()
		fmt.Printf("Successfully saved user token to %s\n", path)
		return
	}

	runBrowserLogin(clientID, clientSecret, authURL, tokenURL)
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

func runBrowserLogin(clientID, clientSecret, authURL, tokenURL string) {
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
		fmt.Fprintf(os.Stderr, "Failed to start local listener: %v\n", err)
		os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "Authentication failed: %v\n", err)
		os.Exit(1)
	case <-time.After(3 * time.Minute):
		fmt.Fprintf(os.Stderr, "Login timed out after 3 minutes.\n")
		os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "Token exchange failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Token exchange returned HTTP %d: %s\n", resp.StatusCode, string(body))
		os.Exit(1)
	}

	var tokenResp oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to decode token response: %v\n", err)
		os.Exit(1)
	}

	tokenFile := tokenResp.toTokenFile()
	if err := SaveOAuthToken(&tokenFile); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving token: %v\n", err)
		os.Exit(1)
	}

	path, _ := OAuthTokenPath()
	fmt.Printf("\nLogin successful! Credentials saved to %s\n", path)
}

func runDeviceLogin(clientID, clientSecret, deviceURL, tokenURL string) {
	data := url.Values{
		"client_id": {clientID},
		"scope":     {"openid profile email"},
	}

	resp, err := http.PostForm(deviceURL, data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Device authorization failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "Device authorization failed (HTTP %d): %s\n", resp.StatusCode, string(body))
		os.Exit(1)
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
		fmt.Fprintf(os.Stderr, "Failed to decode device response: %v\n", err)
		os.Exit(1)
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
			tokenFile := tokenResp.toTokenFile()
			_ = SaveOAuthToken(&tokenFile)
			path, _ := OAuthTokenPath()
			fmt.Printf("\nLogin successful! Credentials saved to %s\n", path)
			return
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
			fmt.Fprintf(os.Stderr, "Authentication error: %s\n", errResp.Error)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "Device authorization timed out.\n")
	os.Exit(1)
}
