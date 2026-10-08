# Enterprise Endpoint Management Platform

[![Go Version](https://img.shields.io/badge/Go-1.26%2B-blue.svg)](https://golang.org)
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
   - Connection pool of 16 open / 4 idle, sized for the reads rather than serialised behind one connection — a console page issues several queries at once, and putting them in a line is what turns a slow write into a page timeout.
   - Write transactions take SQLite's write lock at `BEGIN` (`_txlock=immediate`) rather than at first write, so a batch that reads then writes is one atomic unit instead of a transaction built on a read that has already gone stale.

5. **Optional PostgreSQL Backend**:
   - SQLite stays the default and requires nothing: one binary, one file, no services. The single-binary standalone deployment is unchanged.
   - Set `DB_URL` to a `postgres://` connection string (and optionally `DB_DRIVER=postgres`) to run on PostgreSQL instead. `DB_PATH` is ignored in that mode.
   - The 21 migrations are written once in SQLite dialect and translated on the way out — `DATETIME` → `TIMESTAMPTZ`, `INSERT OR IGNORE` → `INSERT ... ON CONFLICT DO NOTHING`, `?` → `$N` — so the two backends cannot drift apart across releases.
   - **Currently covers the schema and migration path.** The server's own query call sites still pass `?` straight to the driver rather than through `db.Rebind`, so running the full request path against PostgreSQL is not yet supported.

---

## 📦 Complete Enterprise Capabilities (Phases 1 - 15)

| Phase | Module | Core Functionality |
|---|---|---|
| **Phase 1** | **Core & Transport** | WebSocket TLS server/client, JWT authentication, RBAC authorization, revocable sessions, single-use WebSocket tickets, tamper-evident audit trail |
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
| **Phase 15** | **Device Maintenance** | Server-defined maintenance jobs (disk cleanup, temp purge, cache clearing) with per-device step progress and fleet-wide progress reporting |

### Interactive remote control runs as the signed-in user

A remote-control session has to inject input into a desktop the operator can see,
and the agent service does not have one: it runs in Session 0, where there is
no interactive desktop, no visible cursor and no way to click anything. So when a
session starts, the service asks Windows for the token of the console session
(`WTSQueryUserToken`), duplicates it into a primary token
(`DuplicateTokenEx(TokenPrimary)`), builds that user's environment block, and
spawns the capture worker with `CreateProcessAsUser` on `winsta0\default`.

Two consequences worth knowing before you use it:

- **The desktop belongs to whoever is logged in.** If nobody is signed in on the
  endpoint, there is no user token to borrow and the session is refused. This is
  the correct answer — see the fallback note below.
- **The worker is scoped to that session.** It runs in the console session's
  session id, not the service's, and it is terminated when the session closes.

### Silent uninstall refuses anything it cannot verify

Uninstalling from **Device → Installed Software** never opens a window on the
endpoint. The agent reads the program's own `UninstallString` /
`QuietUninstallString` from the registry and runs the quiet variant only. If the
program records no verifiable silent command, the request is **refused** and
nothing is executed — an operator gets a refusal in the console instead of a
dialog box appearing on someone's desktop. PostgreSQL, for example, is refused
with the reason attached.

The uninstall is a *request*, not a removal. The console says so plainly, and
the list only changes once the agent reports a new inventory snapshot: click
**Collect Inventory**, which waits for the agent's own `collected_at` to change
rather than assuming a fixed delay, so what you see afterwards is what the agent
actually found.

---

## 📂 Repository Directory Structure

```
.
├── agent/                          # Outbound agent source code (Pure Go)
│   ├── cmd/agent/                  # Multi-platform agent entrypoint; per-OS shims that
│   │                               # delegate to the collector package for that platform
│   ├── shared/                     # Modular shared agent subsystems
│   │   ├── enrollment/             # TLS token device enrollment
│   │   ├── inventory/              # OS hardware spec collection + staggered scheduler
│   │   ├── maintenance/            # Disk cleanup, temp purge, cache maintenance
│   │   ├── networkfilter/          # DNS sinkhole & hosts policy engine
│   │   ├── osinfo/                 # OS name/version/hostname collection
│   │   ├── patch/                  # OS patch scanning & silent installation
│   │   ├── remotecontrol/          # Remote desktop capture & mouse/keyboard replay
│   │   ├── remoteexec/             # Shell command execution & interactive terminal
│   │   ├── service/                # OS service registration (systemd, launchd, Windows SCM)
│   │   ├── software/               # Software installer & uninstaller runner
│   │   ├── transport/              # Outbound WebSocket client with keepalive
│   │   └── update/                 # Autonomous agent binary self-updater
│   ├── windows/  linux/  macos/    # Per-OS inventory collectors and OS info
├── deploy/                         # Production Ubuntu deployment automation
│   ├── install-ubuntu.sh           # One-click Ubuntu server setup script
│   ├── nginx-endpoint.conf.template# Parameterized Nginx reverse proxy (HTTPS + WebSocket)
│   ├── endpoint-mgmt.service       # Systemd daemon configuration
│   └── README-UBUNTU-DEPLOY.md     # Step-by-step production server setup guide
├── docs/                           # Architecture specifications & audit reports
│   ├── architecture/               # Phase 0 through Phase 14 technical blueprints
│   ├── installation/               # Server deployment guides (Linux, Windows, specs)
│   └── readiness-reports/          # Verification scorecards and production audits
├── packaging/                      # Agent & server installers
│   ├── windows/                    # NSIS installers + build scripts
│   ├── linux/                      # .deb package and Zenity/KDialog GUI installer
│   └── darwin/                     # Apple .pkg installer and GUI uninstaller
├── protocol/                       # The agent<->server wire contract, declared once
│                                   # and aliased by both transport packages
├── scripts/                        # Automated PowerShell E2E test suites
├── server/                         # Central management server source code (Pure Go)
│   ├── cmd/server/                 # Server entrypoint with embedded Web Console
│   │   ├── dist/                   # Production React build embedded at compile time
│   │   ├── main.go                 # HTTP server, routing, TLS, and graceful shutdown
│   │   └── web_embed.go            # Go embed.FS declaration
│   ├── core/                       # Core system foundations
│   │   ├── audit/                  # Tamper-evident audit logging service
│   │   ├── auth/                   # JWT, session store, login, rate limiting, origin policy
│   │   ├── config/                 # Environment-driven runtime configuration
│   │   ├── db/                     # SQLite WAL engine & sequential migrations (0001-0021)
│   │   ├── httpguard/              # Request body size limits
│   │   ├── logger/                 # zerolog setup and in-memory log tail
│   │   ├── rbac/                   # Role-Based Access Control context utilities
│   │   ├── transport/              # Hub WebSocket manager & agent dispatch
│   │   └── wsticket/               # Single-use WebSocket handshake tickets
│   └── modules/                    # Enterprise business logic modules (Phases 3-15)
│       ├── agentupdate/            # Phase 13: agent self-update & rollout campaigns
│       ├── alerting/               # Phase 9: rule evaluation, incidents, webhooks
│       ├── assetlicense/           # Phase 14: hardware assets & software licenses
│       ├── dashboard/              # Phase 3: executive fleet metrics
│       ├── device-management/      # Phases 1-2: enrollment, inventory, groups, lifecycle
│       ├── maintenance/            # Phase 15: device maintenance jobs & steps
│       ├── networkfilter/          # Phase 12: DNS/web filter policies
│       ├── patch-management/       # Phase 6: patch scanning & install jobs
│       ├── remote-exec/            # Phase 5: command execution & terminal relay
│       ├── remotecontrol/          # Phase 11: screen capture & input relay
│       ├── reports/                # Phase 8: streaming CSV/JSON exports
│       ├── software-deployment/    # Phase 4 (+15): package repository & rollouts
│       ├── taskscheduler/          # Phase 10: script repository & scheduled jobs
│       └── user-management/        # Phase 7: console users & role assignment
├── tests/                          # Go integration and unit test suites
├── web-console/                    # Modern React 19 + TypeScript + Vite Web Console
│   ├── src/
│   │   ├── components/             # Reusable UI widgets, charts, and interactive modals
│   │   ├── context/                # Authentication, theme & toast React contexts
│   │   ├── hooks/                  # Permission and shared hooks
│   │   ├── pages/                  # 16 operational views for all enterprise phases
│   │   ├── services/               # Typed REST API client & report download helpers
│   │   └── types/                  # Shared DTO types mirroring the server responses
│   ├── package.json
│   └── vite.config.ts              # Builds straight into server/cmd/server/dist
├── Dockerfile.server               # Container build for the management server
├── go.mod, go.sum, LICENSE, SECURITY.md, CONTRIBUTING.md
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

### Container

[`Dockerfile.server`](Dockerfile.server) builds the same single binary into a
distroless image, with the console embedded at image build time and no root
user. The database lives on a volume, so it survives a container replacement:

```bash
docker build -f Dockerfile.server -t endpoint-mgmt-server .

docker volume create endpoint-mgmt-data

docker run -d --name endpoint-mgmt \
  -p 8443:8443 \
  -e JWT_SECRET="$(openssl rand -hex 32)" \
  -e ADMIN_PASSWORD='change-me-before-first-boot' \
  -v endpoint-mgmt-data:/data \
  endpoint-mgmt-server
```

`JWT_SECRET` has no default and the server refuses to start without it. Set it
to the same value on every restart, or every issued token is invalidated.

### Full guides

| Guide | Covers |
|---|---|
| [`docs/installation/server-linux.md`](docs/installation/server-linux.md) | Ubuntu/Debian: systemd, TLS, firewall, backup verification, upgrade & rollback, hardening |
| [`docs/installation/server-windows.md`](docs/installation/server-windows.md) | Windows Server: service registration, TLS, firewall, backup, upgrade |
| [`deploy/README-UBUNTU-DEPLOY.md`](deploy/README-UBUNTU-DEPLOY.md) | Short-form Ubuntu quickstart |

---

## 🧪 Verification & Automated Testing

Run all Go tests — unit, integration and per-package — the same way CI does:
```bash
CGO_ENABLED=0 go test -count=1 ./...
```

The integration suites alone:
```bash
CGO_ENABLED=0 go test -v ./tests/integration/...
```

Run PowerShell end-to-end verification scripts against a running server and a
real agent. These are Windows-only and are deliberately **not** part of CI — they
need a live desktop, a real install, and a real endpoint, so they are a
before-release check rather than a per-commit one:

```powershell
# Core and device lifecycle
.\scripts\e2e-live.ps1           # Phase 1: core, transport, auth
.\scripts\e2e-inventory.ps1      # Phase 2: hardware inventory

# Operational modules
.\scripts\e2e-dashboard.ps1            # Phase 3: dashboard & console
.\scripts\e2e-software-deployment.ps1  # Phase 4: software rollouts
.\scripts\e2e-remote-exec.ps1          # Phase 5: execution & live terminal
.\scripts\e2e-patch-management.ps1     # Phase 6: patch scan & install
.\scripts\e2e-user-management.ps1      # Phase 7: users & RBAC
.\scripts\e2e-reports.ps1              # Phase 8: CSV/JSON exports
.\scripts\e2e-alerting.ps1             # Phase 9: rules & incidents
.\scripts\e2e-task-scheduler.ps1       # Phase 10: scripts & schedules
.\scripts\e2e-remote-control.ps1       # Phase 11: screen capture & input
.\scripts\e2e-network-filter.ps1       # Phase 12: DNS/web filter policies
.\scripts\e2e-agent-update.ps1         # Phase 13: agent self-update rollouts
.\scripts\e2e-asset-license.ps1        # Phase 14: assets & license seats
```

---

## 🔐 Security & Compliance

- **Role-Based Access Control**: Granular endpoint access authorization (`admin`, `technician`, `viewer`).
- **Cryptographic Hash Verification**: All scripts, binaries, and patch installers are hashed with SHA-256 before remote dispatch.
- **Audit Logging**: Every administrative action, command execution, and remote desktop connection is recorded with UTC timestamp, user ID, IP address, and change details.
- **Encrypted Communication**: Mandatory TLS 1.2+ encryption on all agent-to-server WebSocket connections and REST endpoints.
