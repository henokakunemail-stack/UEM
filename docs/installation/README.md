# Instalasi Endpoint Management

Panduan ini mencakup pemasangan server pusat, agen di komputer/perpisahan, dan
deployment lewat Docker.

## Mulai dari mana

| Yang ingin dipasang | Halaman |
|---|---|
| Server di Windows | [server-windows.md](server-windows.md) |
| Server di Linux | [server-linux.md](server-linux.md) |
| Agen di Windows | [agent-windows.md](agent-windows.md) |
| Agen di Linux | [agent-linux.md](agent-linux.md) |
| Server lewat Docker | [docker.md](docker.md) |
| Kebutuhan perangkat lunak | [prerequisites.md](prerequisites.md) |

Urutan yang benar: **server dulu, lalu agen**. Agen membutuhkan URL server dan
satu enrollment token yang hanya bisa dibuat setelah server berjalan.

---

## Satu hal yang paling sering membuat server gagal start

**Server tidak membaca file `.env` di working directory.** `config.Load()` hanya
memakai `os.LookupEnv`, jadi file `.env` yang diletakkan di samping biner akan
diabaikan tanpa pesan apa pun.

Satu-satunya cara file dibaca adalah flag `-env-file`:

```bash
endpoint-mgmt-server -env-file /opt/endpoint-mgmt/.env
```

Tiga aturan yang menyertainya:

1. **`-env-file` adalah fallback, bukan override.** Variabel yang sudah ada di
   environment proses menang. Kalau service dijalankan dengan
   `EnvironmentFile=` (systemd) atau blok environment SCM, file `.env` hanya
   mengisi kekosongan.
2. **`JWT_SECRET` wajib.** Kalau kosong, `config.Load()` keluar dengan
   `JWT_SECRET must be set`. Kalau `-env-file` dipakai dan nilainya kosong, server
   mengisinya sendiri pada run pertama lalu menulis balik ke file — jadi **jangan
   menghapus file itu** setelah install, atau seluruh sesi login yang sudah
   terbit menjadi tidak valid.
3. **Bangun `dist/` lebih dulu.** Web console di-embed ke biner server saat
   compile. Server yang dibangun tanpa `npm run build` tetap berjalan, tapi
   menyajikan console versi lama dari kompilasi sebelumnya.

---

## Verifikasi

Setelah server berjalan:

```bash
curl -s http://127.0.0.1:8443/healthz
# {"status":"ok","agents_online":0}
```

Lalu buka console di browser dan login dengan `admin` + password yang Anda
tetapkan di `ADMIN_PASSWORD`.

Untuk memastikan console yang disajikan memang yang terbaru, cari tahu nama
berkas JavaScript yang ada di `dist/`:

```bash
grep -o 'index-[A-Za-z0-9_-]*\.js' server/cmd/server/dist/index.html
```

Kalau biner Anda dibangun sebelum ada perubahan frontend, nama itu akan berbeda
dari yang ada di `dist/`. Solusinya selalu sama: `npm run build` di
`web-console/`, lalu bangun ulang biner servernya.
