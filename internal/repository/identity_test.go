package repository

import (
	"context"
	"os"
	"testing"
)

func TestEnvironmentIdentityProvider(t *testing.T) {
	ctx := context.Background()
	os.Setenv("GIT_AUTHOR_NAME", "Env Author")
	os.Setenv("GIT_AUTHOR_EMAIL", "env.author@example.com")
	os.Setenv("INVARIANT_AUTH_TOKEN", "env-token-xyz")
	defer func() {
		os.Unsetenv("GIT_AUTHOR_NAME")
		os.Unsetenv("GIT_AUTHOR_EMAIL")
		os.Unsetenv("INVARIANT_AUTH_TOKEN")
	}()

	provider := NewEnvironmentIdentityProvider()
	id, err := provider.CurrentIdentity(ctx)
	if err != nil {
		t.Fatalf("CurrentIdentity failed: %v", err)
	}
	if id.Name != "Env Author" {
		t.Errorf("Expected Name 'Env Author', got %q", id.Name)
	}
	if id.Email != "env.author@example.com" {
		t.Errorf("Expected Email 'env.author@example.com', got %q", id.Email)
	}
	if id.Token != "env-token-xyz" {
		t.Errorf("Expected Token 'env-token-xyz', got %q", id.Token)
	}
}

func TestMultiIdentityProviderFallback(t *testing.T) {
	ctx := context.Background()
	os.Setenv("GIT_AUTHOR_NAME", "Fallback Author")
	os.Setenv("GIT_AUTHOR_EMAIL", "fallback@example.com")
	defer func() {
		os.Unsetenv("GIT_AUTHOR_NAME")
		os.Unsetenv("GIT_AUTHOR_EMAIL")
	}()

	mockTS := &mockTailscaleClient{statusResp: nil}
	tsProvider := NewTailscaleIdentityProvider(mockTS)
	envProvider := NewEnvironmentIdentityProvider()

	multi := NewMultiIdentityProvider(tsProvider, envProvider)
	id, err := multi.CurrentIdentity(ctx)
	if err != nil {
		t.Fatalf("MultiIdentityProvider failed: %v", err)
	}
	if id.Name != "Fallback Author" {
		t.Errorf("Expected fallback name 'Fallback Author', got %q", id.Name)
	}
}
