# macOS Agent Packaging & GUI Uninstaller

Folder ini menyediakan spesifikasi dan skrip untuk membuat **macOS Flat Package (`.pkg`)** installer resmi dan **GUI Uninstaller Applet (`Uninstall.command`)**.

---

## 1. Komponen Installer macOS

- **Installer GUI Standar macOS (`EndpointAgent-1.0.0.pkg`)**:
  - Menggunakan wizard instalasi grafis bawaan macOS (*Apple Installer GUI*).
  - Memasang biner agen ke `/usr/local/bin/endpoint-agent` (Universal binary: Apple Silicon M1-M4 & Intel x86_64).
  - Mendaftarkan LaunchDaemon di `/Library/LaunchDaemons/com.endpoint-mgmt.agent.plist`.
  - Mengaktifkan daemon di latar belakang via `launchctl load -w`.
- **Uninstaller GUI Interaktif (`Uninstall.command`)**:
  - File skrip berkstensi `.command` yang dapat di-klik dua kali langsung di macOS Finder.
  - Menampilkan kotak dialog konfirmasi native AppleScript.
  - Meminta otentikasi administrator (Touch ID atau password Mac) melalui dialog sistem resmi macOS.
  - Menghentikan daemon `launchctl`, menghapus file LaunchDaemon, menghapus biner, dan membersihkan folder `/etc/endpoint-agent`.

---

## 2. Cara Membangun Paket di macOS

> **Catatan Penting**: Pembuatan biner installer `.pkg` membutuhkan utilitas native Apple (`pkgbuild` dan `productbuild`) yang hanya tersedia di lingkungan macOS (atau CI runner macOS seperti GitHub Actions `macos-latest`).

### Langkah Build di Mesin Mac:
1. Pastikan Xcode Command Line Tools terpasang:
   ```bash
   xcode-select --install
   ```
2. Pastikan Go 1.24+ terpasang di Mac.
3. Jalankan skrip build:
   ```bash
   cd packaging/darwin
   chmod +x build.sh gui-app/Uninstall.command
   ./build.sh
   ```
4. File installer `EndpointAgent-1.0.0.pkg` akan terbentuk dan siap didistribusikan.

---

## 3. Pendaftaran Token di Mac Klien Setelah Instalasi

Setelah paket `.pkg` diinstal di komputer Mac pengguna:
```bash
sudo endpoint-agent -server https://mgmt.perusahaan.com -enroll <TOKEN> -enroll-only -creds /etc/endpoint-agent/creds.json
sudo launchctl kickstart -k system/com.endpoint-mgmt.agent
```
Agen akan langsung terhubung ke konsol server via WebSocket TLS.
