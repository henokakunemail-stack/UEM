# Panduan Instalasi Server Windows (Windows Server 2016 - 2025)

Dokumen ini menjelaskan implementasi produksi server pusat **Endpoint Management** pada platform Windows Server (2016, 2019, 2022, 2025) arsitektur `x64`.

---

## 1. Persyaratan Sistem

- **Sistem Operasi**: Windows Server 2016, 2019, 2022, 2025, atau Windows 10/11 Pro/Enterprise 64-bit.
- **Hardware Minimum**: 2 vCPU, 4 GB RAM, 30 GB Disk Kosong (SSD/NVMe).
- **Jaringan**: IP statis, Port `443` (atau `8443`) dapat diakses oleh komputer agen di seluruh kantor cabang.
- **Sertifikat TLS**: Sertifikat SSL x509 (format PEM: `.crt` dan private key `.key`) atau sertifikat PFX yang diekstrak.

> Angka lengkap per profil, beserta cara mengukurnya di mesin Anda sendiri, ada di
> **[server-specs.md](./server-specs.md)**. Ringkasnya: 10.000+ endpoint butuh
> 8 vCPU / **32 GB** RAM (bukan 16 GB) dan NVMe, karena pembatasnya I/O database
> dan page cache — bukan jumlah inti CPU.

---

## 2. Struktur Direktori Rekomendasi

Buat hierarki folder di `C:\EndpointMgmt`:

```text
C:\EndpointMgmt\
├── bin\
│   └── endpoint-mgmt-server.exe     # Biner executable server
├── config\
│   └── .env                         # Konfigurasi environment
├── data\
│   ├── endpoint-mgmt.db             # Database SQLite WAL utama
│   ├── packages\                    # Repositori installer aplikasi
│   ├── agent-releases\              # File update biner agen
│   └── backups\                     # Direktori otomatis VACUUM INTO
└── certs\
    ├── server.fullchain.crt         # Sertifikat TLS
    └── server.key                   # Private Key TLS
```

Buka PowerShell sebagai **Administrator** dan jalankan:
```powershell
New-Item -ItemType Directory -Force -Path "C:\EndpointMgmt\bin"
New-Item -ItemType Directory -Force -Path "C:\EndpointMgmt\config"
New-Item -ItemType Directory -Force -Path "C:\EndpointMgmt\data\packages"
New-Item -ItemType Directory -Force -Path "C:\EndpointMgmt\data\agent-releases"
New-Item -ItemType Directory -Force -Path "C:\EndpointMgmt\data\backups"
New-Item -ItemType Directory -Force -Path "C:\EndpointMgmt\certs"
```

---

## 3. Kompilasi & Penempatan Biner

Bila mengompilasi dari source di workstation pengembang:
```powershell
# Di root repository:
$env:CGO_ENABLED="0"; $env:GOOS="windows"; $env:GOARCH="amd64"
go build -ldflags="-s -w" -o "C:\EndpointMgmt\bin\endpoint-mgmt-server.exe" ./server/cmd/server
```
Atau salin file `endpoint-mgmt-server.exe` hasil build ke `C:\EndpointMgmt\bin\`.

---

## 4. Konfigurasi Environment (`.env`)

Buat file konfigurasi `C:\EndpointMgmt\config\.env`. Gunakan script PowerShell berikut untuk men-generate secret acak yang kuat:

```powershell
$jwtBytes = New-Object byte[] 32
[Security.Cryptography.RNGCryptoServiceProvider]::Create().GetBytes($jwtBytes)
$jwtSecret = -join ($jwtBytes | ForEach-Object { "{0:x2}" -f $_ })

$adminPass = [System.Web.Security.Membership]::GeneratePassword(16, 2)
# Atau generator fallback tanpa dependensi:
if (-not $adminPass) {
    $adminPass = -join ((65..90) + (97..122) + (48..57) | Get-Random -Count 16 | ForEach-Object {[char]$_})
}

$envContent = @"
HTTP_ADDR=0.0.0.0:8443
DB_PATH=C:/EndpointMgmt/data/endpoint-mgmt.db
JWT_SECRET=$jwtSecret
LOG_LEVEL=info
LOG_FILE=C:/EndpointMgmt/logs/server.log
ADMIN_PASSWORD=$adminPass
ACCESS_TOKEN_TTL=30m
REFRESH_TOKEN_TTL=168h
AGENT_OFFLINE_AFTER=90s
ENROLLMENT_TTL=30m
BACKUP_INTERVAL=1h
BACKUP_RETAIN=24
BACKUP_DIR=C:/EndpointMgmt/data/backups

# Konfigurasi TLS Native (Jika tidak menggunakan Reverse Proxy IIS/Caddy):
# TLS_CERT_FILE=C:/EndpointMgmt/certs/server.fullchain.crt
# TLS_KEY_FILE=C:/EndpointMgmt/certs/server.key

# Domain origin konsol (opsional jika konsol diakses dari domain terpisah):
# ALLOWED_ORIGIN_DOMAINS=mgmt.perusahaan.com
"@

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText("C:\EndpointMgmt\config\.env", $envContent, $utf8NoBom)

Write-Host "Konfigurasi .env berhasil dibuat!" -ForegroundColor Green
Write-Host "KATA SANDI ADMIN AWAL: $adminPass" -ForegroundColor Yellow
Write-Host "Simpan kata sandi ini untuk login pertama di Web Console."
```

---

## 5. Menjalankan Server Sebagai Windows Service (NSSM)

Biner server Go dirancang sebagai proses background berkinerja tinggi. Agar berjalan otomatis saat komputer booting tanpa login user, gunakan **NSSM** (Non-Sucking Service Manager) yang teruji di enterprise:

### Langkah 5.1: Unduh & Pasang NSSM
1. Unduh NSSM dari `https://nssm.cc/download` (atau pasang via `winget install nssm` atau `choco install nssm`).
2. Tempatkan `nssm.exe` (versi 64-bit) di `C:\Windows\System32\` atau `C:\EndpointMgmt\bin\`.

### Langkah 5.2: Daftarkan Service
Jalankan perintah berikut di PowerShell Administrator:

```powershell
$nssmPath = "nssm.exe"
$serviceName = "EndpointMgmtServer"
$exePath = "C:\EndpointMgmt\bin\endpoint-mgmt-server.exe"
$workDir = "C:\EndpointMgmt\config"

# Pasang Service
& $nssmPath install $serviceName $exePath
& $nssmPath set $serviceName AppDirectory $workDir
& $nssmPath set $serviceName DisplayName "Endpoint Management Central Server"
& $nssmPath set $serviceName Description "Manages enterprise branch endpoints and remote execution relay."
& $nssmPath set $serviceName Start SERVICE_AUTO_START

# Konfigurasi Log Output (Rotasi log otomatis)
New-Item -ItemType Directory -Force -Path "C:\EndpointMgmt\logs"
& $nssmPath set $serviceName AppStdout "C:\EndpointMgmt\logs\server-out.log"
& $nssmPath set $serviceName AppStderr "C:\EndpointMgmt\logs\server-err.log"
& $nssmPath set $serviceName AppRotateFiles 1
& $nssmPath set $serviceName AppRotateOnline 1
& $nssmPath set $serviceName AppRotateSeconds 86400
& $nssmPath set $serviceName AppRotateBytes 52428800

# Jalankan Service
Start-Service $serviceName
Get-Service $serviceName
```

---

## 6. Opsi Terminasi TLS / HTTPS

### Opsi 1: Native Go TLS (Paling Sederhana)
Server Go memiliki dukungan TLS terintegrasi:
1. Simpan sertifikat di `C:\EndpointMgmt\certs\server.fullchain.crt` dan private key di `C:\EndpointMgmt\certs\server.key`.
2. Buka file `C:\EndpointMgmt\config\.env`, uncomment dan arahkan baris:
   ```env
   HTTP_ADDR=0.0.0.0:443
   TLS_CERT_FILE=C:/EndpointMgmt/certs/server.fullchain.crt
   TLS_KEY_FILE=C:/EndpointMgmt/certs/server.key
   ```
3. Restart service:
   ```powershell
   Restart-Service EndpointMgmtServer
   ```

### Opsi 2: Reverse Proxy via Caddy for Windows (Rekomendasi Otomatis Let's Encrypt)
Jika ingin sertifikat Let's Encrypt terkelola otomatis:
1. Unduh Caddy Windows binary (`caddy.exe`).
2. Buat `C:\EndpointMgmt\Caddyfile`:
   ```caddyfile
   mgmt.perusahaan.com {
       reverse_proxy 127.0.0.1:8443
   }
   ```
3. Jalankan Caddy sebagai service pendamping.

---

## 7. Konfigurasi Windows Firewall

Buka port listening (`8443` atau `443`) agar agen klien di luar server dapat terhubung:

```powershell
# Port 8443 (Backend API / Console)
New-NetFirewallRule -DisplayName "Endpoint Management Server (TCP-8443)" `
    -Direction Inbound -Protocol TCP -LocalPort 8443 -Action Allow

# Port 443 (Jika TLS langsung di port HTTPS standar)
New-NetFirewallRule -DisplayName "Endpoint Management Server (HTTPS-443)" `
    -Direction Inbound -Protocol TCP -LocalPort 443 -Action Allow
```

---

## 8. Verifikasi Operasional

```powershell
# 1. Cek port listening
Get-NetTCPConnection -LocalPort 8443, 443 -State Listen -ErrorAction SilentlyContinue

# 2. Cek endpoint healthz
Invoke-RestMethod -Uri "http://127.0.0.1:8443/healthz"
# Mengembalikan: status = "ok"

# 3. Pantau log real-time
Get-Content -Path "C:\EndpointMgmt\logs\server-out.log" -Tail 30 -Wait
```

---

## 9. Backup & Retensi Otomatis

Server secara otomatis membuat backup online SQLite snapshot via engine `VACUUM INTO` setiap jam:
- Lokasi: `C:\EndpointMgmt\data\backups\`
- Format: `endpoint-mgmt-backup-YYYYMMDD-HHMMSS.db`
- Retensi: Ditentukan oleh parameter `BACKUP_RETAIN=24` (24 backup terakhir disimpan, file lama otomatis dibersihkan).

### Verifikasi Backup di PowerShell:
```powershell
Get-ChildItem -Path "C:\EndpointMgmt\data\backups" | Sort-Object LastWriteTime -Descending | Select-Object -First 5
```

---

## 10. Prosedur Pembaruan (Upgrade) Biner di Windows

Karena Windows mengunci file executable yang sedang berjalan (*file locking*):
1. Unduh atau siapkan biner baru di `C:\EndpointMgmt\bin\endpoint-mgmt-server-new.exe`.
2. Hentikan service:
   ```powershell
   Stop-Service EndpointMgmtServer
   ```
3. Cadangkan dan ganti biner:
   ```powershell
   Move-Item -Path "C:\EndpointMgmt\bin\endpoint-mgmt-server.exe" -Destination "C:\EndpointMgmt\bin\endpoint-mgmt-server.exe.bak" -Force
   Move-Item -Path "C:\EndpointMgmt\bin\endpoint-mgmt-server-new.exe" -Destination "C:\EndpointMgmt\bin\endpoint-mgmt-server.exe" -Force
   ```
4. Nyalakan kembali service:
   ```powershell
   Start-Service EndpointMgmtServer
   Get-Service EndpointMgmtServer
   ```
