# Enterprise Endpoint Management Platform

[![Go Version](https://img.shields.io/badge/Go-1.24%2B-blue.svg)](https://golang.org)
[![Pure Go](https://img.shields.io/badge/CGO-Disabled%20(Zero%20CGO)-success.svg)](https://golang.org)
[![Web Console](https://img.shields.io/badge/Frontend-React%2019%20%2B%20TypeScript%20%2B%20Vite-blueviolet.svg)](web-console)
[![Architecture](https://img.shields.io/badge/Architecture-Single--Binary%20Embedded-orange.svg)](#single-binary-delivery)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

Enterprise-grade, central endpoint management and security compliance platform architected to manage 500+ to 10,000+ distributed endpoints (Windows, Linux, macOS) across distributed branch offices.

---

## 🚀 Key Architectural Highlights

1. **Zero CGO / Pure Go Standard**:
   - Compiles cleanly (`CGO_ENABLED=0`) across five primary OS/CPU architectures:
     - `windows/amd64`
     - `linux/amd64`
     - `linux/arm64` (aarch64)
     - `darwin/amd64` (macOS Intel)
     - `darwin/arm64` (macOS Apple Silicon)
   - Uses `modernc.org/sqlite` for SQLite database operations with zero external C compiler runtime dependencies.

2. **Outbound-Only Branch Network Topology**:
   - Central server listens over secure HTTPS/WSS on whatever hostname you configure (e.g. `https://mgmt.example.com`).
   - Distributed branch endpoints initiate **outbound-only** TLS WebSocket connections.
   - **0 inbound ports** are required on branch firewalls/NAT routers.
   - Automatic reconnect with full jitter backoff (1s - 60s) prevents thundering herd connection storms during network recovery.
   - Persistent 20s WebSocket ping/pong heartbeats keep stateful NAT firewalls alive without idle timeouts.

3. **Single-Binary Delivery with Embedded Web Console**:
   - The Go server binary embeds the compiled React 19 + TypeScript + Vite SPA directly via Go's standard `embed.FS` (`server/cmd/server/web_embed.go`).
   - Serves both REST/WebSocket APIs and the complete web console with client-side SPA routing fallback from a single self-contained executable.

4. **High-Concurrency SQLite WAL Engine**:
   - Write-Ahead Logging (`PRAGMA journal_mode=WAL;`).
   - `PRAGMA busy_timeout=5000;`, `PRAGMA foreign_keys=ON;`, and `PRAGMA synchronous=NORMAL;`.
   - Single-writer pool (`SetMaxOpenConns(1)`) prevents database lock contention while allowing unlimited concurrent readers.

---

## 📦 Complete Enterprise Capabilities (Phases 1 - 14)

| Phase | Module | Core Functionality |
|---|---|---|
| **Phase 1** | **Core & Transport** | WebSocket TLS server/client, JWT authentication, RBAC authorization, tamper-evident audit trail |
| **Phase 2** | **Device Management** | Automated hardware inventory (CPU, RAM, Disks, NICs, Battery), dynamic groups, enrollment |
| **Phase 3** | **Executive Dashboard** | Real-time fleet health KPIs, OS breakdown, storage capacity alerts, branch distribution charts |
| **Phase 4** | **Software Deployment** | Silent software rollouts (MSI, EXE, PKG, DEB, RPM), batching, pre/post install verification |
| **Phase 5** | **Remote Execution & CLI** | Real-time command execution (PowerShell, CMD, Bash, Sh), live streaming interactive PTY terminal |
| **Phase 6** | **Patch Management** | Windows Update (WUA) and Linux/macOS package patch scanner, CVE tracking, scheduled reboots |
| **Phase 7** | **User Management & RBAC** | Multi-operator console accounts (`admin`, `technician`, `viewer`), credential provisioning |
| **Phase 8** | **Audit & Reports** | Streaming chunked CSV & JSON exports for compliance audits (ISO 27001 / SOC 2) |
| **Phase 9** | **Alerting & Incidents** | Automated metric evaluations (Disk space <10%, offline agents, critical missing CVEs, deduplication) |
| **Phase 10** | **Task Scheduler** | Script repository with cryptographic SHA-256 validation, cron and recurring fleet maintenance jobs |
| **Phase 11** | **Remote Desktop Control** | Outbound reverse screen capture relay (MJPEG/Canvas), mouse click and keyboard input transmission |
| **Phase 12** | **Network & Web Filter** | Pure-Go DNS sinkholing (0.0.0.0), atomic hosts file manipulation with automatic local DNS cache flush |
| **Phase 13** | **Agent Self-Update** | Autonomous in-place binary upgrades, SHA-256 pre-execution validation, phased canary wave rollouts |
| **Phase 14** | **IT Asset & License Management** | Hardware Asset Management (HAM) with configurable currency valuation, Software Asset Management (SAM) live seat reconciliation |

---

## 📂 Repository Directory Structure

```
.
├── agent/                          # Outbound agent source code (Pure Go)
│   ├── cmd/agent/                  # Multi-platform agent entrypoint & inventory collector
│   └── shared/                     # Modular shared agent subsystems
│       ├── enrollment/             # TLS token device enrollment
│       ├── inventory/              # OS hardware spec collection
│       ├── networkfilter/          # DNS sinkhole & hosts policy engine
│       ├── patch/                  # OS patch scanning & silent installation
│       ├── remotecontrol/          # Remote desktop capture & mouse/keyboard replay
│       ├── remoteexec/             # Shell command execution & interactive terminal
│       ├── software/               # Software installer runner
│       ├── transport/              # Outbound WebSocket client with keepalive
│       └── update/                 # Autonomous agent binary self-updater
├── deploy/                         # Production Ubuntu deployment automation
│   ├── install-ubuntu.sh           # One-click Ubuntu server setup script
│   ├── nginx-endpoint.conf.template# Parameterized Nginx reverse proxy (HTTPS + WebSocket)
│   ├── endpoint-mgmt.service       # Systemd daemon configuration
│   └── README-UBUNTU-DEPLOY.md     # Step-by-step production server setup guide
├── docs/                           # Architecture specifications & audit reports
│   ├── architecture/               # Phase 0 through Phase 14 technical blueprints
│   └── readiness-reports/          # Verification scorecards and production audits
├── scripts/                        # Automated PowerShell E2E test suites for all 14 phases
├── server/                         # Central management server source code (Pure Go)
│   ├── cmd/server/                 # Server entrypoint with embedded Web Console
│   │   ├── dist/                   # Production React build embedded at compile time
│   │   ├── main.go                 # HTTP server, routing, TLS, and graceful shutdown
│   │   └── web_embed.go            # Go embed.FS declaration
│   ├── core/                       # Core system foundations
│   │   ├── audit/                  # Immutable audit logging service
│   │   ├── auth/                   # JWT creation, validation, and query token handler
│   │   ├── db/                     # SQLite WAL engine & sequential migrations (0001-0013)
│   │   ├── rbac/                   # Role-Based Access Control context utilities
│   │   └── transport/              # Hub WebSocket manager & agent dispatch
│   └── modules/                    # Enterprise business logic modules (Phases 3-14)
├── tests/integration/              # Go integration test suites
└── web-console/                    # Modern React 19 + TypeScript + Vite Web Console
    ├── src/
    │   ├── components/             # Reusable UI widgets, charts, and interactive modals
    │   ├── context/                # Authentication & Session React contexts
    │   ├── pages/                  # 12 Operational views for all enterprise phases
    │   └── services/               # Typed REST API client & report download helpers
    └── package.json
```

---

## 🛠️ Building From Source

### 1. Build Frontend Console
```bash
cd web-console
npm install
npm run build
# Output goes directly to server/cmd/server/dist/ (vite.config.ts outDir),
# which is the directory server/cmd/server/web_embed.go embeds. Run this BEFORE
# building the server binary — go:embed captures the files at compile time, so a
# server built against a stale dist serves the old console.
```

### 2. Build Server Binary (Linux amd64 for Physical Ubuntu Server)
```bash
# Build standalone Linux server executable with embedded frontend
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o endpoint-mgmt-server ./server/cmd/server
```

### 3. Build Agent Executables (All 5 Target Architectures)
```bash
# Windows x86_64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o agent-windows-amd64.exe ./agent/cmd/agent

# Linux x86_64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o agent-linux-amd64 ./agent/cmd/agent

# Linux ARM64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o agent-linux-arm64 ./agent/cmd/agent

# macOS Intel (x86_64)
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -o agent-darwin-amd64 ./agent/cmd/agent

# macOS Apple Silicon (M1/M2/M3/M4)
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -o agent-darwin-arm64 ./agent/cmd/agent
```

---

## 🌐 Production Server Deployment

Nothing in this repository is tied to a particular domain. Every hostname,
certificate path, and credential is a parameter you supply.

### Linux (Ubuntu / Debian)

The `deploy/` bundle installs the server, an isolated system user, a hardened
systemd unit, and a parameterized nginx reverse proxy:

```bash
# 1. Copy the bundle to the server
scp -r ./deploy/* user@your-server-ip:/tmp/deploy/

# 2. Run the installer with YOUR hostname
cd /tmp/deploy
sudo ./install-ubuntu.sh --domain mgmt.example.com
```

The installer writes the nginx site by substituting `__DOMAIN__` and
`__SSL_DIR__` into `deploy/nginx-endpoint.conf.template`, which:
- Enforces TLS 1.2 and TLS 1.3 with modern ciphers.
- Proxies standard HTTPS REST API calls.
- Supports long-lived full-duplex WebSocket connections (`Upgrade $http_upgrade`)
  with 24-hour read/send timeouts, so agent sockets survive normal idle timeouts.

### Windows Server

There is no scripted installer for Windows Server — the server binary is a
plain console-free service binary. See
[`docs/installation/server-windows.md`](docs/installation/server-windows.md)
for the full procedure.

### Full guides

| Guide | Covers |
|---|---|
| [`docs/installation/server-linux.md`](docs/installation/server-linux.md) | Ubuntu/Debian: systemd, TLS, firewall, backup verification, upgrade & rollback, hardening |
| [`docs/installation/server-windows.md`](docs/installation/server-windows.md) | Windows Server: service registration, TLS, firewall, backup, upgrade |
| [`deploy/README-UBUNTU-DEPLOY.md`](deploy/README-UBUNTU-DEPLOY.md) | Short-form Ubuntu quickstart |

---

## 🧪 Verification & Automated Testing

Run all Go integration test suites across all modules:
```bash
CGO_ENABLED=0 go test -v ./tests/integration/...
```

Run PowerShell end-to-end verification scripts:
```powershell
# E2E Patch Management
.\scripts\e2e-patch-management.ps1

# E2E Remote Terminal & Execution
.\scripts\e2e-remote-exec.ps1

# E2E Task Scheduler
.\scripts\e2e-task-scheduler.ps1

# E2E Network & Web Filter
.\scripts\e2e-network-filter.ps1

# E2E Agent Self-Update
.\scripts\e2e-agent-update.ps1

# E2E IT Asset & Software License Management
.\scripts\e2e-asset-license.ps1
```

---

## 🔐 Security & Compliance

- **Role-Based Access Control**: Granular endpoint access authorization (`admin`, `technician`, `viewer`).
- **Cryptographic Hash Verification**: All scripts, binaries, and patch installers are hashed with SHA-256 before remote dispatch.
- **Audit Logging**: Every administrative action, command execution, and remote desktop connection is recorded with UTC timestamp, user ID, IP address, and change details.
- **Encrypted Communication**: Mandatory TLS 1.2+ encryption on all agent-to-server WebSocket connections and REST endpoints.
