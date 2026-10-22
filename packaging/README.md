# Panduan Packaging & UI Installer Agen Endpoint

Repositori ini menyediakan berkas sumber installer dan uninstaller berbasis antarmuka grafis (GUI) serta paket distribusi resmi untuk tiga sistem operasi utama: **Windows**, **Linux**, dan **macOS**.

---

## Ringkasan Format Distribusi per OS

| Sistem Operasi | Format Installer GUI | Uninstaller GUI | Lokasi Sumber |
|---|---|---|---|
| **Windows** (x64) | `EndpointAgent-Setup.exe` (NSIS Wizard) | Windows Settings / Control Panel *Installed Apps* (`uninstall.exe`) | [`packaging/windows/`](windows/) |
| **Linux** (amd64, arm64) | `install-gui.sh` (Zenity / KDialog GUI) & `.deb` | `endpoint-agent-uninstall.desktop` / GUI Applet | [`packaging/linux/`](linux/) |
| **macOS** (Apple Silicon + Intel) | `EndpointAgent-macOS.pkg` (Apple Installer) & `endpoint-agent-darwin-<arch>.tar.gz` (`install-agent.sh`) | `endpoint-agent-uninstall` / `Uninstall.command` (AppleScript Native GUI) | [`packaging/darwin/`](darwin/) |

---

## Detail per Platform

### 1. Windows (`packaging/windows/`)
- Menggunakan **NSIS Modern UI 2**.
- Menampilkan form input untuk **Server URL** dan **Enrollment Token**.
- Menginstal ke `C:\Program Files\EndpointAgent\endpoint-agent.exe`.
- Menyimpan kredensial sistem di `C:\ProgramData\EndpointAgent\creds.json`.
- Mendaftarkan dan menjalankan Windows Service `endpoint-agent`.
- Terintegrasi langsung dengan menu Windows *Installed Apps* untuk pencabutan (uninstall) bersih.

### 2. Linux (`packaging/linux/`)
- Menyediakan **GUI Dialog Installer** (`install-gui.sh`) berbasis Zenity (GNOME/Ubuntu) atau KDialog (KDE).
- Menyediakan paket Debian standar (`.deb`) dengan skrip `postinst`, `prerm`, dan `postrm` untuk instalasi massal (APT/Ansible).
- Menambahkan ikon **Uninstall Endpoint Agent** di daftar aplikasi desktop Linux.

### 3. macOS (`packaging/darwin/`)
- Paket installer `.pkg` standar Apple yang mendukung arsitektur Universal (ARM64 M-series dan Intel).
- Mendaftarkan LaunchDaemon di `/Library/LaunchDaemons/com.endpoint-mgmt.agent.plist`.
- Menyertakan `Uninstall.command` yang memicu dialog otentikasi Touch ID / Admin AppleScript untuk uninstalasi bersih.

---

## Paket Server

| Sistem Operasi | Aset rilis |
|---|---|
| Windows (x64) | `EndpointServer-Setup.exe` (NSIS), `endpoint-server-windows-amd64.zip` (untuk `update-server.ps1`) |
| Linux (amd64, arm64) | `endpoint-server-linux-<arch>.tar.gz` (`install-server.sh`, `update-server.sh`) |
| macOS (amd64, arm64) | `endpoint-server-darwin-<arch>.tar.gz` (`install-server.sh`, LaunchDaemon) |

Setiap paket server membawa `agents/` berisi agen untuk kelima platform. Server memakainya untuk
installer satu baris Linux dan untuk **Agent Updates → Impor dari Paket Server**, sehingga agen yang
sudah terdaftar dapat di-update ke versi yang dibawa server tanpa unggah manual.

Semua aset dibangun oleh `scripts/build-release.ps1` (dipakai `scripts/publish-release.ps1` dan
`.github/workflows/release.yml`); hanya `EndpointAgent-macOS.pkg` yang dibangun di runner macOS.
`checksums.txt` mencakup semua aset.

---

## Catatan Build Lintas Platform
- Biner Go untuk seluruh 5 target arsitektur (`windows/amd64`, `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`) dikompilasi secara pure Go (`CGO_ENABLED=0`) tanpa dependensi compiler C eksternal.
- Pembuatan installer biner `.exe` (NSIS) membutuhkan `makensis` di host Windows.
- Pembuatan installer `.deb` membutuhkan `dpkg-deb` di host Linux.
- Pembuatan installer `.pkg` membutuhkan utilitas Apple (`pkgbuild` & `productbuild`) di host macOS.
