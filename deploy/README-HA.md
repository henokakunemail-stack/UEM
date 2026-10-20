# Menjalankan Dua Server atau Lebih (High Availability)

Mulai v1.5.3, beberapa server node bisa melayani satu armada endpoint secara
bersamaan. Kalau satu node mati, node lain tetap melayani dan agent pindah
sendiri dalam hitungan detik.

## Syarat

| Komponen | Keterangan |
|---|---|
| **PostgreSQL** | Wajib. SQLite hanya untuk satu node. Lihat [README-POSTGRES.md](README-POSTGRES.md) untuk memindahkan data. |
| **Versi server sama** | Semua node harus versi yang sama. |
| **`JWT_SECRET` sama** | Token login yang dibuat satu node harus diterima node lain. |
| **`DATA_DIR` bersama** | Paket software, rilis agent dan wallpaper disimpan di sini. Harus berupa penyimpanan yang sama untuk semua node (mount NFS/SMB, atau volume bersama). |
| **Load balancer** | Nginx dengan [nginx-ha.conf.template](nginx-ha.conf.template). |
| **`TRUSTED_PROXIES`** | Isi dengan alamat load balancer, supaya pembatasan login per IP melihat IP asli user. |

## Cara kerjanya

- **Satu leader.** Hanya satu node yang menjalankan scheduler, evaluator alert,
  sweep dan pembersihan sesi. Kalau leader mati, node lain mengambil alih
  dalam beberapa detik. Pemilihannya memakai advisory lock PostgreSQL, tidak
  ada komponen tambahan.
- **Perintah lintas node.** Agent tersambung ke salah satu node. Perintah dari
  console yang masuk lewat node lain diteruskan otomatis ke node yang memegang
  agent itu (PostgreSQL `LISTEN/NOTIFY`).
- **Relay per device.** Remote control dan remote terminal membutuhkan socket
  operator dan socket agent di node yang sama. Template nginx mengarahkan
  semua lalu lintas satu device ke node yang sama (hash per device id).
- **Logout dan pencabutan sesi** langsung berlaku di semua node.
- **Lockout login** dihitung per akun di database: 10 kali gagal dalam
  15 menit mengunci akun 15 menit, di node mana pun percobaannya.

## Langkah pemasangan (contoh dua node)

1. Pindahkan data ke PostgreSQL ([README-POSTGRES.md](README-POSTGRES.md)).
2. Siapkan folder bersama, misalnya NFS di `/mnt/em-data`, dan mount di kedua
   node.
3. Di **setiap node**, isi file env:

   ```
   DB_URL=postgres://endpointmgmt:PASSWORD@10.0.0.5:5432/endpointmgmt?sslmode=require
   JWT_SECRET=<sama persis di semua node>
   DATA_DIR=/mnt/em-data
   TRUSTED_PROXIES=10.0.0.10
   HTTP_ADDR=:8443
   METRICS_TOKEN=<token-acak-panjang>
   ```

4. Pasang server di tiap node dan start servicenya.
5. Di load balancer, salin `nginx-ha.conf.template`, ganti `__DOMAIN__`,
   `__SSL_DIR__`, `__NODE1__` dan `__NODE2__`, lalu reload nginx.
6. **Cek:**

   ```bash
   curl -s http://10.0.0.11:8443/healthz   # node 1: version, node, leader
   curl -s http://10.0.0.12:8443/healthz   # node 2
   ```

   Tepat satu node harus menampilkan `"leader":true`.

## Update tanpa downtime

Update node satu per satu. Saat dihentikan, node lebih dulu menjawab 503 di
`/readyz` selama `SHUTDOWN_DRAIN` (default 5 detik) supaya load balancer
berhenti mengirim request. Setelah itu agent-nya diputus dengan rapi, dan agent
langsung tersambung ke node yang lain.

## Pemantauan

- `GET /healthz`: versi, id node, uptime, apakah leader.
- `GET /readyz`: status siap (503 saat drain atau database tidak terjangkau).
- `GET /metrics`: format Prometheus. Isinya request HTTP per route, latensi,
  agent tersambung, leader, relay aktif, pool database, dan perintah lintas
  node. Kalau `METRICS_TOKEN` diisi, kirim `Authorization: Bearer <token>`.

## Yang sudah diuji

Diuji pada dua node yang berbagi satu PostgreSQL:

- tepat satu leader;
- agent tersambung ke node A, dan perintah dari console lewat node B sampai ke
  agent lalu dijawab;
- leader dimatikan paksa, dan node lain menjadi leader dalam kurang dari 10
  detik.

Konfigurasi nginx HA belum diuji dengan nginx sungguhan; uji di lingkungan
staging sebelum dipakai di produksi.
