package discovery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tailscale.com/types/key"
)

var (
	ErrInvitationNotFound  = errors.New("invitation not found")
	ErrInvitationUsed      = errors.New("invitation has already been used")
	ErrInvitationExpired   = errors.New("invitation has expired")
	ErrInvitationRevoked   = errors.New("invitation has been revoked")
	ErrAccessTokenNotFound = errors.New("access token not found")
	ErrAccessTokenRevoked  = errors.New("access token has been revoked")
	ErrAccessTokenExpired  = errors.New("access token has expired")
	ErrInvalidRole         = errors.New("invalid role: must be user, service, or admin")
)

// TokenRole specifies the authorization scope of an invitation and its resulting access token.
type TokenRole string

const (
	RoleUser    TokenRole = "user"
	RoleService TokenRole = "service"
	RoleAdmin   TokenRole = "admin"
)

// DefaultInvitationTTL is the default lifetime for an invitation code (15 minutes).
const DefaultInvitationTTL = 15 * time.Minute

// ValidRole returns true if r is a valid TokenRole.
func ValidRole(r TokenRole) bool {
	switch r {
	case RoleUser, RoleService, RoleAdmin:
		return true
	default:
		return false
	}
}

// PeerKeyResolver extracts the peer's WireGuard public key from a remote TCP address string.
// *itp.Listener implements this interface directly.
type PeerKeyResolver interface {
	PeerKey(remoteAddr string) (key.NodePublic, bool)
}

// InvitationRecord stores metadata and state for a one-time invitation code.
type InvitationRecord struct {
	ID              string        `json:"id"`
	Code            string        `json:"code"`
	ControlPlaneURL string        `json:"control_plane_url,omitempty"`
	Name            string        `json:"name"`
	Email           string        `json:"email,omitempty"`
	Role            TokenRole     `json:"role"`
	CreatedBy       NodeIdentity  `json:"created_by"`
	CreatedAt       time.Time     `json:"created_at"`
	ExpiresAt       time.Time     `json:"expires_at"`
	Used            bool          `json:"used,omitempty"`
	UsedAt          *time.Time    `json:"used_at,omitempty"`
	UsedByKey       string        `json:"used_by_key,omitempty"`
	Revoked         bool          `json:"revoked,omitempty"`
	RevokedAt       *time.Time    `json:"revoked_at,omitempty"`
	RevokedBy       *NodeIdentity `json:"revoked_by,omitempty"`
}

// FormatInvitationCode combines a raw secret invitation code and a control plane URL
// into a single self-contained invitation string: "<rawCode>@<controlPlaneURL>".
func FormatInvitationCode(rawCode, controlPlaneURL string) string {
	raw, existingURL := ParseInvitationCode(rawCode)
	cpURL := strings.TrimSpace(controlPlaneURL)
	if cpURL == "" {
		cpURL = existingURL
	}
	if cpURL == "" {
		return raw
	}
	return raw + "@" + cpURL
}

// ParseInvitationCode splits an invitation string into its raw secret code ("inv_invite_...")
// and the embedded control plane URL (e.g. "itp://tc1..." or "http://..."), if present.
func ParseInvitationCode(code string) (rawCode, controlPlaneURL string) {
	code = strings.TrimSpace(code)
	if idx := strings.IndexByte(code, '@'); idx != -1 {
		return strings.TrimSpace(code[:idx]), strings.TrimSpace(code[idx+1:])
	}
	return code, ""
}

// IsActive returns true if the invitation has not been used, revoked, or expired.
func (r *InvitationRecord) IsActive() bool {
	if r == nil || r.Used || r.Revoked {
		return false
	}
	return time.Now().Before(r.ExpiresAt)
}

// Masked returns a copy of the invitation with the secret code masked for safe display.
func (r InvitationRecord) Masked() InvitationRecord {
	raw, cpURL := ParseInvitationCode(r.Code)
	if cpURL == "" {
		cpURL = r.ControlPlaneURL
	}
	maskedRaw := MaskServiceKey(raw)
	if cpURL != "" {
		r.Code = maskedRaw + "@" + cpURL
	} else {
		r.Code = maskedRaw
	}
	return r
}

// AccessTokenRecord stores metadata and state for an issued access token bound to a peer key.
type AccessTokenRecord struct {
	ID              string        `json:"id"`
	Token           string        `json:"token"`
	ControlPlaneURL string        `json:"control_plane_url,omitempty"`
	Name            string        `json:"name"`
	Email           string        `json:"email,omitempty"`
	Role            TokenRole     `json:"role"`
	PublicKey       string        `json:"public_key,omitempty"`
	InvitationID    string        `json:"invitation_id,omitempty"`
	CreatedBy       NodeIdentity  `json:"created_by"`
	CreatedAt       time.Time     `json:"created_at"`
	ExpiresAt       *time.Time    `json:"expires_at,omitempty"`
	Revoked         bool          `json:"revoked,omitempty"`
	RevokedAt       *time.Time    `json:"revoked_at,omitempty"`
	RevokedBy       *NodeIdentity `json:"revoked_by,omitempty"`
}

// IsActive returns true if the access token is not revoked and not expired.
func (r *AccessTokenRecord) IsActive() bool {
	if r == nil || r.Revoked {
		return false
	}
	if r.ExpiresAt != nil && time.Now().After(*r.ExpiresAt) {
		return false
	}
	return true
}

// Masked returns a copy of the access token record with the secret token masked.
func (r AccessTokenRecord) Masked() AccessTokenRecord {
	r.Token = MaskServiceKey(r.Token)
	return r
}

// GenerateInvitation creates a new high-entropy (256-bit) single-use InvitationRecord.
func GenerateInvitation(name string, role TokenRole, creator NodeIdentity, ttl time.Duration) (*InvitationRecord, error) {
	if role == "" {
		role = RoleUser
	}
	if !ValidRole(role) {
		return nil, ErrInvalidRole
	}
	if ttl <= 0 {
		ttl = DefaultInvitationTTL
	}

	codeBytes := make([]byte, 32) // 256 bits of CSPRNG entropy
	if _, err := rand.Read(codeBytes); err != nil {
		return nil, fmt.Errorf("failed to generate invitation code: %w", err)
	}
	code := "inv_invite_" + hex.EncodeToString(codeBytes)

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("failed to generate invitation id: %w", err)
	}
	id := "inv-" + hex.EncodeToString(idBytes)

	now := time.Now().UTC()
	return &InvitationRecord{
		ID:        id,
		Code:      code,
		Name:      name,
		Role:      role,
		CreatedBy: creator,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}, nil
}

// GenerateAccessToken creates a new high-entropy (256-bit) AccessTokenRecord bound to publicKey.
func GenerateAccessToken(name, email string, role TokenRole, publicKey, invitationID string, creator NodeIdentity, expiresAt *time.Time) (*AccessTokenRecord, error) {
	if role == "" {
		role = RoleUser
	}
	if !ValidRole(role) {
		return nil, ErrInvalidRole
	}

	tokBytes := make([]byte, 32) // 256 bits of CSPRNG entropy
	if _, err := rand.Read(tokBytes); err != nil {
		return nil, fmt.Errorf("failed to generate access token: %w", err)
	}
	token := "inv_at_" + hex.EncodeToString(tokBytes)

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, fmt.Errorf("failed to generate access token id: %w", err)
	}
	id := "tok-" + hex.EncodeToString(idBytes)

	return &AccessTokenRecord{
		ID:           id,
		Token:        token,
		Name:         name,
		Email:        email,
		Role:         role,
		PublicKey:    strings.TrimSpace(publicKey),
		InvitationID: invitationID,
		CreatedBy:    creator,
		CreatedAt:    time.Now().UTC(),
		ExpiresAt:    expiresAt,
	}, nil
}

// InvitationManager manages creation, redemption, listing, and revocation of invitations and access tokens.
type InvitationManager interface {
	CreateInvitation(ctx context.Context, name string, role TokenRole, creator NodeIdentity, ttl time.Duration) (*InvitationRecord, error)
	RedeemInvitation(ctx context.Context, code string, publicKey string, identity NodeIdentity) (*AccessTokenRecord, error)
	ListInvitations(ctx context.Context) ([]InvitationRecord, error)
	RevokeInvitation(ctx context.Context, codeOrID string, revoker NodeIdentity) error

	VerifyAccessToken(ctx context.Context, token string) (bool, *AccessTokenRecord, error)
	ListAccessTokens(ctx context.Context) ([]AccessTokenRecord, error)
	RevokeAccessToken(ctx context.Context, tokenOrID string, revoker NodeIdentity) error
}

// InvitationRestorer allows restoring invitations and access tokens during journal replay or disaster recovery.
type InvitationRestorer interface {
	RestoreInvitation(ctx context.Context, rec InvitationRecord) error
	RestoreAccessToken(ctx context.Context, rec AccessTokenRecord) error
}

// EnsureBootstrapInvitation checks if any active admin access token or active admin invitation exists.
// If none exist, it creates a new admin invitation and optionally writes the code to inviteFilePath with 0600 permissions.
func EnsureBootstrapInvitation(ctx context.Context, m InvitationManager, ttl time.Duration, inviteFilePath string, controlPlaneURL ...string) (*InvitationRecord, bool, error) {
	if ttl <= 0 {
		ttl = 1 * time.Hour
	}
	cpURL := ""
	if len(controlPlaneURL) > 0 {
		cpURL = strings.TrimSpace(controlPlaneURL[0])
	}

	tokens, err := m.ListAccessTokens(ctx)
	if err == nil {
		for i := range tokens {
			if tokens[i].Role == RoleAdmin && tokens[i].IsActive() {
				return nil, false, nil
			}
		}
	}

	invites, err := m.ListInvitations(ctx)
	if err == nil {
		for i := range invites {
			if invites[i].Role == RoleAdmin && invites[i].IsActive() {
				inv := invites[i]
				if cpURL != "" {
					inv.ControlPlaneURL = cpURL
					inv.Code = FormatInvitationCode(inv.Code, cpURL)
				}
				if inviteFilePath != "" {
					_ = writeBootstrapInviteFile(inviteFilePath, inv.Code)
				}
				return &inv, false, nil
			}
		}
	}

	inv, err := m.CreateInvitation(ctx, "bootstrap-admin", RoleAdmin, NodeIdentity{Name: "system"}, ttl)
	if err != nil {
		return nil, false, err
	}
	if cpURL != "" {
		inv.ControlPlaneURL = cpURL
		inv.Code = FormatInvitationCode(inv.Code, cpURL)
	}
	if inviteFilePath != "" {
		if err := writeBootstrapInviteFile(inviteFilePath, inv.Code); err != nil {
			return inv, true, err
		}
	}
	return inv, true, nil
}

func writeBootstrapInviteFile(path, code string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(code+"\n"), 0600)
}
