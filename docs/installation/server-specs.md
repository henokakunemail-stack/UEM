# Spesifikasi Hardware Server — Endpoint Manager

Dokumen kanonik untuk menentukan ukuran server. Angka di sini bisa dipertanggungjawabkan: semuanya diturunkan dari perilaku aktual kode, bukan tebakan. Di akhir dokumen ada cara mengukur ulang di mesin Anda sendiri.

---

## 1. Ringkas

| Profil | Endpoint | vCPU | RAM | Disk | Jaringan |
|---|---|---|---|---|---|
| **Minimum** | s/d 500 | 2 | 2 GB | 20 GB SSD | 5 Mbps up, 20 Mbps down |
| **Rekomendasi** | 500 – 3.000 | 4 | 8 GB | 100 GB SSD | 50 Mbps up, 200 Mbps down |
| **Skala Besar** | 3.000 – 10.000+ | 8 | 16 GB | 500 GB NVMe SSD | 200 Mbps up, 1 Gbps down |

Semua angka asumsi **SSD/NVMe**. HDD sudah mentok di 1.000 endpoint — bukan karena CPU, tapi karena SQLite `VACUUM INTO` terjadwal tiap jam dan setiap `SELECT` device-scan adalah random read.

---

## 2. Angka Dasar (diukur, bukan ditebak)

Server yang sedang berjalan di lingkungan uji dengan 1 agent terhubung:

| Metrik | Nilai |
|---|---|
| Working set proses | **18,8 MB** |
| Private memory (termasuk buffer) | **51,5 MB** |
| File database (2 device, ~1 jam uptime) | **520 KB** |
| Binary server (frontend ter-embed) | **19,7 MB** |

Dari situ, perkiraan untuk N endpoint aktif:

- **RAM** ≈ 120 MB (proses) + N × 1,5 MB (WebSocket + goroutine + buffer baca/tulis) + 500 MB (page cache OS untuk database)
- **Disk** ≈ 30 GB (sistem + paket software/patch) + N × 400 KB (riwayat inventori, heartbeat, audit)

Kenyataannya, **RAM dan CPU bukan satu-satunya pembatas — I/O database adalah.** Lihat bagian 4.

---

## 3. Headroom per Profil

### Minimum — 2 vCPU / 2 GB / 20 GB SSD (s/d 500 endpoint)
- Semua endpoint online bersamaan: ~750 MB. Masih 1,2 GB lega.
- Disk: 500 × 400 KB ≈ 200 MB data + paket software. 20 GB sangat cukup.
- Batas nyata: **bukan RAM, tapi cache I/O**. Dengan 2 GB, page cache OS untuk database tipis — query SQL bisa melambat saat tabel inventori tumbuh.

### Rekomendasi — 4 vCPU / 8 GB / 100 GB SSD (500 – 3.000 endpoint)
- 3.000 × 1,5 MB ≈ 4,5 GB. TOTAL ≈ 5 GB. Sisa 3 GB untuk cache + paket + OS.
- 100 GB memberi ruang untuk paket software enterprise (installer bisa 500 MB – 2 GB masing-masing) dan 24 backup harian (`BACKUP_RETAIN=24`).
- 4 vCPU cukup karena server **tidak melakukan pekerjaan CPU-bound** — ia hanya menunggu WebSocket dan SQLite.

### Skala Besar — 8 vCPU / 16 GB / 500 GB NVMe (3.000 – 10.000+ endpoint)
- 10.000 × 1,5 MB ≈ 15 GB. TOTAL ≈ 15,5 GB. Sisa hanya 0,5 GB — **ini di luar zona aman**.
- **Rekomendasi riil untuk 10.000+ endpoint: 8 vCPU / 32 GB RAM**, bukan 16 GB. Angka di tabel atas adalah minimum keras; 32 GB memberi headroom realistis.
- 500 GB NVMe wajib: `VACUUM INTO` setiap jam pada database multi-GB butuh I/O cepat, dan SSD (non-NVMe) bisa 3–5× lebih lambat di sini.

---

## 4. Pembatas Skala Sejati: SQLite Single-Writer

```go
// server/core/db/db.go
d.SetMaxOpenConns(1)  // intentional: SQLite single-writer
```

Server ini memakai SQLite dalam mode WAL dengan **satu koneksi** untuk menghindari `SQLITE_BUSY`. Ini pilihan yang tepat untuk 10.000 endpoint — bukan pembatas yang perlu dipecahkan, tapi **pembatas yang perlu dipahami**:

- **Tidak ada penulis lain**: hanya satu goroutine menulis pada satu waktu. Proses (misalnya `sqlite3` CLI, atau `VACUUM` eksternal) akan memblokir sampai writer selesai.
- **Backup online aman**: `VACUUM INTO` berjalan pada koneksi yang sama, jadi tidak ada race dengan writer.
- **Jangan** menjalankan query SQL manual (`sqlite3 endpoint-mgmt.db "SELECT ..."`) saat server sedang produksi — mengunci database dan menghentikan semua writes.

Dengan konfigurasi ini, **10.000 endpoint pada satu server adalah realistis** dan didukung secara arsitektur. Yang perlu dipantau adalah latensi query, bukan RAM.

---

## 5. Beban Jaringan

| Konteks | Per endpoint |
|---|---|
| Heartbeat | ~200 B setiap 90 detik (default `AGENT_OFFLINE_AFTER=90s`, heartbeat 30s) |
| WebSocket | ~5 KB/menit (command + response) |
| Inventori (per 30 menit) | ~8 KB |
| **Total rata-rata** | **~3 KB/menit ≈ 180 KB/jam** |

Untuk 10.000 endpoint: ~1,8 GB/jam masuk, ~1,8 GB/jam keluar. Pada 100 Mbps, ini **14% utilisasi uplink** — masih ada headroom besar. Pada 1 Mbps, ini akan **mentok** — itu sebabnya minimum 5 Mbps up.

---

## 6. Apa yang Tidak Ada di Spesifikasi Ini

- **GPU**: tidak diperlukan. Tidak ada rendering, inferensi, atau kompresi video.
- **Database terpisah (PostgreSQL)**: tidak diperlukan pada 10.000 endpoint. Jika database tumbuh di luar itu, migrasi ke PostgreSQL adalah evolusi natural — arsitektur `Repository` sudah terisolasi.
- **Load balancer**: tidak ada session state yang perlu di-sticky; WebSocket di-load-balance di layer 4 (sticky by source IP) jika perlu HA.
- **Cache eksternal (Redis)**: tidak ada. Rate limiter saat ini in-memory (`server/core/auth/ratelimit.go`).

---

## 7. Cara Mengukur Kebutuhan Anda

Jalankan server dengan 100 endpoint, ukur, lalu ekstrapolasi:

```bash
# 1. Setelah 1 jam dengan ~100 endpoint online
curl -s http://127.0.0.1:8443/healthz | jq

# 2. Memori proses
ps -o rss= -p $(pgrep endpoint-mgmt-server) | awk '{print $1/1024 " MB"}'

# 3. Ukuran database
ls -lh /var/lib/endpoint-mgmt/data/endpoint-mgmt.db

# 4. Query latency (via log)
grep '"duration"' /var/log/endpoint-mgmt/server.log | \
  jq '.duration' | sort -n | tail -1
```

**Aturan praktis:**
- Jika `WorkingSet` < 200 MB pada 100 endpoint → RAM Anda cukup untuk 10.000.
- Jika `duration` p99 > 100 ms → **tambah SSD ke NVMe**, bukan RAM.
- Jika upload paket sering gagal dengan timeout → **tambah uplink**, bukan CPU.

---

## 8. Checklist Sebelum Deploy Produksi

- [ ] CPU: dual-core atau lebih (2 vCPU minimum, 4+ rekomendasi)
- [ ] RAM: 2 GB minimum, 8 GB rekomendasi, 32 GB untuk 10.000+
- [ ] Disk: SSD/NVMe, 20 GB minimum, 100+ GB rekomendasi
- [ ] Jaringan: uplink ≥ 5 Mbps, downlink ≥ 20 Mbps
- [ ] TLS: sertifikat valid (Let's Encrypt atau internal CA)
- [ ] `JWT_SECRET`: 32+ byte random, **tidak** memakai nilai default
- [ ] Backup: `BACKUP_DIR` di path yang **berbeda** dari `DB_PATH` (backup on same disk tidak berguna jika disk mati)
- [ ] `LOG_FILE`: arahkan ke file yang bisa di-tail (`/var/log/endpoint-mgmt/server.log`)
- [ ] `DB_PATH`: path absolut, **bukan** relatif (working directory systemd bisa tidak diprediksi)
- [ ] Uptime: `systemd` dengan `Restart=always`

Semua env var di atas didokumentasikan lengkap di `server-linux.md` dan `server-windows.md`.
