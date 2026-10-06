# Panduan Deployment Server Linux Ubuntu

## Enterprise Endpoint Management Platform

Dokumen ini memandu instalasi biner server pada **server fisik Linux Ubuntu**
(Ubuntu 20.04 / 22.04 / 24.04 LTS) maupun Debian 11/12.

> Untuk panduan yang lebih lengkap (hardening, upgrade, rollback, backup,
> troubleshooting) lihat [`docs/installation/server-linux.md`](../docs/installation/server-linux.md).
> Untuk server Windows, lihat [`docs/installation/server-windows.md`](../docs/installation/server-windows.md).

Nama domain **tidak di-hardcode** di dalam skrip. Anda menetapkannya sendiri
saat menjalankan installer, sehingga paket yang sama bisa dipakai organisasi mana pun.

---

## Isi Paket Deployment (`deploy/`)

| Nama File | Fungsi |
|---|---|
| `endpoint-mgmt-server-linux-amd64` | Biner tunggal Go server (Zero CGO, embedded Web Console SPA) |
| `install-ubuntu.sh` | Skrip instalasi otomatis (user, direktori, service, nginx, firewall) |
| `endpoint-mgmt.service` | Unit file Systemd daemon dengan proteksi NOFILE=65535 |
| `nginx-endpoint.conf.template` | Template reverse proxy Nginx (HTTPS + WebSocket persisten) |

---

## Langkah Cepat

### Langkah 1: Transfer File ke Server Ubuntu

```bash
# Dari komputer lokal / workstation Anda:
scp -r ./deploy/* user@ip-server-fisik:/tmp/deploy/
```

### Langkah 2: Siapkan Sertifikat TLS

Buat direktori sertifikat (nama default sudah dipakai `install-ubuntu.sh`):

```bash
sudo mkdir -p /etc/ssl/endpoint-mgmt
```

Salin sertifikat dan private key, **dengan nama setelah domain Anda**:

```bash
# Contoh domain: mgmt.example.com
sudo cp /path/ke/sertifikat.crt   /etc/ssl/endpoint-mgmt/mgmt.example.com.fullchain.crt
sudo cp /path/ke/sertifikat.key   /etc/ssl/endpoint-mgmt/mgmt.example.com.key

# Private key hanya boleh dibaca root:
sudo chmod 600 /etc/ssl/endpoint-mgmt/mgmt.example.com.key
sudo chmod 644 /etc/ssl/endpoint-mgmt/mgmt.example.com.fullchain.crt
```

Sertifikat wildcard pun bisa dipakai — salin dengan nama domain yang sama seperti di atas.

Alternatif: lewati langkah ini dan Gunakan certbot setelah instalasi.

### Langkah 3: Jalankan Installer

```bash
cd /tmp/deploy/
chmod +x install-ubuntu.sh

sudo ./install-ubuntu.sh --domain mgmt.example.com
```

Opsi lain yang tersedia:

| Flag | Fungsi |
|---|---|
| `--domain <nama>` | Hostname publik yang dipakai untuk `server_name` dan sertifikat (wajib, kecuali `--no-tls`) |
| `--email <alamat>` | Alamat kontak untuk perintah certbot yang ditampilkan di akhir |
| `--no-tls` | Lewati setup nginx/TLS sepenuhnya (untuk server di belakang reverse proxy lain) |

Skrip installer otomatis:

1. Menginstal paket `nginx`, `ufw`, `openssl`, dan `curl`.
2. Membuat user sistem terisolasi `endpointmgmt` (tanpa login shell).
3. Menyiapkan `/opt/endpoint-mgmt` dan storage SQLite WAL di `/opt/endpoint-mgmt/data`.
4. Men-generate `JWT_SECRET` acak 64 karakter dan password admin di `/opt/endpoint-mgmt/.env`.
5. Mendaftarkan service background `endpoint-mgmt.service` via Systemd.
6. Merender `nginx-endpoint.conf.template` untuk domain Anda dan mengaktifkannya.
7. Membuka port 22, 80, 443 pada firewall UFW.

> **Catatan:** `install-ubuntu.sh` hanya menulis `.env` bila belum ada. Jalankan ulang
> skrip dengan aman — password dan secret yang sudah ada tidak ditimpa.

### Langkah 4: Verifikasi

```bash
sudo systemctl status endpoint-mgmt
sudo systemctl status nginx
sudo journalctl -u endpoint-mgmt -f

# Health check langsung ke backend (melewati nginx):
curl -s http://127.0.0.1:8443/healthz
```

Jika sertifikat belum ada, nginx belum bisa reload. Setelah sertifikat ditaruh:

```bash
sudo nginx -t && sudo systemctl reload nginx
```

---

## Konfigurasi DNS

Di penyedia DNS Anda, tambahkan **A Record** untuk hostname yang dipilih:

```text
mgmt.example.com   IN   A   [IP-Publik-Server-Fisik]
```

Akses Web Console di:

```text
https://mgmt.example.com
```

Login memakai username `admin` dan password yang dicetak oleh installer
(atau lihat `/opt/endpoint-mgmt/.env`).

---

## Pemasangan Agen di Komputer Klien

Agen membuat koneksi **outbound 100%** ke server, jadi tidak ada port inbound
yang perlu dibuka di firewall kantor cabang.

1. Masuk ke Web Console, buka menu **Devices → Generate Enrollment Token**.
2. Jalankan installer/biner agen di komputer klien:

   ```cmd
   endpoint-agent.exe -server https://mgmt.example.com -enroll <TOKEN_DARI_WEB_CONSOLE>
   ```

3. Komputer klien terdaftar otomatis, mengirim inventaris hardware/software
   pertama, lalu tersambung permanen melalui
   `wss://mgmt.example.com/api/agent/connect`.

Untuk pemasangan massal lewat paket, lihat [`packaging/`](../packaging/README.md).
