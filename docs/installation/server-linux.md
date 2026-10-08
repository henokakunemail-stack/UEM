# Panduan Instalasi Server Linux (Ubuntu / Debian)

Dokumen ini adalah panduan produksi untuk memasang biner server pusat **Endpoint Management** pada sistem operasi Ubuntu (20.04 / 22.04 / 24.04 LTS) atau Debian (11 / 12).

---

## 1. Persyaratan Sistem

- **Sistem Operasi**: Ubuntu 20.04+ LTS atau Debian 11+ (arsitektur `amd64` atau `arm64`)
- **Spesifikasi Minimum (500 Endpoint)**: 2 vCPU, 2 GB RAM, 20 GB Disk (SSD/NVMe disarankan)
- **Spesifikasi Rekomendasi (5.000 - 10.000+ Endpoint)**: 4 vCPU, 8 GB RAM, 100+ GB SSD (koneksi persistensi WebSocket & penyimpanan paket software/patch)
- **Jaringan**: IP publik statis, domain terdaftar (contoh: `mgmt.example.com`), port `80` dan `443` terbuka ke internet/kantor cabang

> Angka lengkap per profil, beserta cara mengukurnya di mesin Anda sendiri, ada di
> **[server-specs.md](./server-specs.md)**. Ringkasnya: 10.000+ endpoint butuh
> 8 vCPU / **32 GB** RAM (bukan 16 GB) dan NVMe, karena pembatasnya I/O database
> dan page cache — bukan jumlah inti CPU.

---

## 2. Pilihan Instalasi

### Metode A: Otomatis via Skrip (`deploy/install-ubuntu.sh`)

Paket `deploy/` telah menyediakan skrip otomatis yang mengatur user sistem, direktori data, unit systemd, reverse proxy Nginx, dan firewall.

```bash
# 1. Salin folder deploy ke server
scp -r ./deploy/* user@ip-server:/tmp/deploy/

# 2. Masuk ke server dan jalankan installer
ssh user@ip-server
cd /tmp/deploy
chmod +x install-ubuntu.sh

# Pasang dengan hostname publik Anda:
sudo ./install-ubuntu.sh --domain mgmt.perusahaan.com --email admin@perusahaan.com
```

Skrip akan menghasilkan kata sandi admin awal di terminal. Simpan kata sandi ini.

---

### Metode B: Instalasi Manual Langkah-demi-Langkah

Gunakan langkah manual jika server menggunakan topologi khusus atau hardened OS.

#### Langkah 1: Pasang Paket Dependensi
```bash
sudo apt-get update -y
sudo apt-get install -y nginx ufw openssl curl
```

#### Langkah 2: Buat User Sistem & Direktori
```bash
sudo useradd --system --home-dir /opt/endpoint-mgmt --shell /usr/sbin/nologin endpointmgmt

sudo mkdir -p /opt/endpoint-mgmt/data/packages
sudo mkdir -p /opt/endpoint-mgmt/data/agent-releases
sudo mkdir -p /opt/endpoint-mgmt/data/backups
sudo mkdir -p /etc/ssl/endpoint-mgmt
```

#### Langkah 3: Tempatkan Biner Server
Bangun biner Linux (`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o endpoint-mgmt-server ./server/cmd/server`) atau gunakan biner rilis, lalu salin:
```bash
sudo cp endpoint-mgmt-server /opt/endpoint-mgmt/endpoint-mgmt-server
sudo chmod 0755 /opt/endpoint-mgmt/endpoint-mgmt-server
```

#### Langkah 4: Buat File Konfigurasi Lingkungan (`.env`)
Buat file `/opt/endpoint-mgmt/.env`:
```bash
JWT_KEY=$(openssl rand -hex 32)
ADMIN_PASS=$(openssl rand -base64 12 | tr -d '/+=')

sudo bash -c "cat <<EOF > /opt/endpoint-mgmt/.env
HTTP_ADDR=127.0.0.1:8443
DB_PATH=/opt/endpoint-mgmt/data/endpoint-mgmt.db
JWT_SECRET=${JWT_KEY}
LOG_LEVEL=info
LOG_FILE=/var/log/endpoint-mgmt/server.log
ADMIN_PASSWORD=${ADMIN_PASS}
ACCESS_TOKEN_TTL=30m
REFRESH_TOKEN_TTL=168h
AGENT_OFFLINE_AFTER=90s
ENROLLMENT_TTL=30m
BACKUP_INTERVAL=1h
BACKUP_RETAIN=24
BACKUP_DIR=/opt/endpoint-mgmt/data/backups
# HTTP_ADDR di atas sudah 127.0.0.1, jadi nginx di localhost dipercaya otomatis.
# Isi hanya kalau nginx ada di mesin lain: TRUSTED_PROXIES=10.0.0.4
EOF"

sudo chmod 600 /opt/endpoint-mgmt/.env
sudo chown -R endpointmgmt:endpointmgmt /opt/endpoint-mgmt
echo "Password Admin Anda: ${ADMIN_PASS}"
```

#### Langkah 5: Konfigurasi Service Systemd
Salin file `deploy/endpoint-mgmt.service` ke `/etc/systemd/system/endpoint-mgmt.service`:
```ini
[Unit]
Description=Endpoint Management Central Server
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=endpointmgmt
Group=endpointmgmt
WorkingDirectory=/opt/endpoint-mgmt
EnvironmentFile=/opt/endpoint-mgmt/.env
ExecStart=/opt/endpoint-mgmt/endpoint-mgmt-server
Restart=always
RestartSec=5s
LimitNOFILE=65535
LimitNPROC=32768
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Aktifkan dan jalankan service:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now endpoint-mgmt
```

##### Kalau Anda menjalankan biner secara manual, bukan lewat systemd

`EnvironmentFile=` adalah fitur **systemd**, bukan fitur server. Server hanya
membaca environment proses (`os.LookupEnv`); tidak ada dotenv loader yang
membaca `.env` di working directory. Jadi unit di atas bekerja justru karena
systemd yang membaca file itu dan menyuntikkan hasilnya ke process environment.

Jalankan manual, Anda harus menyebut file-nya sendiri:

```bash
sudo -u endpointmgmt /opt/endpoint-mgmt/endpoint-mgmt-server \
  -env-file /opt/endpoint-mgmt/.env
```

Tanpa `-env-file`, semua variabel di file itu diabaikan dan server keluar dengan
`JWT_SECRET must be set`.

Ingat juga bahwa `-env-file` adalah **fallback, bukan override**: variabel yang
sudah ada di environment proses menang. Kalau dijalankan lewat systemd, itu
berarti `EnvironmentFile=` adalah sumber yang sesungguhnya, dan `-env-file`
tidak perlu ada di `ExecStart`.

#### Langkah 6: Konfigurasi TLS & Nginx Reverse Proxy
Tempatkan sertifikat SSL:
```bash
sudo cp /path/ke/cert.crt /etc/ssl/endpoint-mgmt/mgmt.perusahaan.com.fullchain.crt
sudo cp /path/ke/cert.key /etc/ssl/endpoint-mgmt/mgmt.perusahaan.com.key
sudo chmod 600 /etc/ssl/endpoint-mgmt/mgmt.perusahaan.com.key
```

Salin template Nginx `deploy/nginx-endpoint.conf.template`, sesuaikan variabel `__DOMAIN__` dan `__SSL_DIR__`:
```bash
sudo sed -e "s/__DOMAIN__/mgmt.perusahaan.com/g" \
         -e "s#__SSL_DIR__#/etc/ssl/endpoint-mgmt#g" \
         deploy/nginx-endpoint.conf.template > /etc/nginx/sites-available/mgmt.perusahaan.com

sudo ln -sf /etc/nginx/sites-available/mgmt.perusahaan.com /etc/nginx/sites-enabled/
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t && sudo systemctl reload nginx
```

Template ini sudah mengatur `X-Real-IP` dan `X-Forwarded-For`, tapi server
hanya membaca header itu bila koneksi datang dari peer yang ada di
`TRUSTED_PROXIES`. Nginx dan server biasanya satu kotak, jadi loopback
(127.0.0.1) sudah dipercaya secara default dan tidak perlu konfigurasi
tambahan. Kalau nginx ada di mesin lain, daftarkan alamatnya:

```bash
# Di EnvironmentFile server (/etc/endpoint-mgmt/config/.env)
TRUSTED_PROXIES=10.0.0.4
```

Tanpa itu, header diabaikan dan rate limiter login melihat semua request
nginx sebagai satu alamat — yang berarti pembatasan brute-force tidak
berlaku per-klien.

#### Langkah 7: Konfigurasi Firewall UFW
```bash
sudo ufw allow 22/tcp
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw --force enable
```

> **Agen tidak butuh port inbound.** Semua koneksi dari agen ke server adalah
> outbound. Buka hanya port server agar agen bisa menghubungi server.

---

## 3. Verifikasi Status Layanan

```bash
# Periksa status service aplikasi
sudo systemctl status endpoint-mgmt

# Tes endpoint healthcheck internal
curl -s http://127.0.0.1:8443/healthz
# Respon yang diharapkan: {"status":"ok","agents_online":0}

# Pantau log aktif
sudo journalctl -u endpoint-mgmt -f
```

---

## 4. Manajemen Backup & Retensi

Server Go secara otomatis menjalankan backup online SQLite berkala (`VACUUM INTO`) tanpa mengunci transaksi agen:
- **Lokasi Backup**: `/opt/endpoint-mgmt/data/backups/`
- **Format Nama**: `endpoint-mgmt-<YYYYMMDD>T<HHMMSS>Z.db` (UTC, contoh `endpoint-mgmt-20261008T091500Z.db`)
- **Konfigurasi Retensi**: Diatur melalui `BACKUP_INTERVAL=1h` dan `BACKUP_RETAIN=24` (menyimpan 24 jam backup snapshot terakhir).

### Backup Manual via CLI

Untuk backup di luar jadwal, atau sebelum upgrade:

```bash
sudo systemctl stop endpoint-mgmt          # lihat catatan di bawah
sudo -u endpoint-mgmt /opt/endpoint-mgmt/endpoint-mgmt-server \
  -env-file /opt/endpoint-mgmt/server.env \
  -backup /opt/endpoint-mgmt/data/backups/manual-$(date -u +%Y%m%dT%H%M%SZ).db
sudo systemctl start endpoint-mgmt
```

### Restore Manual via CLI

```bash
# 1. WAJIB hentikan service lebih dulu
sudo systemctl stop endpoint-mgmt

# 2. Restore (source = file backup, dest = DB_PATH aktif)
sudo -u endpoint-mgmt /opt/endpoint-mgmt/endpoint-mgmt-server \
  -env-file /opt/endpoint-mgmt/server.env \
  -restore /opt/endpoint-mgmt/data/backups/endpoint-mgmt-20261008T091500Z.db

# 3. Jalankan lagi service
sudo systemctl start endpoint-mgmt
```

Beberapa hal yang perlu diketahui sebelum menjalankan restore:

- **Service harus berhenti.** Database yang sedang dibuka tidak bisa diganti — di Windows error-nya `ERROR_SHARING_VIOLATION`, di Linux `rename ...: device or resource busy`. Restore pada service yang sedang berjalan akan gagal, dan pada Windows pesan errornya terlihat seperti database rusak padahal tidak.
- **Restore memvalidasi source dulu** dengan `PRAGMA quick_check`. File yang korup atau terpotong ditolak sebelum sempat menimpa database yang sehat.
- **File backup sumber tidak diubah.** Restore memvalidasi lewat salinan sementara, jadi artefak read-only tetap read-only — tidak ada migrasi yang berjalan di atasnya.
- **File `-wal` dan `-shm` di samping database aktif dihapus.** Sidecar itu milik database yang sedang diganti; membiarkannya membuat frame lama diterapkan di atas hasil restore.
- **Passing `-env-file` itu wajib saat dijalankan lewat service.** Tanpa itu, `DB_PATH` jatuh ke default relatif `data/endpoint-mgmt.db` dan restore akan membuat database baru di sana, bukan yang Anda maksud.

### Verifikasi Integritas File Backup:
```bash
# Periksa file backup terbaru
ls -lth /opt/endpoint-mgmt/data/backups/

# Cek integritas database snapshot menggunakan sqlite3 CLI (jika terpasang)
sqlite3 /opt/endpoint-mgmt/data/backups/endpoint-mgmt-*.db "PRAGMA integrity_check;"
```

---

## 5. Prosedur Upgrade & Rollback

### Upgrade Biner
1. Unggah biner baru ke `/tmp/endpoint-mgmt-server-new`.
2. Hentikan service sebentar:
   ```bash
   sudo systemctl stop endpoint-mgmt
   ```
3. Backup biner saat ini:
   ```bash
   sudo cp /opt/endpoint-mgmt/endpoint-mgmt-server /opt/endpoint-mgmt/endpoint-mgmt-server.bak
   ```
4. Ganti dengan biner baru dan perbaiki permission:
   ```bash
   sudo cp /tmp/endpoint-mgmt-server-new /opt/endpoint-mgmt/endpoint-mgmt-server
   sudo chmod 0755 /opt/endpoint-mgmt/endpoint-mgmt-server
   sudo chown endpointmgmt:endpointmgmt /opt/endpoint-mgmt/endpoint-mgmt-server
   ```
5. Mulai kembali service:
   ```bash
   sudo systemctl start endpoint-mgmt
   sudo systemctl status endpoint-mgmt
   ```

### Rollback
Jika terjadi masalah:
```bash
sudo systemctl stop endpoint-mgmt
sudo cp /opt/endpoint-mgmt/endpoint-mgmt-server.bak /opt/endpoint-mgmt/endpoint-mgmt-server
sudo systemctl start endpoint-mgmt
```
Migrasi database bersifat *forward-compatible* dan penambahan skema baru tidak merusak query versi sebelumnya.
