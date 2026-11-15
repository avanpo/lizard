# 🦎 Lizard - Lightweight Server & Container Monitor

Lizard is a lightweight, zero-dependency server monitoring dashboard written in Go. It is designed to run in Docker Compose alongside applications like **Syncthing** and **Borgmatic** to provide clean, real-time telemetry with historical graphs.

## Features

- **Historical Resource Graphs**: Interactive, time-series charts for CPU %, Memory %, and Disk % (using pure client-side SVG with zero external CDN dependencies) that auto-refresh every 2 seconds without reloading the page.
- **Disk Usage & Top Directories Table**: Visualizes total and used disk space, plus an automatically refreshed breakdown of top subdirectories by size and file counts.
- **Docker Container Monitoring**: Inspects active containers, uptime/runtime duration, CPU usage %, and Memory usage/limits via Docker's unix socket.
- **Borgmatic Backup Telemetry**: Displays Borg repository backup archives, original sizes, compressed sizes, deduplicated sizes, deduplication savings, and file counts in a clean table.
- **Pure Go & Zero Runtime Dependencies**: Compiles to a single, static binary with embedded HTML/CSS/JS frontend.
- **100% Env-Configurable**: Configured via environment variables with no config files or sensitive secrets in the repository.

---

## Environment Variables

All configuration is provided via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `LIZARD_PORT` | `8080` | Port for the HTTP dashboard to listen on |
| `LIZARD_HOST` | `0.0.0.0` | Host/interface IP to bind the HTTP server to |
| `LIZARD_DOCKER_SOCKET` | `/var/run/docker.sock` | Path to Docker unix socket |
| `LIZARD_HOST_ROOT` | `/` | Path to host root filesystem (`/host` inside Docker) |
| `LIZARD_DISK_DIRS` | `/` | Comma-separated paths to inspect disk space and top directories |
| `LIZARD_TOP_DIR_LIMIT` | `10` | Maximum number of top subdirectories to display |
| `LIZARD_POLL_INTERVAL` | `2s` | Polling interval for CPU, memory, and container stats |
| `LIZARD_DISK_SCAN_INTERVAL` | `2m` | Interval to recalculate top directory sizes (avoids I/O spikes) |
| `LIZARD_HISTORY_POINTS` | `360` | Number of historical metric points kept in memory |
| `LIZARD_BORGMATIC_CMD` | `""` | Command to execute to obtain Borgmatic JSON output |
| `LIZARD_BORGMATIC_FILE` | `""` | Path to a pre-generated JSON file containing Borgmatic info |
| `LIZARD_BORGMATIC_INTERVAL` | `5m` | Interval between Borgmatic data refreshes |
| `LIZARD_DATA_DIR` | `""` | Directory to persist historical resource usage across restarts (`history.json`) |
| `LIZARD_DEMO` | `false` | Enable demo mode with realistic mock data for local testing |

---

## Quick Start (Local Development)

Run Lizard locally with demo metrics enabled:

```bash
LIZARD_DEMO=true LIZARD_PORT=8080 go run .
```

Open `http://localhost:8080` in your web browser.

---

## Docker & Docker Compose Setup

### 1. Build the Docker Image

```bash
docker build -t lizard:latest .
```

### 2. Add to your `docker-compose.yml`

```yaml
services:
  lizard:
    image: lizard:latest
    container_name: lizard
    restart: unless-stopped
    user: "0:0"
    pid: "host"
    ports:
      - "8080:8080"
    environment:
      - LIZARD_PORT=8080
      - LIZARD_HOST_ROOT=/host
      - LIZARD_DISK_DIRS=/host
      - LIZARD_DOCKER_SOCKET=/var/run/docker.sock
      # If reading borgmatic stats from a JSON file:
      # - LIZARD_BORGMATIC_FILE=/borgmatic/info.json
      # Or if invoking borgmatic via command:
      # - LIZARD_BORGMATIC_CMD=docker exec -i borgmatic borgmatic info --json
    volumes:
      # Access Docker Engine API for container stats
      - /var/run/docker.sock:/var/run/docker.sock
      # Access Host root strictly read-only for accurate host metrics & directory sizing
      - /:/host:ro
```

---

## Permissions & Security Considerations

- **Why does the container run as root (`user: "0:0"`)?**
  1. `/var/run/docker.sock` is owned by `root:docker` (mode `0660`). An unprivileged container user cannot access it without being in the host's `docker` GID.
  2. Scanning top directories under `/host` (such as `/var/lib/docker`, `/root`, `/var/log`) requires root read permissions, otherwise unprivileged users will receive `Permission denied` errors.
- **Safety guarantee via read-only mounts (`:ro`)**:
  Even though the container runs as root, `/:/host:ro` is mounted strictly **read-only**. The Linux kernel will reject any write, delete, or modify operation to the host filesystem.
- **`pid: "host"`**:
  Enables Lizard to see the host's PID namespace and accurately calculate host-wide CPU metrics and uptime from `/proc`.

---

## Borgmatic Integration

Lizard supports two methods to read Borgmatic statistics:

1. **Via JSON File (`LIZARD_BORGMATIC_FILE`)**:
   Configure Borgmatic or a cron hook to write `borgmatic info --json > /path/to/borgmatic.json`, and mount that file into Lizard.
2. **Via Command (`LIZARD_BORGMATIC_CMD`)**:
   If Lizard has access to the Docker socket, you can set `LIZARD_BORGMATIC_CMD=docker exec -i borgmatic borgmatic info --json`.
