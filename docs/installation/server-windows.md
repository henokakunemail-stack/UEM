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

# Reverse proxy (IIS/Caddy/nginx) di depan server? Daftarkan alamat/_CIDR-nya
# di sini, jika tidak header X-Forwarded-For diabaikan dan rate limiter login
# melihat semua request proxy sebagai satu alamat:
# TRUSTED_PROXIES=127.0.0.1
"@

$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText("C:\EndpointMgmt\config\.env", $envContent, $utf8NoBom)

Write-Host "Konfigurasi .env berhasil dibuat!" -ForegroundColor Green
Write-Host "KATA SANDI ADMIN AWAL: $adminPass" -ForegroundColor Yellow
Write-Host "Simpan kata sandi ini untuk login pertama di Web Console."
```

---

## 5. Menjalankan Server Sebagai Windows Service

Server berjalan sebagai proses background. Agar hidup otomatis saat boot tanpa
perlu login user, daftarkan sebagai Windows service.

**Tidak perlu NSSM.** Biner ini punya mode `-service` bawaan yang terdaftar
sendiri lewat Windows SCM. `packaging/windows/server.nsi` memakainya persis
seperti di bawah, jadi installer resmi dan langkah manual berikut melakukan hal
yang sama.

### Langkah 5.1: Pastikan file konfigurasi ada

Service yang dijalankan SCM mewarisi **tidak ada** environment dari shell Anda.
Artinya file `.env` harus punya `-env-file` yang eksplisit. Tanpa flag itu,
seluruh file diabaikan diam-diam, `JWT_SECRET` kosong, dan server keluar dengan
`JWT_SECRET must be set` tanpa pernah membaca satu baris pun dari konfigurasi
Anda.

> Ini bukan peringatan teoretis. Jalur NSSM yang pernah ada di dokumen ini
> mendaftarkan service **tanpa** `-env-file`, sehingga langkah "edit `.env`"
> sesudahnya tidak pernah berefek apa pun.

### Langkah 5.2: Daftarkan service

Jalankan di PowerShell **Administrator**:

```powershell
$serviceName = "EndpointMgmtServer"
$exePath     = "C:\EndpointMgmt\bin\endpoint-mgmt-server.exe"
$envFile     = "C:\EndpointMgmt\config\.env"

# -env-file wajib: SCM tidak mewarisi environment dari shell Anda
& $exePath -env-file $envFile -service install
& $exePath -service start

Get-Service $serviceName
```

Perintah yang tersedia:

| Perintah | Fungsi |
|---|---|
| `-service install` | Daftarkan service dengan `-env-file` yang diberikan |
| `-service start` / `-service stop` | Kontrol service |
| `-service status` | Lihat status |
| `-service uninstall` | Hapus service (data tidak dihapus) |

### Langkah 5.3: Rotasi log

Service menulis ke `LOG_FILE` yang ada di file `.env` — bukan ke stdout. Kalau
Anda butuh rotasi yang dikelola otomatis, biarkan Task Scheduler atau tool log
lain yang mengarsipkan; service ini tidak melakukan rotasi sendiri.

Kalau Anda lebih suka stdout, jalankan biner di luar service dengan stdout yang
di-redirect ke file, dan rotasikan dengan tool yang Anda inginkan.

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

> **Agen tidak butuh port inbound.** Semua koneksi dari agen ke server adalah
> outbound. Yang perlu dibuka hanyalah port server agar agen bisa
> **_menghubungi_** server — bukan agar server bisa menghubungi agen.

---

## 8. Verifikasi Operasional

```powershell
# 1. Cek port listening
Get-NetTCPConnection -LocalPort 8443, 443 -State Listen -ErrorAction SilentlyContinue

# 2. Cek endpoint healthz
Invoke-RestMethod -Uri "http://127.0.0.1:8443/healthz"
# Mengembalikan: status = "ok"

# 3. Pastikan service benar-benar membaca konfigurasi Anda
#    (kalau JWT_SECRET kosong, server tidak akan start sama sekali)
& "C:\EndpointMgmt\bin\endpoint-mgmt-server.exe" -env-file "C:\EndpointMgmt\config\.env" -service status

# 4. Pantau log — ke LOG_FILE, bukan ke stdout
Get-Content -Path "C:\EndpointMgmt\logs\server.log" -Tail 30 -Wait
```

> Kalau langkah 2 gagal dengan `JWT_SECRET must be set`, service berjalan
> **tanpa** `-env-file`. Periksa baris `-service install` di Langkah 5.2.

---

## 9. Backup & Retensi Otomatis

Server secara otomatis membuat backup online SQLite snapshot via engine `VACUUM INTO` setiap jam:
- Lokasi: `C:\EndpointMgmt\data\backups\`
- Format: `endpoint-mgmt-<YYYYMMDD>T<HHMMSS>Z.db` (UTC, contoh `endpoint-mgmt-20261008T091500Z.db`)
- Retensi: Ditentukan oleh parameter `BACKUP_RETAIN=24` (24 backup terakhir disimpan, file lama otomatis dibersihkan).

### Backup Manual via CLI

```powershell
Stop-Service EndpointMgmtServer
& "C:\EndpointMgmt\bin\endpoint-mgmt-server.exe" `
  -env-file "C:\EndpointMgmt\server.env" `
  -backup "C:\EndpointMgmt\data\backups\manual-$(Get-Date -AsUTC -Format 'yyyyMMddTHHmmssZ').db"
Start-Service EndpointMgmtServer
```

### Restore Manual via CLI

```powershell
# 1. WAJIB hentikan service lebih dulu -- database yang sedang dibuka
#    tidak bisa diganti di Windows (ERROR_SHARING_VIOLATION)
Stop-Service EndpointMgmtServer

# 2. Restore (source = file backup, dest = DB_PATH aktif)
& "C:\EndpointMgmt\bin\endpoint-mgmt-server.exe" `
  -env-file "C:\EndpointMgmt\server.env" `
  -restore "C:\EndpointMgmt\data\backups\endpoint-mgmt-20261008T091500Z.db"

# 3. Jalankan lagi service
Start-Service EndpointMgmtServer
```

Sebelum menjalankan restore, perhatikan:

- **Service harus berhenti.** Ini bukan rekomendasi, tapi syarat. Go dan SQLite membuka file database tanpa `FILE_SHARE_DELETE`, jadi Windows menolak penggantian file yang sedang dibuka dengan `ERROR_SHARING_VIOLATION`. Pesan error itu terlihat seperti database rusak, padahal tidak — dan именно itu kondisi paling umum penyebabnya.
- **Restore memvalidasi source dulu** dengan `PRAGMA quick_check`, dan menolak file korup atau terpotong sebelum sempat menimpa database yang sehat.
- **File backup sumber tidak diubah.** Validasi berjalan di salinan sementara, jadi artefak read-only tetap read-only.
- **File `-wal` dan `-shm` di samping database aktif dihapus.** Sidecar itu milik database yang diganti; membiarkannya membuat frame lama menimpa hasil restore secara diam-diam.
- **Passing `-env-file` itu wajib saat service.** Tanpa itu, `DB_PATH` jatuh ke default relatif `data\endpoint-mgmt.db` dan restore akan membuat database baru di sana, bukan yang Anda maksud.

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
