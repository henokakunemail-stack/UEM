# Progress — Platform Manajemen Endpoint

Dokumen ini adalah titik masuk tunggal. Sesi baru cukup membaca **file ini** untuk memahami
struktur, arsitektur, dan status pekerjaan, tanpa menelusuri seluruh repositori.

**Konteks saat ditulis:** 28 September 2026 · commit `7aec4ca` · branch `main` (sudah sama dengan
`origin/main`) · 20 commit.

**Aturan pemeliharaan:** jangan menebak angka di sini. Kalau belum dijalankan atau dibaca langsung,
tandai `belum diverifikasi`. Kalau ada yang salah, perbaiki dokumen ini — bukan memorize-nya.

---

## 1. Tujuan aplikasi

Platform manajemen endpoint terpusat. Satu server pusat, banyak agent (Windows, Linux, macOS) yang
terpasang di berbagai cabang. Server memantau, mengirim perintah, dan menerima laporan.

- **Skala sasaran:** 500 sampai 10.000 endpoint.
- **Pengguna:** operator TI internal, bukan produk komersial.
- **Masalah yang diselesaikan:** inventaris perangkat keras otomatis, pemasangan software massal,
  kepatuhan (patch, lisensi, standar aplikasi), dan kendali jarak jauh saat user tidak ada.

### Prinsip yang tidak boleh dilanggar

| Prinsip | Alasan |
|---|---|
| **Hanya koneksi keluar** | Agent selalu menghubungi server, tidak pernah dibalik. Nol port masuk di firewall cabang. |
| **Tanpa CGO, Go murni** | `CGO_ENABLED=0`, bisa dikompilasi ke 5 kombinasi sistem operasi dan arsitektur tanpa compiler C. |
| **Satu berkas executable** | Server berisi API sekaligus konsol web, ditanam lewat `embed.FS`. |
| **Tanpa jendela dialog di desktop** | Installer apa pun tidak boleh memunculkan antarmuka di mesin yang sedang dipakai user. |
| **SQLite mode WAL, satu penulis** | `SetMaxOpenConns(1)` untuk penulisan, pembacaan boleh paralel. |

---

## 2. Arsitektur

```
┌───────────────────────────┐   HTTPS / WebSocket keluar   ┌────────────────────────┐
│  Agent di cabang         │ ◄───────────────────────────► │  Server pusat          │
│  Windows / Linux / macOS │     heartbeat 20 detik        │                        │
│                           │     sambung ulang acak 1-60s  │  REST + WebSocket     │
│  • inventaris            │                               │  14 modul              │
│  • pemasangan software    │                               │  SQLite mode WAL       │
│  • pemindaian patch       │                               │  konsol React ditanam  │
│  • eksekusi jarak jauh   │                               └────────────────────────┘
│  • kendali jarak jauh    │
│  • penyaring jaringan    │
│  • pembaruan mandiri      │
│  • pemeliharaan          │
└───────────────────────────┘
```

Server tidak pernah menghubungi agent lebih dulu. Semua perintah mengalir lewat WebSocket yang
dipinthek oleh agent.

### Tata letak direktori

```
server/
  cmd/server/        main.go, web_embed.go, route_contract_test.go
  core/              auth, rbac, audit, config, db, logger, transport
  modules/           14 modul, satu folder per domain
agent/
  cmd/agent/         main.go, mendaftarkan semua penangan perintah
  shared/            transport, enrollment, inventory, software, patch,
                     remoteexec, remotecontrol, networkfilter, maintenance,
                     update, osinfo, service
  windows/ linux/ macos/   pemasang khusus tiap sistem operasi
web-console/
  src/pages/         16 halaman
  src/components/ui/ Modal, DataTable, ConfirmDialog
  src/index.css      3.357 baris, berbasis token
docs/
  architecture/      rencana fase-0 sampai fase-14
  readiness-reports/ laporan verifikasi tiap fase
scripts/             14 berkas e2e-*.ps1
data/                basis data lokal pengujian (jangan ikut commit)
```

### Empat belas modul server

`dashboard` · `device-management` · `software-deployment` · `remote-exec` · `remotecontrol` ·
`patch-management` · `user-management` · `alerting` · `taskscheduler` · `networkfilter` ·
`agentupdate` · `assetlicense` · `maintenance` · `reports`

### Tabel basis data

32 tabel domain: `devices` · `device_inventory` · `device_groups` · `device_group_members` ·
`software_packages` · `software_deployments` · `deployment_tasks` · `remote_executions` ·
`terminal_sessions` · `remote_control_sessions` · `device_patches` · `patch_install_jobs` ·
`users` · `audit_logs` · `alert_rules` · `alert_incidents` · `script_templates` · `task_schedules` ·
`scheduled_task_runs` · `scheduled_task_device_runs` · `filter_policies` · `filter_rules` ·
`device_filter_states` · `agent_releases` · `update_campaigns` · `device_update_tasks` ·
`hardware_assets` · `software_licenses` · `license_allocations` · `maintenance_jobs` ·
`maintenance_tasks` · `agent_commands`

Ditambah satu tabel khusus: `schema_migrations`.

Migrasi `0001_init` sampai `0014_maintenance`, ditanam di dalam berkas executable, dijalankan sekali
saat pertama kali, lalu dicatat di `schema_migrations`.

### Rute konsol

`/` pengalihan ke `/dashboard` · `/devices` · `/software/:tab?` · `/scheduler/:tab?` ·
`/assets/:tab?` · `/updates/:tab?` · `/patches` · `/maintenance` · `/remotecontrol` · `/filter` ·
`/alerts` · `/reports` · `/users` (admin) · `/audit` (teknisi) · `/log` (admin)

### Konfigurasi

- **Server**, dari lingkungan: `ADMIN_PASSWORD` · `JWT_SECRET` · `DB_PATH` · `HTTP_ADDR` · `LOG_LEVEL`
- **Agent**, dari argumen baris perintah: `-server` (atau `AGENT_SERVER`) · `-enroll` · `-creds` ·
  `-heartbeat` · `-service`

---

## 3. Alur pemasangan software

Bagian ini paling sering bermasalah. Pahami dulu sebelum mengubah apa pun.

```
KONSOL
  │  POST /api/software/packages       unggah berkas, hitung SHA-256, simpan
  │  POST /api/software/deployments    tentukan target, buat satu tugas per perangkat
  ▼
SERVER
  │  simpan software_deployments (status running)
  │  simpan deployment_tasks   (status pending)
  │  perangkat online  → hub.SendTo(perintah "software.install")
  │  perangkat offline → tugas tetap pending, tidak ada antrean tertunda
  ▼
AGENT  (penangan "software.install", jalan di goroutine)
  │  lapor "downloading"
  │  unduh dari /api/agent/packages/{id}/download  (header X-Device-Id dan X-Device-Secret)
  │  simpan ke berkas sementara, hitung SHA-256, bandingkan dengan yang dikirim
  │  lapor "installing"
  │  jalankan installer sesuai jenis paket
  │  lapor "success" atau "failed"  (kode keluar, log keluaran, pesan galat)
  ▼
SERVER
  perbarui deployment_tasks
  lalu hitung ulang status deployment dan simpan ke software_deployments
```

### Perjanjian yang harus dijaga

| Perjanjian | Nilai |
|---|---|
| Nama perintah agent | `software.install` |
| Endpoint laporan | `POST /api/agent/tasks/{id}/progress` |
| Header autentikasi agent | `X-Device-Id`, `X-Device-Secret` |
| Jenis paket Windows | `msi` · `exe` · `script` |
| Jenis paket Linux | `deb` · `rpm` · `script` |
| Jenis paket macOS | `pkg` · `script` |
| Status tugas | `pending` · `dispatched` · `downloading` · `installing` · `success` · `failed` |
| Status deployment | `running` · `completed` · `failed` · `cancelled` |
| Kode berhasil untuk MSI | `0` · `3010` (minta mulai ulang) · `1641` (mulai ulang sudah dimulai) |

**Penting.** Satu-satunya bundel frontend adalah `server/cmd/server/dist/`, ditanam lewat
`//go:embed dist/*`. `vite.config.ts` menulis ke sana dengan `emptyOutDir: true`. Mengubah berkas
TSX saja **tidak mengubah apa pun di browser**. Siklusnya: `npm run build` → `go build` → mulai
ulang server.

---

## 4. Cara memverifikasi (wajib dibaca sebelum mengklaim berhasil)

Empat langkah yang selama ini terbukti benar di proyek ini:

1. **Jalankan ke agent sungguhan.** Membaca kode lalu mengklaim berhasil adalah cara paling sering
   membuat kesalahan.
2. **Periksa akibat nyata, bukan hanya kode keluar.** Contoh: setelah memasang WinRAR, periksa
   daftar entri uninstall di registry dan berkas di Program Files.
3. **Periksa jendela popup.** `Get-Process | Where MainWindowTitle -ne ''` — kalau ada `winrar`
   atau `msiexec`, berarti pemasangan tidak senyap.
4. **Buka halamannya lewat peramban** untuk memastikan tampilan benar-benar muncul, bukan sekadar
   HTTP 200.

### Jebakan yang sudah pernah menimpa

| Jebakan | Akibatnya |
|---|---|
| Token JWT kedaluwarsa (sekitar 15 menit) | Semua pemeriksaan dapat 401. Bukan bug, masukkan ulang. |
| `/api/auth/me` memang tidak ada (404) | Itu normal, bukan bug. |
| Bentuk pemanggilan di `api.ts` | `route_contract_test.go` mengurai berkas itu. Jangan memecah jalur ke konstanta. |
| Galat autentikasi setelah contraseña berubah | Mulai ulang server dengan `ADMIN_PASSWORD` yang benar. |
| Teks antarmuka yang tidak ada di dalam bundel | Artinya yang sedang diuji adalah **build versi lain**, bukan build repositori ini. |

**Keputusan pengguna (28 September 2026):** bahasa antarmuka **tetap bahasa Inggris**. Semua
komunikasi antar-pengguna tetap bahasa Indonesia. Tidak ada pekerjaan penerjemahan.

---

## 5. Yang sudah selesai dan terverifikasi

| Bagian | Commit | Bukti |
|---|---|---|
| 14 fase platform, fase-0 sampai 14 | `d6368c6` | Laporan di `docs/readiness-reports/` |
| 20 commit, fitur dan perbaikan | — | `git log` |
| Sistem desain dan perutean | `c331b7b` | Gerbang CSS: seluruh kelas ada aturannya |
| Volume disk lengkap, C dan D | `e97661d` | Dua kartu disk tampil, nol galat konsol |
| Kendali jarak jauh hidup | `e97661d` | Kanvas 1920×1080, 10 bingkai/detik, 2,07 juta piksel bukan hitam |
| Terminal interaktif | terverifikasi | `term.data` menghasilkan `TERM_PROBE_OK` |
| Progres pemeliharaan | terverifikasi | `100.0%`, 2,46 GB berhasil dibebaskan |
| 17 rute dan 9 sub-tab terbuka | terverifikasi | Semua judul benar, nol galat |
| 69 jalur API tanpa galat 5xx | terverifikasi | `ok=69 bad=0` |
| Status deployment gagal semua | `951c61f` | Pengujian: 0% berhasil menjadi `failed`, bukan hijau |
| Pemasangan senyap dan validasi jenis | `7aec4ca` | WinRAR 7.23 terpasang, kode keluar 0, tanpa popup |

### Riwayat perbaikan

| Commit | Masalah |
|---|---|
| `0e651e1` | 7 penghambat produksi, termasuk keamanan tingkat tinggi |
| `c331b7b` | Menu pemeliharaan menampilkan layar putih kosong |
| `e97661d` | Hanya drive C yang terbaca; kanvas kendali jauh tanpa state |
| `951c61f` | Deployment yang gagal total tetap tampil hijau sebagai selesai |
| `7aec4ca` | `package_type` tidak diperiksa terhadap ekstensi berkas, sehingga `msiexec` keluar dengan kode 1620 |
| `7aec4ca` | Log installer korup, byte UTF-16LE mentah tersimpan sebagai teks |
| `7aec4ca` | Berkas `.exe` tanpa argumen memunculkan jendela dialog di desktop user |

---

## 6. Pekerjaan yang sedang berjalan

### 6.1 Uninstall senyap — belum ada kodenya

Sudah ditelusuri, hasilnya:

- Kolom `software_packages.uninstall_args` **sudah ada** di skema, tersimpan, dan dikirim API,
  tetapi **tidak pernah dipakai** sama sekali.
- Tidak ada perintah uninstall di `agent/cmd/agent/main.go`.
- Tidak ada antarmuka uninstall.
- Sudah dibuktikan bahwa `uninstall.exe` milik WinRAR dengan argumen `/s` berhasil uninstall senyap.

Jadi pekerjaan ini belum dimulai, hanya sudah dipetakan.

### 6.2 Cacat alur pemasangan yang sudah terbukti

Kelima cacat berikut akan muncul begitu banyak aplikasi dideploy ke fleet luas. Semuanya sudah
dibuktikan, belum diperbaiki.

| # | Cacat | Bukti | Dampak |
|---|---|---|---|
| 1 | Argumen berkutip dirusak `strings.Fields` | `/DIR="C:\Program Files\App"` terpecah menjadi dua | Terpasang ke lokasi salah, atau gagal |
| 2 | Tidak ada batas waktu proses | `runner_*.go` memakai `cmd.CombinedOutput()` polos | Installer dengan dialog menggantung selamanya |
| 3 | Tidak ada antrean atau pengunci | `installer.go` tidak memuat `sync.Mutex` | Dua installer berbaris-tabrak, kunci berkas, MSI gagal 1603 |
| 4 | Tidak ada batas ukuran keluaran | `CombinedOutput` menampung semuanya | Installer cerewet menghabiskan memori |
| 5 | Proses anak bisa terlantar | Matikan induk tidak otomatis anak | Installer tetap jalan setelah agent mati |

Alur analisis sedang berjalan di latar (tiga pemeriksa, tiga perancang, lalu penilaian).
Keputusan desain belum diambil. Jangan mulai implementasi sebelum hasilnya ada.

### 6.3 Rancangan uninstall (belum final)

- **Penemuan perangkat lunak**: Windows membaca registry
  `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall` beserta cerminnya di `WOW6432Node`.
  Field yang dipakai: `DisplayName`, `UninstallString`, `QuietUninstallString`, `DisplayVersion`.
  Linux memakai `dpkg-query` atau `rpm -qa`. macOS memakai `pkgutil`.
- **Penyimpanan data**: tidak perlu tabel baru, tapi struktur yang ada belum punya tempat untuk
  membedakan aksi pasang dari aksi lepas.
- **Pencocokan**: uninstall tidak punya berkas installer, jadi paket harus dicocokkan lewat nama
  dan versi terpasang.
- **Perintah senyap**: berbeda per sistem operasi dan per keluarga installer, dengan cadangan bila
  mode senyap memang tidak tersedia.

---

## 7. Tunggu keputusan pengguna

1. **Paket uji** `WinrarVerbatim` dan `WinrarNoArgs` masih ada di basis data lokal. Keduanya tidak
   pernah dideploy. Belum ada keputusan apakah dihapus.
2. **Bahasa antarmuka**: sudah ditegaskan tetap bahasa Inggris. Tidak ada pekerjaan penerjemahan.

---

## 8. Cara menjalankan

```bash
cd "D:/Henok/Projects/Desktop Manage"

# Server
JWT_SECRET="$(cat .testrun/test-console-jwt.txt)" \
DB_PATH='data/test-console.db' HTTP_ADDR=':8443' \
LOG_LEVEL='info' ADMIN_PASSWORD='<password>' ./server.exe

# Agent, dibangun dari repositori
go build -o agent.exe ./agent/cmd/agent
./agent.exe -server "http://127.0.0.1:8443" -creds "<berkas-kredensial>" -heartbeat 20
```

Masuk lewat `POST /api/auth/login` dengan `{"username": ..., "password": ...}`, ambil
`access_token`, lalu simpan di `localStorage` dengan kunci **`em_access_token`**.

Basis data pengujian lokal: `data/test-console.db`

---

## 9. Aturan kerja untuk sesi berikutnya

- **Jangan mengklaim berhasil tanpa menjalankan sungguhan.** Jalankan, lihat hasilnya, baru lapor.
- **Unggahan `.exe` dan `.msi` selalu butuh argumen senyap.** Sudah divalidasi di sisi server.
- **Jangan menambah pustaka baru.** Semua yang dibutuhkan sudah ada di `go.mod`.
- **Setiap perubahan frontend wajib `npm run build`.** Gerbang CSS gagal kalau ada nama kelas yang
  tidak punya aturan.
- **Jangan commit** `pic/`, `.testrun/`, `server.exe`, `data/`.
- **Pindai kebocoran kredensial sebelum commit.**
- **Jangan diubah:** kata sandi admin, JWT secret, dan skema basis data tanpa migrasi baru.

---

## 10. Ringkasan satu paragraf

Platform manajemen endpoint yang selesai 14 fase dan sudah diuji langsung ke agent sungguhan
beberapa kali. Alur pemasangan software sekarang bisa memasang WinRAR secara senyap tanpa popup di
desktop user, setelah empat sebab Independen ditemukan dan diperbaiki pada commit `7aec4ca`.
Yang sedang dikerjakan berikutnya: fitur uninstall senyap untuk mendukung kepatuhan terhadap
prosedur standar, sekaligus menutup lima cacat alur pemasangan yang akan meledak ketika banyak
aplikasi mulai dideploy ke fleet luas.
