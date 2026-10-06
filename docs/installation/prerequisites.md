# Prasyarat dan Aplikasi Pendukung

Halaman ini hanya berisi perangkat lunak yang **benar-benar dibutuhkan**, dengan
alasan tiap satu. Bukan daftar paket yang mungkin berguna — kalau sebuah paket
tidak disebut di sini, sistem tidak memakainya.

---

## Server di Windows

**Tidak ada perangkat lunak tambahan yang wajib.** `packaging/windows/server.nsi`
sudah mendaftarkan Windows service sendiri lewat `-service install`, jadi NSSM
atau service manager pihak ketiga tidak diperlukan sama sekali.

Yang sudah ada di setiap Windows dan dipakai:

| Kebutuhan | Kenapa |
|---|---|
| PowerShell 5.1+ | Dipakai installer untuk menulis file konfigurasi. Default ada di Windows 10/11 dan Windows Server 2016+. |
| netsh / Windows Filtering Platform | Dipakai untuk aturan firewall server bila Anda mengonfigurasi filter manual. |

Yang perlu dipastikan oleh operator, bukan diinstall:

- **Port listening terbuka** ke jaringan agen's — `8443` (HTTP) atau `443` (TLS).
  Perintah ada di [server-windows.md](server-windows.md#7-konfigurasi-windows-firewall).
- **Izin Administrator** untuk mendaftarkan service. Tanpa itu, `-service install`
  ditolak SCM dan server tidak akan berjalan saat boot.

---

## Server di Linux

| Paket | Kenapa |
|---|---|
| `ca-certificates` | Terminalisasi TLS (Nginx/Caddy) memvalidasi rantai sertifikat dari sistem. Tanpa ini, sertifikat Let's Encrypt gagal diverifikasi dan reverse proxy menolak koneksi. |
| `nginx` atau `caddy` | Terminalisasi TLS di depan server. Server berbicara HTTP plaintext; taruh reverse proxy di depannya agar lalu lintas WAN terenkripsi. |
| `openssl` | Hanya untuk membuat `JWT_SECRET` dan `ADMIN_PASSWORD` acak saat pertama install. |
| `tar` atau `rsync` | Backup manual. Backup otomatis memakai SQLite `VACUUM INTO` dan tidak butuh keduanya — tapi menyalin DB mentah tanpa journaling sedang berjalan bisa menghasilkan berkas korup. |

---

## Agen di Windows

**Tidak ada paket tambahan.** WUA, PowerShell, dan service manager sudah ada di
sistem operasi.

Satu hal yang harus benar: **agen harus berjalan sebagai LocalSystem, atau sebagai
user yang punya hak Administrator.**

Ini bukan rekomendasi, ini syarat. Tiga fitur butuh hak administrator:

| Fitur | Yang terjadi tanpa hak admin |
|---|---|
| Filter jaringan | Penulisan aturan Windows Filtering Platform ditolak. Kolom Status di console akan menunjukkan firewall tidak aktif — bukan diam-diam. |
| Scan dan install patch | WUA dan installer yang berjalan sebagai user biasa akan ditolak Windows Update. |
| Remote exec / uninstall software | Registry per-machine dan MSI install tidak bisa ditulis oleh user biasa. |

`packaging/windows/agent.nsi` mendaftarkan service sebagai LocalSystem, jadi
jalur installer sudah benar. Yang perlu dicek: kalau Anda menjalankan
`endpoint-agent.exe` manual dari command prompt, jalankan dari PowerShell
yang di-elevate.

---

## Agen di Linux

| Paket | Kenapa |
|---|---|
| `nft` (nftables) | Backend filter jaringan. Tanpa `nft` di PATH, `Supported()` mengembalikan false dan filter tidak dijalankan. Status di console akan menandainya tidak aktif. |
| `apt-get` atau `dnf` atau `yum` | Scan patch. Tanpa salah satunya, scan **gagal dengan pesan error**, bukan melaporkan "tidak ada patch". Itu perbedaan yang disengaja: mesin tanpa package manager memang tidak bisa di-manage patch, dan itu harus terlihat sebagai kegagalan. |
| `ca-certificates` | Verifikasi sertifikat TLS server bila konektor memakai HTTPS. |

> Kalau Anda memasang agen Debian/Ubuntu lewat `.deb` dari `packaging/linux/`,
> dependensi di atas sudah dideklarasikan di `control` file-nya dan diurus
> otomatis.

---

## Untuk membangun dari sumber

Tidak ada Makefile di repo ini. Perintah build diambil dari `.github/workflows/ci.yml`
dan `packaging/*/build.sh`, bukan dari konvensi:

```bash
# Server + console (console harus dibangun lebih dulu, hasilnya di-embed)
cd web-console && npm ci && npm run build && cd ..

CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
  -o endpoint-mgmt-server ./server/cmd/server

# Agen per platform
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o endpoint-agent ./agent/cmd/agent
```

`CGO_ENABLED=0` adalah invarian proyek, bukan sekadar optimasi: driver SQLite
yang dipakai adalah `modernc.org/sqlite` yang murni Go, dan biner hasil build
harus bisa jalan di distroless base yang tidak punya libc dinamis.

Target agen yang dipakai CI: `linux/amd64`, `linux/arm64`, `windows/amd64`,
`windows/arm64`, `darwin/amd64`, `darwin/arm64`.
