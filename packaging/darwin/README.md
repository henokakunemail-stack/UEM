# macOS: Installer Agen & Server

Folder ini berisi sumber untuk tiga cara memasang di macOS (Apple Silicon dan Intel):

| Aset rilis | Isi | Dibangun oleh |
|---|---|---|
| `EndpointAgent-macOS.pkg` | Installer GUI Apple, biner agen universal (arm64 + x86_64) | job `macos-pkg` di `.github/workflows/release.yml` (butuh `pkgbuild`) |
| `endpoint-agent-darwin-<arch>.tar.gz` | `endpoint-agent`, `install-agent.sh`, `uninstall-agent.sh` | `scripts/build-release.ps1` (host apa pun) |
| `endpoint-server-darwin-<arch>.tar.gz` | `endpoint-server`, `install-server.sh`, `uninstall-server.sh`, `agents/` (agen untuk 5 platform) | `scripts/build-release.ps1` |

Semua jalur memakai label LaunchDaemon dan tata letak yang sama, sehingga perangkat yang dipasang
dengan cara mana pun di-update dan di-uninstall dengan cara yang sama:

- biner: `/usr/local/bin/endpoint-agent`
- kredensial: `/etc/endpoint-agent/creds.json`
- LaunchDaemon: `/Library/LaunchDaemons/com.endpoint-mgmt.agent.plist` (`KeepAlive` selama `creds.json` ada)
- log: `/var/log/endpoint-agent.log`, `/var/log/endpoint-agent.err`

---

## 1. Agen lewat `.pkg`

Pasang dengan klik dua kali atau `sudo installer -pkg EndpointAgent-macOS.pkg -target /`.
Setelah itu daftarkan perangkat:

```bash
sudo endpoint-agent -server https://mgmt.perusahaan.com -enroll <TOKEN> -enroll-only -creds /etc/endpoint-agent/creds.json
```

Daemon mulai berjalan sendiri begitu `creds.json` muncul.

**Pemasangan tanpa interaksi (MDM):** letakkan `/etc/endpoint-agent/enroll.conf` sebelum paket dipasang:

```
SERVER_URL=https://mgmt.perusahaan.com
ENROLL_TOKEN=<token dari konsol>
SITE=<opsional>
```

`postinstall` mendaftarkan perangkat lalu menghapus file itu (token hanya sekali pakai).
Kegagalan pendaftaran dicatat di `/var/log/endpoint-agent-install.log`.

## 2. Agen lewat tarball

```bash
tar -xzf endpoint-agent-darwin-arm64.tar.gz
sudo ./install-agent.sh https://mgmt.perusahaan.com <TOKEN> [site]
```

Skrip memilih arsitektur dari CPU (`hw.optional.arm64`), bukan dari `uname -m`, karena shell Rosetta
melaporkan `x86_64` di Apple Silicon.

## 3. Uninstall agen

Perangkat yang terdaftar hanya bisa dicopot dengan token uninstall dari konsol:

```bash
sudo endpoint-agent-uninstall <TOKEN>          # dipasang lewat .pkg
sudo ./uninstall-agent.sh <TOKEN>              # dari tarball
```

`gui-app/Uninstall.command` menyediakan alur yang sama dengan dialog AppleScript.

## 4. Server di macOS

```bash
tar -xzf endpoint-server-darwin-arm64.tar.gz
sudo ./install-server.sh :8443 <password-admin-awal>
```

Server dipasang di `/usr/local/endpoint-server` (dengan `agents/` di sebelahnya), konfigurasi di
`/usr/local/etc/endpoint-mgmt-server/server.env`, data di `/usr/local/var/endpoint-mgmt-server`,
LaunchDaemon `com.endpoint-mgmt.server`. `server.env` yang sudah ada dipertahankan.
`sudo ./uninstall-server.sh` mencopot program; `--purge` juga menghapus database dan konfigurasi.

## 5. Update agen dari konsol

Agen macOS yang sudah terdaftar di-update dari **Agent Updates**: **Impor dari Paket Server**
mendaftarkan `agents/agent-darwin-*` dari paket server sebagai rilis, lalu kampanye mengirim biner
yang cocok dengan arsitektur tiap perangkat. Agen mengunduh, memverifikasi checksum (dan tanda tangan
bila dikonfigurasi), mengganti dirinya, lalu di-restart oleh launchd lewat label di `XPC_SERVICE_NAME`.

---

## Membangun `.pkg` secara lokal (di Mac)

```bash
xcode-select --install          # pkgbuild, productbuild, lipo
bash packaging/darwin/build.sh  # -> packaging/darwin/EndpointAgent-<versi>.pkg
```

Variabel opsional: `VERSION`, `AGENT_DARWIN_ARM64` / `AGENT_DARWIN_AMD64` (biner prebuilt; job rilis
memakai biner yang sama dengan aset rilis), `PKG_OUTPUT`.
