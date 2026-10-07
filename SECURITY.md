# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 1.x.x   | :white_check_mark: |

Only the latest stable release receives security updates. Backporting to
earlier releases is not guaranteed.

## Reporting a Vulnerability

**Please do not open a public issue for security vulnerabilities.**

Instead, report vulnerabilities privately through GitHub Security Advisories
or send a detailed report to the repository maintainers. Include:

- A clear description of the vulnerability and its impact
- Steps to reproduce (minimal proof-of-concept if possible)
- The affected component(s) and version(s)
- Any mitigations you are aware of

Reports will be acknowledged within 48 hours. A fix timeline will be
communicated once the issue has been triaged.

## Disclosure Process

1. Report received and acknowledged
2. Vulnerability verified and impact assessed
3. Fix developed and tested
4. Advisory prepared
5. Fix released (minor/patch version bump)
6. Advisory published after users have had time to upgrade

## Security Architecture Notes

This platform was designed with the following threat model assumptions:

- **Outbound-only agent connections**: Branch firewalls need zero inbound
  ports. Agents initiate TLS WebSocket connections to the central server.
- **Device authentication**: Every agent request carries `X-Device-Id` and
  `X-Device-Secret`. The server verifies the SHA-256 hash of the secret
  against the enrolled device record. A secret bound to one device ID cannot
  be replayed against another.
- **WebSocket origin validation**: The three WebSocket upgraders (agent
  connect, remote terminal, remote control) enforce an allowlist of browser
  origins via the `ALLOWED_ORIGIN_DOMAINS` environment variable. The default
  (empty) trusts only same-host and loopback origins.
- **Agent self-update integrity**: Binary releases are identified by SHA-256.
  The agent validates the hash before executing a downloaded update.
- **Role-based access control**: Console operators are `admin`, `technician`,
  or `viewer`. Administrative actions and sensitive data exports require
  `admin` role.
- **Server-side session authority**: A console JWT is not self-authorising. The
  access token names a row in `auth_sessions`, and every authenticated request
  checks that the row is still live. Signing out, deactivating an account,
  resetting a password, or changing a role revokes the affected sessions, and
  the access token the console already holds stops working on its next request
  rather than waiting out its own TTL.
- **Forwarded-address trust**: `X-Forwarded-For` and `X-Real-IP` are honoured
  only when the connection came from a peer listed in `TRUSTED_PROXIES`
  (loopback is always trusted). From anybody else the peer address is used,
  which the remote end cannot choose for itself. Without this gate the login
  rate limiter is bypassable by writing a fresh forwarding header on every
  attempt, because every attempt then appears to come from a new address.
- **Audit logging**: Every administrative action, command execution, remote
  desktop session, and enrollment event is recorded with actor, timestamp, and
  details.

## Responsible Configuration

Deployers must ensure:

- `JWT_SECRET` is a high-entropy random string (64 hex chars recommended).
  The installer generates one automatically.
- `ALLOWED_ORIGIN_DOMAINS` is set only if the web console is served from a
  different hostname than the API. Leaving it empty is the safe default.
- `TRUSTED_PROXIES` lists the reverse proxy addresses that may set the
  forwarded-address headers. Leaving it empty trusts loopback only; set it to
  the proxy's IP or CIDR (e.g. `10.0.0.0/8`) when nginx or Caddy terminates the
  connection, or the rate limiter will see every proxied request as the same
  address and the forwarded address will be ignored.
- TLS certificates are valid, rotated before expiry, and private keys are
  stored with restrictive permissions (root-only read).
- The server database directory (`/opt/endpoint-mgmt/data` by default) is
  backed up regularly and access is restricted to the service user.
- Agent deployments use a code-signing certificate so Windows SmartScreen
  does not block the installer.

## Known gaps

Stated plainly, because a security policy that only lists strengths is not a
security policy:

- **Agents are not code-signed.** Releases are distributed unsigned, so Windows
  SmartScreen will warn on first run, and the self-update path verifies a
  SHA-256 hash but not a signature. A hash only proves the download matches what
  the server offered; it does not prove who built it. Sign the agent and the
  updater before treating either as a trust anchor.
- **The default installation is not hardened for the public internet.** The
  threat model assumes a trusted internal network. `JWT_SECRET`, TLS
  certificates, and the origin allowlist must be configured before the console
  is reachable from anywhere else.
- **Remote control inherits the console user's session.** The worker runs in the
  interactive session it borrows, so it can act as that user. Anyone able to
  start a session can act as whoever is signed in on that endpoint.