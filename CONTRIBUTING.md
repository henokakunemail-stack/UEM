# Contributing to Endpoint Management Platform

Thank you for your interest in improving this project!

## Quick Links

- **Issues**: [GitHub Issues](https://github.com/henokakunemail-stack/Endpoint-Manager/issues)
- **Discussions**: [GitHub Discussions](https://github.com/henokakunemail-stack/Endpoint-Manager/discussions)
- **Documentation**: `docs/` directory

## How to Contribute

### 1. Reporting Bugs

- Search existing issues first to avoid duplicates
- Use the bug report template
- Include: Go version, OS, reproduction steps, expected vs actual behavior
- If possible, add a failing test case

### 2. Suggesting Features

- Open a discussion first for anything beyond a trivial change
- Explain the use case and why it belongs in core vs a plugin/extension
- Consider the maintenance burden of the proposed feature

### 3. Code Contributions

#### Prerequisites

- **Go 1.26.8+** — the version `go.mod` requires. An older toolchain will
  refuse to build the module.
- **Node.js 20+** for the web console (`web-console/`)
- SQLite development headers are **not** required — this project uses
  `modernc.org/sqlite` (pure Go)

#### Build & Test Locally

```bash
# Frontend (run once, or when web-console source changes)
cd web-console
npm install
npm run build

# Backend + agent (all targets)
CGO_ENABLED=0 go build ./...

# Run all tests
CGO_ENABLED=0 go test -count=1 ./...
```

Cross-compile the agent for a target with `GOOS`/`GOARCH`. There is no
build-all script; the exact commands are in the
[README](README.md#3-build-agent-executables-all-5-target-architectures):

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o agent-linux-arm64 ./agent/cmd/agent
```

#### Coding Standards

- **Go**: `gofmt` and `goimports`, enforced by
  [`.golangci.yml`](.golangci.yml); meaningful variable names; error wrapping
  with `%w`
- **TypeScript/React**: `oxlint` (`npm run lint`), functional components + hooks
- **Commits**: Conventional Commits (`feat:`, `fix:`, `chore:`, `docs:`,
  `refactor:`, `test:`)
- **Tests**: Table-driven where applicable; `*_test.go` in the same package;
  integration tests in `tests/integration/`

#### Pull Request Checklist

- [ ] `CGO_ENABLED=0 go build ./...` passes
- [ ] `CGO_ENABLED=0 go vet ./...` passes
- [ ] `CGO_ENABLED=0 go test -count=1 ./...` passes
- [ ] `golangci-lint run` is clean
- [ ] Frontend: `npm run lint && npm run build` (if web-console changed)
- [ ] New code has tests (unit or integration)
- [ ] Documentation updated (README, docs/, or code comments)
- [ ] No hardcoded secrets, domains, or machine-specific paths
- [ ] Commits are clean and follow Conventional Commits

### 4. Security Issues

See [SECURITY.md](SECURITY.md) — **do not** file public issues for
vulnerabilities.

## Project Structure Overview

```
.
├── agent/                    # Outbound agent (Pure Go, 5 targets)
│   ├── cmd/agent/            # Entrypoint; per-OS shims delegating to collectors
│   ├── shared/               # enrollment, inventory, maintenance, networkfilter,
│                             # osinfo, patch, remotecontrol, remoteexec, service,
│                             # software, transport, update
│   └── windows/ linux/ macos/# Per-OS inventory collectors and OS info
├── deploy/                   # Ubuntu/Debian server install bundle
│   ├── install-ubuntu.sh     # Parameterized installer
│   ├── nginx-endpoint.conf.template
│   └── endpoint-mgmt.service
├── docs/                     # Architecture specs, installation guides,
│   ├── architecture/         #   phase plans 0-14
│   ├── installation/         #   Linux, Windows, hardware specs
│   └── readiness-reports/    #   Phase 0-14 audit scorecards
├── packaging/                # Agent and server installers — implemented
│   ├── windows/              #   NSIS agent + server installers, build scripts
│   ├── linux/                #   .deb package, Zenity/KDialog GUI installer
│   └── darwin/               #   Apple .pkg installer, GUI uninstaller
├── protocol/                 # Agent<->server wire contract, defined once
├── scripts/                  # E2E PowerShell test suites
├── server/                   # Central management server (Pure Go)
│   ├── cmd/server/           # Entrypoint, embedded web console
│   ├── core/                 # audit, auth, config, db, httpguard, logger,
│   │                         # rbac, transport, wsticket
│   └── modules/              # Business logic (Phases 3-15)
├── tests/                    # Integration + unit test suites
├── web-console/              # React 19 + TypeScript + Vite SPA
├── Dockerfile.server         # Container image build for the server
├── start-server.ps1          # Local Windows start helper
├── progress.md               # Session handoff notes (not committed)
└── LICENSE, SECURITY.md, CONTRIBUTING.md
```

## Development Tips

- **Single-binary delivery**: The server embeds the React build via `embed.FS`.
  After any frontend change, run `npm run build` in `web-console/` so the
  embed is fresh on next `go build`.
- **Database migrations**: `server/core/db/migrations/0001_*.sql` — sequential,
  run once on first startup. Never edit an applied migration; add a new one.
- **Agent service layer**: `agent/shared/service/` provides `Manager`
  interface (systemd, launchd, Windows SCM). The Windows SCM handler uses
  `golang.org/x/sys/windows/svc` and runs the agent under the Service Control
  Manager — the agent binary must implement the handler contract.
- **Configuration over hardcode**: All hostnames, certificate paths, and
  credentials are environment variables. See `server/core/config/config.go`.

## License

By contributing, you agree that your contributions will be licensed under the
[MIT License](LICENSE).