# 🛡️ Enterprise Endpoint Management Platform (UEM)

<div align="center">

[![Release](https://img.shields.io/badge/Release-v1.5.3+-brightgreen.svg?style=for-the-badge&logo=github)](https://github.com/henokakunemail-stack/UEM/releases)
[![Go Version](https://img.shields.io/badge/Go-1.23%2B%20%7C%201.26%2B-blue.svg?style=for-the-badge&logo=go)](https://golang.org)
[![Pure Go](https://img.shields.io/badge/CGO-Disabled%20(Zero%20CGO)-success.svg?style=for-the-badge&logo=linux)](https://golang.org)
[![Web Console](https://img.shields.io/badge/Frontend-React%2019%20%2B%20TypeScript%20%2B%20Vite-blueviolet.svg?style=for-the-badge&logo=react)](web-console)
[![Architecture](https://img.shields.io/badge/Architecture-Single--Binary%20Embedded-orange.svg?style=for-the-badge&logo=docker)](#-single-binary-delivery--architecture)
[![License](https://img.shields.io/badge/License-MIT-green.svg?style=for-the-badge)](LICENSE)

**Unified, high-performance endpoint management and corporate compliance platform designed to orchestrate 500 to 10,000+ distributed endpoints (Windows, Linux, macOS) across hybrid multi-branch environments.**

[Features](#-key-capabilities) • [Architecture](#-architectural-highlights) • [Quick Start](#-quick-start-installation) • [Deployment](#-production-deployment) • [Updater](#-safe-in-place-updater) • [API & Security](#-production-hardening--security)

</div>

---

## 🌟 Overview

UEM is a lightweight yet enterprise-grade Unified Endpoint Management system. It provides IT administrators, sysadmins, and security teams with unified control over distributed fleets with **zero open incoming ports** required on client networks.

```
                           ┌────────────────────────────────────────┐
                           │      UEM Central Server (Go + SPA)     │
                           │  REST API + WebSocket Hub + SQLite WAL │
                           └──────────────────┬─────────────────────┘
                                              │
                      HTTPS / WSS (Port 8443) │ Outbound-Only Persistent WebSockets
                                              │ 0 Inbound Ports on Client Firewalls
             ┌────────────────────────────────┼────────────────────────────────┐
             ▼                                ▼                                ▼
   ┌──────────────────┐             ┌──────────────────┐             ┌──────────────────┐
   │   Headquarters   │             │  Branch Office   │             │   Remote / WFH   │
   │  Windows 11 / 10 │             │   Linux Server   │             │  macOS (M-Series)│
   │ Endpoint Agent   │             │ Endpoint Agent   │             │ Endpoint Agent   │
   └──────────────────┘             └──────────────────┘             └──────────────────┘
```

---

## 🚀 Key Capabilities

| Domain | Features & Description |
|---|---|
| 🖥️ **Fleet Telemetry** | Real-time connection status (online/offline ratios), hardware specs (CPU, RAM, Disks, NICs, Battery), storage threshold alerts, and OS distribution breakdown. |
| 🎮 **Remote Control & Shell** | Full interactive low-latency remote desktop streaming (session-aware input replay) and in-browser VT100 interactive shell terminal (PowerShell, CMD, Bash, Zsh). |
| 🛡️ **Group Policy Engine** | Active Directory equivalent policy management: Removable USB Mass Storage lock, corporate wallpaper enforcement, inactivity screen lock, Windows Update (`AUOptions`), and managed local administrator accounts. |
| 📦 **Software Deployment** | Silent enterprise package distribution (MSI, EXE, PKG, DEB, RPM) with batch execution, hash verification, pre/post-install health checks, and quiet uninstalls. |
| 🩹 **Patch Management** | OS security update scanning via Windows Update API (WUA) and native Linux/macOS package managers with scheduled maintenance reboots. |
| 🌐 **Network & Web Filter** | Pure-Go DNS sinkholing (0.0.0.0) with atomic hosts file manipulation, local DNS resolver cache flushing, and domain whitelist/blacklist policy modes. |
| 🏢 **Multi-Site Enrollment** | Reusable multi-device enrollment tokens supporting branch self-service location assignment through interactive GUI installers and CLI flags (`-site`). |
| 👤 **Managed Admin Account** | Non-destructive, idempotent local administrator provisioning across Windows (`S-1-5-32-544`), Linux (`sudo`/`wheel`), and macOS (`admin`) with credential streaming via stdin. |
| 📊 **IT Asset & Licenses** | Hardware Asset Management (HAM) with cost valuation, serial tracking, warranty expiry alerts, and Software Asset Management (SAM) live seat compliance reconciliation. |
| ⏰ **Task Scheduler** | Central script repository with cryptographic SHA-256 validation, recurring cron schedules, and fleet-wide maintenance tasks (disk cleanup, temp purge). |
| 🔒 **Tamper-Evident Audit** | Comprehensive immutable audit trails for every administrative action, remote control session, and policy change with streaming CSV/JSON export. |

---

## 🏗️ Architectural Highlights

### 1. Pure Go & Zero CGO
- Compiles with `CGO_ENABLED=0` across 5 primary target architectures:
  - `windows/amd64`
  - `linux/amd64`
  - `linux/arm64` (aarch64)
  - `darwin/amd64` (macOS Intel)
  - `darwin/arm64` (Apple Silicon M1/M2/M3/M4)
- Uses `modernc.org/sqlite` for database operations without any external C runtime dependencies.

### 2. Single-Binary Delivery with Embedded SPA
- React 19 + TypeScript + Vite SPA web console is pre-built into `server/cmd/server/dist/` and embedded directly into the Go server binary via standard Go `embed.FS`.
- Zero external assets or web server configuration needed: one executable serves both REST/WebSocket endpoints and the complete web application.

### 3. Outbound-Only Branch Network Topology
- Agents connect outward via TLS WebSockets (`wss://`).
- Requires **zero open inbound ports** on client branch firewalls or NAT routers.
- Full-jitter reconnection backoff (1s - 60s) prevents thundering herd connection storms during link recovery.
- Bidirectional 20-second ping/pong frames keep stateful firewall NAT translation tables alive indefinitely.

### 4. High-Performance SQLite WAL & Optional PostgreSQL
- **SQLite WAL**: Operates with `PRAGMA journal_mode=WAL;`, `PRAGMA busy_timeout=5000;`, `PRAGMA foreign_keys=ON;`, and `PRAGMA synchronous=NORMAL;`.
- **Immediate Write Transactions**: Database transactions acquire SQLite write locks immediately (`_txlock=immediate`) to guarantee atomicity and prevent stale reads.
- **Hot Query Indexes**: Optimized query paths for agent secrets, token consumption, device status filtering, and audit log tailing.
- **PostgreSQL Ready**: Switch transparently from SQLite to PostgreSQL by defining `DB_URL=postgres://...` with automatic query rebind (`?` → `$N`) and schema translations.

---

## ⚡ Production Hardening & Performance

The platform incorporates comprehensive enterprise production-grade architecture patterns:

- **Token-Bucket Rate Limiter**: Central in-memory API rate limiter (`httpguard.APIRateLimiter`) returning RFC-compliant headers (`RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset`, `Retry-After`) with HTTP 429 protection.
- **Gateway Resilience Middleware**: Chi HTTP router pipeline fortified with `middleware.RequestID` (`X-Request-ID`), `middleware.RealIP` (trusted proxy forwarding), and `middleware.Recoverer` (graceful panic recovery).
- **CORS Defense**: Strict origin matching (`httpguard.CORS`) supporting localhost, same-host, and operator-defined wildcard domains (`*.example.com`).
- **HTTP Cache Optimization**: Browser caching split into **immutable long-term caching** (`max-age=31536000, immutable`) for hashed Vite bundles and **strict cache-busting** (`no-cache, no-store, must-revalidate`) for SPA entrypoints.
- **In-Memory TTL Caching**: Dashboard telemetry metrics cached with 10-second thread-safe `sync.RWMutex` to absorb burst traffic from multi-operator consoles.
- **N+1 Query Elimination**: Batch SQL aggregation with `GROUP BY` across license allocations and device inventory, keeping compliance evaluations $O(1)$ instead of $O(N)$.
- **Process Protection**: Windows background agents run in Session 0, spawning interactive capture workers via `CreateProcessAsUser` in the active user's desktop session (`winsta0\default`).

---

## 🚀 Quick-Start: Installation

### 🖥️ Central Server

| Platform | Method | Instructions |
|---|---|---|
| **Linux (Ubuntu/Debian)** | Automated Deploy Script | `curl -fsSL https://raw.githubusercontent.com/henokakunemail-stack/UEM/main/update.sh \| sudo bash` |
| **Linux (From Git Source)** | In-Place Source Build | `cd ~/UEM && git pull origin main && sudo bash update.sh --from-source` |
| **Windows Server** | NSIS Setup Wizard | Run `EndpointServer-Setup.exe` as Administrator (auto-provisions Windows Service on port `8443`). |
| **Docker Compose** | Production Container | `docker compose up -d` (uses distroless container image with persistent `/data` volume). |

#### Docker Compose Quickstart
```yaml
services:
  endpoint-mgmt-server:
    image: endpoint-mgmt-server
    build:
      context: .
      dockerfile: Dockerfile.server
    restart: unless-stopped
    ports:
      - "8443:8443"
    environment:
      - HTTP_ADDR=:8443
      - DB_PATH=/data/endpoint-mgmt.db
      - JWT_SECRET=your-32-character-random-secret-key-here
      - ADMIN_PASSWORD=your-secure-admin-password
    volumes:
      - endpoint-data:/data

volumes:
  endpoint-data:
    driver: local
```

---

### 📱 Endpoint Agent Installation

> **Step 1**: Open the Web Console → Navigate to **Devices** → Click **Generate Enrollment Token** (select multi-device or single-use).

| OS Platform | Installation Method | Command / Procedure |
|---|---|---|
| **Windows 10 / 11 / Server** | GUI Setup Wizard | Run `EndpointAgent-Setup.exe` → Enter Server URL and Token. |
| **Windows (Silent / GPO)** | Mass Deployment | `EndpointAgent-Setup.exe /S /SERVER=https://server:8443 /TOKEN=<TOKEN> [/SITE=Jakarta-HQ]` |
| **Linux (one line)** | Served by your server | `curl -fsSL https://server:8443/install/linux.sh \| sudo bash -s -- --server https://server:8443 --token <TOKEN>` |
| **Linux (Ubuntu / Debian)** | Debian Package | `sudo dpkg -i endpoint-agent_amd64.deb`<br>`sudo endpoint-agent -server https://server:8443 -enroll <TOKEN> -enroll-only -creds /etc/endpoint-agent/creds.json`<br>`sudo systemctl start endpoint-agent` |
| **macOS (Intel & Apple Silicon)** | Installer Package | Run `EndpointAgent.pkg`, then:<br>`sudo endpoint-agent -server https://server:8443 -enroll <TOKEN> -enroll-only -creds /etc/endpoint-agent/creds.json`<br>`sudo launchctl kickstart -k system/com.endpoint-mgmt.agent` |

The site chosen when the token is created is applied to every machine that enrolls with it; `/SITE=` (installer) or `-site` (agent) only overrides it per machine. Running an installer or `-enroll-only` again on an enrolled machine keeps its identity, so it does not use up a multi-device token; pass `-reenroll` to force a new identity. Devices → **Enrollment tokens** shows how many machines each multi-device token has enrolled, and revokes it.

---

## 🔄 Safe In-Place Updater

The repository includes an intelligent server daemon updater that handles hot SQLite backups, atomic binary swapping (`.bak`), `.env` preservation, and health checks:

```bash
# Update directly from GitHub
curl -fsSL https://raw.githubusercontent.com/henokakunemail-stack/UEM/main/update.sh | sudo bash

# Or compile and update directly from latest local git source:
cd ~/UEM
git pull origin main
sudo bash update.sh --from-source
```

---

## 🌐 Production Reverse Proxy (Nginx & Caddy)

Production templates supporting HTTP/2, HTTP/3, and 24-hour persistent WebSockets are available in `deploy/reverse-proxy/`:

### Nginx Configuration Preview (`deploy/reverse-proxy/nginx.conf`)
```nginx
upstream endpoint_backend {
    server 127.0.0.1:8443 max_fails=3 fail_timeout=10s;
    keepalive 64;
}

server {
    listen 443 ssl http2;
    server_name uem.example.com;

    ssl_certificate     /etc/ssl/certs/endpoint-mgmt.crt;
    ssl_certificate_key /etc/ssl/private/endpoint-mgmt.key;

    # REST API & Embedded Web Console
    location / {
        proxy_pass http://endpoint_backend;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Persistent 24-Hour WebSocket Transport (Agents, Shell, Remote Desktop)
    location ~* ^/api/(agent/connect|devices/.*/terminal/ws|devices/.*/remotecontrol/ws) {
        proxy_pass http://endpoint_backend;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_buffering off;
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
    }
}
```

---

## 🛠️ Building From Source

### Prerequisites
- **Go**: 1.23 or newer
- **Node.js**: 20.x or newer & npm

### 1. Build Web Console
```bash
cd web-console
npm install
npm run build
# Compiles directly into server/cmd/server/dist/ for Go embedding
```

### 2. Build Server Binary
```bash
# Standalone Linux Server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o endpoint-mgmt-server ./server/cmd/server

# Standalone Windows Server
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o endpoint-mgmt-server.exe ./server/cmd/server
```

### 3. Build Agent Binaries (Cross-Platform)
```bash
# Windows x86_64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o agent-windows-amd64.exe ./agent/cmd/agent

# Linux x86_64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o agent-linux-amd64 ./agent/cmd/agent

# Linux ARM64 (Raspberry Pi, AWS Graviton)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o agent-linux-arm64 ./agent/cmd/agent

# macOS Apple Silicon (M1/M2/M3/M4)
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o agent-darwin-arm64 ./agent/cmd/agent
```

---

## 🧪 Testing & Verification

Run the complete backend test suite:
```bash
# Run all unit and integration tests (1,000+ tests)
CGO_ENABLED=0 go test -count=1 ./...
```

Verify frontend styling and TypeScript compilation:
```bash
cd web-console
npm run build
```

---

## 📄 License & Attribution

This project is licensed under the [MIT License](LICENSE).
Built with pure Go and modern web standards for enterprise fleet resilience.
