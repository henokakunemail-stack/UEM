# Unified Endpoint Manager (UEM) - Release & Installation Guide

This document provides a clean, comprehensive installation and deployment guide for both **Server** and **Agent**, suitable for GitHub Releases, production environments, and source code builds.

---

## 📦 GitHub Release Assets Overview

Each official GitHub Release provides pre-compiled binaries and installers for the supported platforms:

| Component | Target OS / Architecture | Artifact Name | Description |
|---|---|---|---|
| **Server** | Windows Server x86_64 | `EndpointServer-Setup.exe` | NSIS GUI Installer (sets up Windows Service, ports, config) |
| **Server** | Windows Server x86_64 | `endpoint-server-windows-amd64.zip` | Standalone portable server binary + `.env.example` |
| **Server** | Linux x86_64 | `endpoint-server-linux-amd64.tar.gz` | Linux server binary + `deploy/` directory + systemd unit |
| **Server** | Docker / OCI Container | `ghcr.io/henokakunemail-stack/uem-server:<version>` | Multi-stage distroless container image |
| **Agent** | Windows 10/11 / Server x86_64 | `EndpointAgent-Setup.exe` | NSIS GUI Installer with Server URL & Enrollment prompt |
| **Agent** | Windows x86_64 | `endpoint-agent-windows-amd64.exe` | Standalone CLI agent binary |
| **Agent** | Linux Debian / Ubuntu x86_64 | `endpoint-agent_<version>_amd64.deb` | Debian package with auto-enabling systemd service |
| **Agent** | Linux Debian / Ubuntu ARM64 | `endpoint-agent_<version>_arm64.deb` | Debian ARM64 package |
| **Agent** | Linux x86_64 / ARM64 | `endpoint-agent-linux-<arch>.tar.gz` | Standalone binary + systemd service unit |
| **Agent** | macOS Universal (Intel & Apple Silicon) | `EndpointAgent-<version>.pkg` | macOS Installer Package with LaunchDaemon |

---

## 🚀 1. Server Installation & Setup

The central management server is a single, self-contained Go binary with the React Web Console embedded directly. It requires zero external dependencies by default (using high-concurrency SQLite WAL mode), with optional PostgreSQL backend support.

### Option A: Windows Server (GUI Setup or Service)

#### Method 1: Using NSIS Installer (`EndpointServer-Setup.exe`)
1. Download `EndpointServer-Setup.exe` from the GitHub Release.
2. Run the executable as Administrator.
3. Follow the wizard prompts:
   - **Installation Directory**: Default `C:\Program Files\EndpointManagerServer`.
   - **Port Configuration**: Default `8443` (HTTPS/WSS).
   - **Data Directory**: Default `C:\ProgramData\EndpointManager\data`.
   - **Administrator Password**: Set initial password for account `admin`.
4. The installer automatically:
   - Generates a cryptographically secure 32+ character `JWT_SECRET`.
   - Creates and starts the Windows Service `endpoint-mgmt-server` (Automatic startup).
   - Opens local firewall port `8443`.
5. Access web console at `https://localhost:8443` (or your configured FQDN).

#### Method 2: Manual Windows Service Setup (Portable Binary)
```powershell
# 1. Create target folder and extract binary
New-Item -ItemType Directory -Force "C:\EndpointServer"
Copy-Item "endpoint-server.exe" "C:\EndpointServer\"

# 2. Create configuration file C:\EndpointServer\.env
@'
PORT=8443
DATA_DIR=C:\EndpointServer\data
JWT_SECRET=your-secure-random-32-characters-or-longer-secret
ADMIN_PASSWORD=YourSecureAdminPassword123!
'@ | Out-File -FilePath "C:\EndpointServer\.env" -Encoding utf8

# 3. Register and start Windows Service
cd "C:\EndpointServer"
.\endpoint-server.exe -env-file .env -service install
.\endpoint-server.exe -service start
```

---

### Option B: Linux Server (Ubuntu / Debian Scripted)

The repository provides an automated, production-grade installation script for Ubuntu 20.04/22.04/24.04:

```bash
# 1. Download and extract server bundle
wget https://github.com/henokakunemail-stack/UEM/releases/download/v1.0.0/endpoint-server-linux-amd64.tar.gz
tar -xzf endpoint-server-linux-amd64.tar.gz
cd deploy

# 2. Run automated installer with your server domain/IP
sudo ./install-ubuntu.sh --domain uem.example.com
```

The script automatically performs:
- Creates isolated system user and group `endpointmgmt`.
- Provisions systemd service `/etc/systemd/system/endpoint-mgmt.service` with security sandboxing (`ProtectSystem=strict`, `NoNewPrivileges=true`).
- Sets up Nginx reverse proxy with TLS 1.2/1.3 and full-duplex WebSocket tunneling (`Upgrade` header with 24h timeouts).
- Configures UFW firewall for ports 80/443.

---

### Option C: Docker Container Deployment

Deploy using Docker with persistent storage and environment configuration:

```bash
# 1. Create persistent data volume
docker volume create uem-data

# 2. Run container
docker run -d --name uem-server \
  --restart unless-stopped \
  -p 8443:8443 \
  -e PORT=8443 \
  -e JWT_SECRET="$(openssl rand -hex 32)" \
  -e ADMIN_PASSWORD="SetYourSecurePasswordHere123!" \
  -v uem-data:/data \
  ghcr.io/henokakunemail-stack/uem-server:latest
```

---

## 💻 2. Agent Installation & Endpoint Enrollment

The agent communicates with the server via **outbound-only TLS WebSocket connections**. Endpoints require **0 inbound ports** and traverse NAT/corporate firewalls automatically.

### Prerequisite: Obtain an Enrollment Token
1. Open the Web Console (`https://your-server:8443`).
2. Log in as an Administrator or Technician.
3. Navigate to **Devices** → Click **Generate Enrollment Token**.
4. Copy the one-time registration token.

---

### Option A: Windows Endpoint Installation

#### Method 1: Interactive GUI Setup (`EndpointAgent-Setup.exe`)
1. Download `EndpointAgent-Setup.exe` on the client computer.
2. Run installer as Administrator.
3. Enter:
   - **Server URL**: `https://uem.example.com:8443`
   - **Enrollment Token**: The token copied from the Web Console.
4. Click **Install**. The installer validates the server connection, performs initial enrollment, writes credentials to `C:\ProgramData\EndpointAgent\creds.json`, and registers the background Windows Service `endpoint-agent`.

#### Method 2: Silent / Mass Deployment (GPO, SCCM, Intune, Script)
For automated mass rollout without GUI interaction:

```cmd
:: Enroll and register service in one step
endpoint-agent.exe -server https://uem.example.com:8443 -creds "C:\ProgramData\EndpointAgent\creds.json" -enroll <YOUR_ENROLLMENT_TOKEN>

:: Register and start Windows Service
endpoint-agent.exe -server https://uem.example.com:8443 -creds "C:\ProgramData\EndpointAgent\creds.json" -service install
endpoint-agent.exe -service start
```

---

### Option B: Linux Endpoint Installation (Debian / Ubuntu)

#### Method 1: Using Debian Package (`.deb`)
```bash
# 1. Install package
sudo dpkg -i endpoint-agent_1.0.0_amd64.deb

# 2. Enroll to server
sudo /usr/bin/endpoint-agent -server https://uem.example.com:8443 -enroll <YOUR_ENROLLMENT_TOKEN>

# 3. Enable and start systemd service
sudo systemctl enable --now endpoint-agent
```

#### Method 2: Standalone Binary / systemd Unit
```bash
# 1. Place binary in /usr/local/bin
sudo cp endpoint-agent /usr/local/bin/
sudo chmod +x /usr/local/bin/endpoint-agent

# 2. Enroll to management server
sudo /usr/local/bin/endpoint-agent -server https://uem.example.com:8443 -enroll <YOUR_ENROLLMENT_TOKEN>

# 3. Install as service
sudo /usr/local/bin/endpoint-agent -server https://uem.example.com:8443 -service install
sudo /usr/local/bin/endpoint-agent -service start
```

---

### Option C: macOS Endpoint Installation

#### Method 1: Apple Installer Package (`.pkg`)
1. Download `EndpointAgent-1.0.0.pkg`.
2. Double-click to install. This installs the binary to `/usr/local/bin/endpoint-agent` and provisions `/Library/LaunchDaemons/com.endpoint-mgmt.agent.plist`.
3. Open Terminal to enroll:
   ```bash
   sudo /usr/local/bin/endpoint-agent -server https://uem.example.com:8443 -enroll <YOUR_ENROLLMENT_TOKEN>
   sudo launchctl kickstart -k system/com.endpoint-mgmt.agent
   ```

---

## 🔨 3. Building From Source Code

To compile the entire system from source repository:

### Requirements
- **Go**: 1.23+ or 1.24+
- **Node.js**: 20+ or 22+ (LTS) & **npm**
- **Git**
- Optional: **NSIS** (Nullsoft Scriptable Install System) for compiling Windows `.exe` installers

### Step-by-Step Compilation

#### 1. Build the Web Console (Frontend SPA)
```bash
cd web-console
npm install
npm run build
cd ..
# Compiled frontend outputs directly to server/cmd/server/dist/
```

#### 2. Compile Server Executable
```bash
# Linux amd64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o endpoint-server ./server/cmd/server

# Windows amd64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o endpoint-server.exe ./server/cmd/server
```

#### 3. Compile Agent Executables (Multi-Platform)
```bash
# Windows x86_64
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o endpoint-agent.exe ./agent/cmd/agent

# Linux x86_64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o endpoint-agent-linux-amd64 ./agent/cmd/agent

# Linux ARM64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o endpoint-agent-linux-arm64 ./agent/cmd/agent

# macOS Universal (AMD64 & ARM64)
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o endpoint-agent-darwin-amd64 ./agent/cmd/agent
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o endpoint-agent-darwin-arm64 ./agent/cmd/agent
```

#### 4. Compile Windows Installers (NSIS)
```powershell
# Compile Server Setup Wizard
powershell -ExecutionPolicy Bypass -File packaging\windows\build-server.ps1

# Compile Agent Setup Wizard
powershell -ExecutionPolicy Bypass -File packaging\windows\build.ps1
```

---

## 📜 4. Clean Scripted Installation (Source Code Releases)

For users deploying directly from source code or extracting release archives without running full installer wizards, the repository provides standalone automated installation and uninstallation scripts:

### Windows Scripts (`packaging/windows/scripts/`)
| Script | Target | Purpose |
|---|---|---|
| `install-server.ps1` | Windows Server | Configures directories, `.env`, creates Windows Service, configures firewall port `8443`, and starts server. |
| `uninstall-server.ps1` | Windows Server | Stops service, deletes Windows Service, removes firewall rule, with optional database/data purge switch. |
| `install-agent.ps1` | Windows Endpoint | Enrolls endpoint against target server, writes credentials to ProgramData, registers and starts SCM service as `LocalSystem`. |
| `uninstall-agent.ps1` | Windows Endpoint | Stops and uninstalls `endpoint-agent` service with optional credential purge switch. |

### Linux Scripts (`packaging/linux/scripts/`)
| Script | Target | Purpose |
|---|---|---|
| `install-agent.sh` | Linux Endpoint (POSIX) | Installs binary to `/usr/local/bin`, enrolls with server URL & token, enables & starts systemd service. |
| `install-server.ps1` / `install-agent.ps1` | Linux (PowerShell Core) | Cross-platform PowerShell automation for Linux environments. |
| `deploy/install-ubuntu.sh` | Ubuntu Server | Complete production Nginx + systemd + TLS server provisioning script. |

### Standalone NSIS Uninstallers (`packaging/windows/*.nsi`)
- `EndpointServer-Uninstall.exe` (compiled from `packaging/windows/server-uninstall.nsi`): GUI wizard to stop service, delete registry keys, and safely remove server installation with optional configuration and database purge checkboxes.
- `EndpointAgent-Uninstall.exe` (compiled from `packaging/windows/agent-uninstall.nsi`): GUI wizard to stop agent, delete service, and optionally purge credentials.

---

## 🛡️ Service Management Quick Reference

Both `endpoint-server` and `endpoint-agent` binaries implement native OS service lifecycle commands:

| Command | Action |
|---|---|
| `-service install` | Registers binary into OS service manager (Windows SCM / systemd / launchd) |
| `-service uninstall` | Deregisters service from OS |
| `-service start` | Starts installed service |
| `-service stop` | Stops running service |
| `-service status` | Queries current execution status |
