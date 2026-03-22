# Invariant Systemd Service Examples

This directory contains example `systemd` unit files for running the individual Invariant microservices as background daemons natively on Linux.

## Prerequisites

Before starting the services via `systemd`:

1. Build the binaries using the project's build script or `go build`:
   ```bash
   ./build
   ```

2. Move the generated binaries into `/usr/local/bin/` so `systemd` can find them:
   ```bash
   sudo cp bin/* /usr/local/bin/
   ```

3. Create a dedicated system user and group named `invariant`:
   ```bash
   sudo useradd -r -s /bin/false invariant
   ```

4. Create the required directories for the persistent services (`names` and `storage`) and grant ownership to the `invariant` user:
   ```bash
   sudo mkdir -p /var/lib/invariant/names
   sudo mkdir -p /var/lib/invariant/blocks
   sudo chown -R invariant:invariant /var/lib/invariant
   ```

## Installation

1. Copy the `.service` files from this directory into your `systemd` system directory (usually `/etc/systemd/system/`):
   ```bash
   sudo cp docs/examples/systemd/*.service /etc/systemd/system/
   ```

2. Reload the systemd daemon to process the new unit files:
   ```bash
   sudo systemctl daemon-reload
   ```

3. Enable and start the discovery service first (since all other services rely on it for inter-service communication):
   ```bash
   sudo systemctl enable --now invariant-discovery.service
   ```

4. Enable and start the remaining services:
   ```bash
   sudo systemctl enable --now invariant-names.service
   sudo systemctl enable --now invariant-slots.service
   sudo systemctl enable --now invariant-storage.service
   sudo systemctl enable --now invariant-distribute.service
   sudo systemctl enable --now invariant-finder.service
   ```

## Client Mount Configuration

If you want to mount a specific Invariant slot as a virtual file system securely in the background, you can use the `invariant-mount.service` template.
1. Open `docs/examples/systemd/invariant-mount.service` and configure your specific `SLOT_NAME` and `MOUNT_POINT`.
2. Install it identically to the other services:
   ```bash
   sudo cp docs/examples/systemd/invariant-mount.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now invariant-mount.service
   ```

## Logs and Monitoring

You can inspect the logs of any running service using `journalctl`. For example, to view the live logs for the `storage` service:
```bash
sudo journalctl -u invariant-storage.service -f
```
