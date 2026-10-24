package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"sync"
	"time"
)

type CallerKind string

const (
	CallerKindAnonymous CallerKind = "anonymous"
	CallerKindService   CallerKind = "service"
	CallerKindUser      CallerKind = "user"
	CallerKindAdmin     CallerKind = "admin"
)

// Caller represents the authenticated entity making a request.
type Caller struct {
	Kind        CallerKind
	Identity    NodeIdentity
	KeyRecord   *ServiceKeyRecord
	TokenRecord *AccessTokenRecord
}

type callerKey struct{}

// WithCaller injects the authenticated Caller into the context.
func WithCaller(ctx context.Context, caller Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, caller)
}

// CallerFromContext extracts the authenticated Caller from the context, if present.
func CallerFromContext(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// Authenticator validates a user token (e.g. OAuth Bearer token) and returns the verified user identity.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*NodeIdentity, error)
}

// UserInfoAuthenticator validates OAuth tokens via an HTTP UserInfo endpoint with TTL caching.
type UserInfoAuthenticator struct {
	userInfoURL string
	httpClient  *http.Client
	cache       sync.Map // token -> cachedAuth
	ttl         time.Duration
}

type cachedAuth struct {
	identity NodeIdentity
	expires  time.Time
}

type userInfoResponse struct {
	Sub   string `json:"sub"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// NewUserInfoAuthenticator creates an Authenticator that delegates to an OAuth2/OIDC UserInfo endpoint.
func NewUserInfoAuthenticator(userInfoURL string, httpClient *http.Client, ttl time.Duration) *UserInfoAuthenticator {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &UserInfoAuthenticator{
		userInfoURL: userInfoURL,
		httpClient:  httpClient,
		ttl:         ttl,
	}
}

func (a *UserInfoAuthenticator) Authenticate(ctx context.Context, token string) (*NodeIdentity, error) {
	if val, ok := a.cache.Load(token); ok {
		cached := val.(cachedAuth)
		if time.Now().Before(cached.expires) {
			idCopy := cached.identity
			return &idCopy, nil
		}
		a.cache.Delete(token)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.userInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned status %d", resp.StatusCode)
	}

	var uInfo userInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&uInfo); err != nil {
		return nil, fmt.Errorf("failed to decode userinfo: %w", err)
	}

	name := uInfo.Name
	if name == "" {
		name = uInfo.Email
	}
	if name == "" {
		name = uInfo.Sub
	}

	ident := NodeIdentity{
		Name:  name,
		Email: uInfo.Email,
	}

	a.cache.Store(token, cachedAuth{
		identity: ident,
		expires:  time.Now().Add(a.ttl),
	})

	return &ident, nil
}

// StaticAuthenticator provides a static lookup table of token -> NodeIdentity (e.g. for testing).
type StaticAuthenticator struct {
	tokens map[string]NodeIdentity
}

// NewStaticAuthenticator creates a static authenticator with the given token-identity pairs.
func NewStaticAuthenticator(tokens map[string]NodeIdentity) *StaticAuthenticator {
	m := make(map[string]NodeIdentity, len(tokens))
	maps.Copy(m, tokens)
	return &StaticAuthenticator{tokens: m}
}

func (s *StaticAuthenticator) Authenticate(ctx context.Context, token string) (*NodeIdentity, error) {
	if id, ok := s.tokens[token]; ok {
		idCopy := id
		return &idCopy, nil
	}
	return nil, errors.New("invalid token")
}
