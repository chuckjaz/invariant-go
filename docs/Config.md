# The invariant project - Configuration (`config.yaml` and `services.yaml`)

Invariant uses two YAML configuration files:

1. **`~/.invariant/config.yaml`** (`internal/config`): Global client CLI configuration.
2. **`services.yaml`** (`internal/start`): Multi-service process definitions for `invariant provision`, `invariant start`, and `invariant systemd`.

## Global Client Configuration (`~/.invariant/config.yaml`)

`internal/config.Load()` reads `~/.invariant/config.yaml` (if present) and applies overrides from `~/.invariant/keys/client.discovery` and environment variables.

### Schema

```yaml
discovery: "http://localhost:3003"
access_token: ""
oauth_token: ""
service_key: ""
listen_ip: false
log: false
derp_map_url: ""
write_tag: ""
repository:
  write_tag: ""
```

| Field | YAML Key | Description |
| ----- | -------- | ----------- |
| `Discovery` | `discovery` | Discovery service URL (`http://...` or `itp://...`). |
| `AccessToken` | `access_token` | Redeemed control-plane access token. |
| `OAuthToken` | `oauth_token` | OAuth2 bearer token. |
| `ServiceKey` | `service_key` | Service authentication key. |
| `ListenIP` | `listen_ip` | Default `-listen-ip` preference. |
| `Log` | `log` | Enable diagnostic logging (`-log`). |
| `DERPMapURL` | `derp_map_url` | Custom Tailcat DERP map URL. |
| `WriteTag` | `write_tag` | Default storage write tag. |
| `Repository.WriteTag` | `repository.write_tag` | Default storage write tag for `invariant repository` commands. |

### Environment Variable Overrides

- `INVARIANT_DISCOVERY`: Overrides `discovery`.
- `INVARIANT_ACCESS_TOKEN`: Overrides `access_token`.
- `INVARIANT_SERVICE_KEY`: Overrides `service_key`.
- `INVARIANT_OAUTH_TOKEN`: Overrides `oauth_token`.
- `INVARIANT_LOG`: Enables `log` when `"1"` or `"true"` (case-insensitive).

Standard directories: `~/.invariant` (`0755`), `~/.invariant/keys` (`0700`), `~/.cache/invariant` (`0755`).

---

## Service Runner Configuration (`services.yaml`)

Used by `invariant start`, `invariant provision`, and `invariant systemd` (`internal/start`).

### Schema

```yaml
common:
  discovery:
    discovery: "http://localhost:3003"
    advertise: "http://localhost"
  distribute:
    distribute: "distribute-1"

services:
  - command: inv-discovery
    args:
      port: "3003"
      listen-ip: "true"

  - command: inv-storage
    use: [discovery, distribute]
    args:
      dir: "*/storage-1"
      notify: "distribute-1"
    environment:
      INVARIANT_SERVICE_KEY: "$key:storage-1.key"
```

### Semantics

- **`common` & `use`**: `use` accepts a single group name or a list of group names from `common`. Key/value pairs from `common` are merged into `args` only if not already set in the service's `args`.
- **`args` substitution**: Applied to all `args` values:
  - `*`: Expands to the directory containing `services.yaml`.
  - `~`: Expands to the user's home directory.
  - `$VAR`: Expands to environment variable `VAR` (empty string if unset).
  - `\$`, `\~`, `\*`, `\\`: Literal escaped characters.
- **`environment` secret substitution**: Values prefixed with `$key:<filename>` read the raw contents of `~/.invariant/keys/<filename>` (or `-keys-dir`) into the service's environment variable, keeping secrets out of `services.yaml`.
- **Execution & Backoff (`internal/start/runner.go`)**: Services are launched from the `invariant` executable's directory with `--<key>=<value>` flags and prefixed logs (`[<command>:<pid>]`). Crashed services restart with exponential backoff (`1s` initial, `30s` max step, resetting if uptime `> 30s`; controlled by `-max-backoff` and `-retry-interval` on `invariant start`).
