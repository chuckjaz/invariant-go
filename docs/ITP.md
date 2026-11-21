# The invariant project - ITP (Invariant Transport Protocol)

ITP (`itp://`) is Invariant's encrypted peer-to-peer HTTP transport built on Tailcat (WireGuard/DERP). By default (`-listen-ip=false`), Invariant microservices listen on ITP rather than host TCP ports, authenticating peers by WireGuard public key and control-plane allowlists.

## Transport Architecture (`internal/itp`)

### Listener (`itp.Listen`)

- When `ListenIP` is `false` (default on all `inv-*` services):
  - Derives a dedicated server Curve25519 key from the node's private key via HMAC-SHA256 (`"github.com/chuckjaz/invariant-go itp server key v1"`) so outbound client dials and inbound server listeners do not collide on DERP.
  - Starts a `tailcat.Server` listening on virtual port `:80` and advertises `itp://<tailcat-addr>`.
  - When `ControlPlane` or `AllowedClients` is configured, enforces a peer public-key allowlist on `Accept()` using `TAILCAT_PEER_KEY`. Control-plane keys are synced every `5s` (default `SyncInterval`) and refreshed on cache miss.
- When `ListenIP` is `true` (`-listen-ip`):
  - Binds a standard TCP listener on `<host>:<port>`.

### Client Transport (`itp.Transport`)

- For `itp://` URLs, `Transport.RoundTrip` rewrites the scheme to `http`, defaults the port to `:80`, injects `Authorization: Bearer <token>` if unset, and dials over `tailcat.Client.DialTCPPort` (serializing the initial dial per target address to avoid DERP contention).
- For `http://` / `https://` URLs, delegates to the base `http.RoundTripper` while still injecting `Authorization: Bearer <token>`.

## Key, Token, and Discovery Persistence

Keys and tokens are stored with `0600` permissions (`0700` directory):

- **Key file**: JSON `{"private": "...", "preshared_key": "..."}`.
  - Default path: `~/.invariant/keys/<name>.key` (defaults to `default.key`).
  - Service resolution (`ServiceKeyPath`): explicit `-key-file` -> `<dir>/service.key` -> `~/.invariant/keys/<serviceID>.key`. If no path is specified, an ephemeral key is generated.
- **Token file**:
  - Default path: `~/.invariant/keys/<name>.token`.
  - Service resolution (`ServiceTokenPath`): explicit `-token-file` -> `<dir>/service.token` -> `~/.invariant/keys/<serviceID>.token`.
- **Discovery URL file**:
  - `<dir>/discovery.url` or `~/.invariant/keys/<name>.discovery`.

### Token Resolution & Invitation Redemption (`ResolveOrRedeemToken`)

When a service starts with `-invite` (or `INVARIANT_INVITE`, accepting an inline `inv_invite_...` code or file path):

1. Extracts the discovery URL from the invitation code if not explicitly provided.
2. Redeems the invitation via `POST /auth/invite/redeem` (30s timeout) with the node's public key.
3. Persists the returned access token to `-token-file` (and default token path) and saves the discovery URL.
4. On subsequent restarts, falls back in order: explicit `-service-key`, `INVARIANT_ACCESS_TOKEN`, `INVARIANT_SERVICE_KEY`, `<token-file>`, and `~/.invariant/keys/<serviceID>.token`.

Passing `-provision <invite>` redeems the invitation, saves the token and discovery URL, and exits without starting the service.
