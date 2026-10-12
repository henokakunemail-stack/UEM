# Panduan Pembaruan (Update Guide) - UEM Server & Agent

Dokumen ini menjelaskan tata cara memperbarui (*update*) **Endpoint Management Server** dan **Agent** pada sistem operasi **Linux (Ubuntu / Debian)** dan **Windows Server**, mencakup pembaruan sistem dari repositori Git maupun biner rilis.

---

## 📌 Ringkasan Pembaruan Sistem Terkini

Pembaruan terkini mencakup peningkatan performa, perbaikan bug kritis, dan penguatan keamanan:

1. **Remote Control (UltraViewer / TightVNC Quality)**:
   - **Visual Rendering**: Mengatasi rendering bertahap (mozaik) dan kotak hitam (*black boxes*) dengan mengintegrasikan Win32 `CAPTUREBLT` (menangkap jendela berlapis/hardware-accelerated/transparan).
   - **Sinkronisasi Keyframe Instan**: Siklus canvas React diperbaiki agar frame awal 1080p tidak terbuang saat inisiasi koneksi. Operator otomatis mengirim event `{"type":"refresh"}` saat WebSocket tersambung.
   - **Konsolidasi Tile**: Membatasi fragmentasi perubahan desktop (`maxRects: 24`) dan otomatis menggabungkan area perubahan ke dalam satu bounding box rapat.
   - **Input Injection Fix**: Memperbaiki alignment ABI 64-bit pada Win32 `tagINPUT` (tepat 40 bytes) dan penanganan desktop attachment (`attachToInputDesktop()`). Klik mouse kiri, kanan, scroll wheel, dan keyboard input kini direspons instan oleh agent pada mode **Control**.
2. **Network & Web Filter Security**:
   - Kebijakan filter domain/IP mendukung mode **Whitelist** dan **Blacklist** (migrasi database `0023_network_filter_mode.sql`).
   - DNS proxy loopback internal agent untuk inspeksi dan proteksi lalu lintas jaringan.
3. **Device Management & Enrollment**:
   - Hostname dan sistem operasi perangkat otomatis sinkron ke server saat agent melakukan koneksi awal (*hello handshake*).
   - Installer agent mendukung flag `-enroll-only` untuk mencegah proses installer berhenti (*hang*) saat registrasi token.

---

## 🐧 1. Panduan Update di Linux (Ubuntu / Debian Server)

### Skenario A: Update dari Repositori Git (Source Code)

Jika server Anda di-deploy langsung dari repositori Git (misalnya di direktori `~/UEM`):

> **Catatan:** Aset Web Console hasil build (`server/cmd/server/dist/`) sudah tersimpan di dalam repositori Git. Anda **tidak wajib** menginstal Node.js/npm di server Linux jika hanya mengompilasi biner server Go.

```bash
# 1. Pindah ke direktori repositori
cd ~/UEM

# 2. Tarik kode terbaru dari cabang main
git fetch origin main
git checkout main
git pull origin main

# 3. Kompilasi biner server dan agent Linux (CGO-free)
export CGO_ENABLED=0
go build -ldflags="-s -w" -o server ./server/cmd/server
go build -ldflags="-s -w" -o agent ./agent/cmd/agent

# 4. Terapkan update ke service yang berjalan:
# Kasus 4A: Jika menggunakan direktori produksi /opt/endpoint-mgmt
sudo bash deploy/update-server.sh --binary-path ./server

# Kasus 4B: Jika service berjalan langsung dari ~/UEM via systemd
if systemctl is-active --quiet endpoint-mgmt 2>/dev/null; then
    sudo systemctl restart endpoint-mgmt
elif systemctl is-active --quiet endpoint-server 2>/dev/null; then
    sudo systemctl restart endpoint-server
else
    # Kasus 4C: Jika dijalankan manual / nohup
    killall server 2>/dev/null || true
    sleep 1
    nohup ./server -env-file .env >> server.log 2>&1 &
fi
```

### Skenario B: Update Instalasi Produksi Menggunakan Script Resmi

Jika server terinstal di `/opt/endpoint-mgmt` (mengikuti panduan `deploy/install-ubuntu.sh`):

```bash
# Menggunakan script updater bawaan dengan binary hasil compile lokal:
sudo bash deploy/update-server.sh --binary-path /path/ke/binary/server

# Atau jalankan script bawaan langsung dari repositori:
cd /opt/endpoint-mgmt
sudo bash deploy/update-server.sh
```

Fitur otomatis `deploy/update-server.sh`:
- Membuat backup snapshot database SQLite secara otomatis di `/opt/endpoint-mgmt/data/backups/`.
- Menjaga file konfigurasi `.env`, sertifikat SSL/TLS, dan credential tanpa risiko tertimpa.
- Memutar file biner lama ke `endpoint-mgmt-server.bak`.
- Menguji status kesehatan server (`/healthz`). Jika gagal start, otomatis melakukan *rollback* ke biner sebelumnya.

---

## 🪟 2. Panduan Update di Windows Server

### Skenario A: Update dari Repositori Git (Source Code)

Jalankan perintah berikut di **PowerShell (Run as Administrator)** pada direktori proyek:

```powershell
# 1. Tarik commit terbaru dari GitHub
git fetch origin main
git checkout main
git pull origin main

# 2. (Opsional) Build ulang Web Console jika terdapat perubahan styling/UI frontend
Push-Location "web-console"
try {
    npm run build
} finally {
    Pop-Location
}

# 3. Kompilasi biner Server dan Agent untuk Windows 64-bit
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
$Ldflags = "-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=1.4.0"

go build -ldflags="$Ldflags" -o "server.exe" ./server/cmd/server
go build -ldflags="$Ldflags" -o "server-windows-amd64.exe" ./server/cmd/server
go build -ldflags="$Ldflags" -o "agent.exe" ./agent/cmd/agent
go build -ldflags="$Ldflags" -o "agent-windows-amd64.exe" ./agent/cmd/agent

# Salin biner agent ke folder bin untuk service jika ada
if (Test-Path "bin\emagent.exe") {
    Copy-Item "agent.exe" "bin\emagent.exe" -Force
}

# 4. Restart proses Server
Get-Process -Name server -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
Start-Sleep -Seconds 1

# Jalankan server kembali dengan konfigurasi .env
Start-Process -FilePath ".\server.exe" `
    -ArgumentList "-env-file .env" `
    -WorkingDirectory (Get-Location).Path `
    -RedirectStandardOutput ".\server.log" `
    -RedirectStandardError ".\server_err.log"

# 5. Restart service Agent jika terpasang di mesin ini
if (Get-Service -Name endpoint-agent -ErrorAction SilentlyContinue) {
    Restart-Service -Name endpoint-agent -Force
}
```

### Skenario B: Update Windows Service Menggunakan Script Updater

Jika server dipasang sebagai Windows Service di `C:\Program Files\EndpointMgmtServer`:

```powershell
# Jalankan script updater otomatis dengan proteksi rollback dan backup:
.\packaging\windows\scripts\update-server.ps1 -BinaryPath .\server-windows-amd64.exe
```

---

## 🔍 3. Verifikasi Keberhasilan Update

Setelah server di-restart, lakukan verifikasi berikut:

### 1. Uji Endpoint Healthcheck
```bash
# Linux
curl -s http://localhost:8443/healthz

# Windows PowerShell
(Invoke-RestMethod -Uri "http://localhost:8443/healthz").status
```
Respon harus berupa: `{"status":"ok"}`.

### 2. Cek Koneksi Agent di Server Log
Buka `server.log` dan pastikan agent terhubung kembali dengan versi terbaru:
```text
INF server\core\transport\ws.go:425 > agent hello agent=1.4.0 capabilities=10 device=...
```

### 3. Uji Fitur Remote Control di Web Browser
1. Buka web console di browser (`http://localhost:8443` atau URL domain Anda).
2. Tekan `Ctrl + F5` (Hard Reload) untuk memastikan browser memuat bundle JavaScript terbaru (`index-DLyfYkQD.js`).
3. Buka menu **Devices** -> pilih perangkat -> klik **Remote Control**.
4. Pilih mode **Control**:
   - Pastikan layar desktop tampil bersih tanpa efek mozaik gambar atau kotak hitam pada jendela aplikasi.
   - Klik menu, taskbar, atau buka aplikasi di desktop remote untuk memverifikasi respon klik mouse dan tombol keyboard.
