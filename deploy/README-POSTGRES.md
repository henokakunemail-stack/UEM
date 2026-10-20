# Pindah dari SQLite ke PostgreSQL

SQLite cukup untuk satu server. Untuk menjalankan **dua server atau lebih**
(lihat [README-HA.md](README-HA.md)), database harus PostgreSQL. Server punya
perintah bawaan untuk memindahkan seluruh data.

## Yang dilakukan perintah ini

`server -migrate-to-postgres`:

1. Membuat snapshot file SQLite (`VACUUM INTO`, koneksi read-only). **File
   SQLite asli tidak pernah diubah**, jadi selalu bisa kembali ke sana.
2. Menyamakan skema snapshot dan PostgreSQL ke versi server saat ini.
3. Menolak bila database PostgreSQL tujuan sudah berisi user atau device
   (kecuali dengan `-force`).
4. Menyalin semua tabel sesuai urutan foreign key, satu transaksi per tabel.
5. Membandingkan jumlah baris tiap tabel dan memverifikasi rantai audit log di
   PostgreSQL.

## Langkah-langkah

1. **Siapkan PostgreSQL** (versi 13 atau lebih baru) dan database kosong:

   ```sql
   CREATE USER endpointmgmt WITH PASSWORD 'ganti-dengan-password-kuat';
   CREATE DATABASE endpointmgmt OWNER endpointmgmt;
   ```

2. **Backup SQLite** (pengaman tambahan):

   ```bash
   sudo /opt/endpoint-mgmt/endpoint-mgmt-server -backup /root/sebelum-pindah.db
   ```

3. **Hentikan server**, supaya tidak ada data baru yang tertinggal di SQLite
   setelah disalin:

   ```bash
   sudo systemctl stop endpoint-mgmt
   ```

4. **Jalankan pemindahan** (sesuaikan path file database dan URL):

   ```bash
   sudo /opt/endpoint-mgmt/endpoint-mgmt-server -migrate-to-postgres \
     -from /opt/endpoint-mgmt/data/endpoint-mgmt.db \
     -to 'postgres://endpointmgmt:PASSWORD@127.0.0.1:5432/endpointmgmt?sslmode=disable'
   ```

   Hasil yang benar diakhiri dengan:

   ```
   Done: 43 tables, ... rows. Audit chain on PostgreSQL: verified.
   ```

5. **Arahkan server ke PostgreSQL.** Di file env server
   (`/opt/endpoint-mgmt/.env`), tambahkan:

   ```
   DB_URL=postgres://endpointmgmt:PASSWORD@127.0.0.1:5432/endpointmgmt?sslmode=disable
   ```

6. **Start dan cek:**

   ```bash
   sudo systemctl start endpoint-mgmt
   curl -s http://127.0.0.1:8443/readyz
   ```

   `"status":"ready"` berarti server jalan di PostgreSQL. Login ke console dan
   pastikan device dan user lengkap.

## Kalau perlu kembali ke SQLite

Hapus baris `DB_URL` dari file env dan restart. File SQLite tidak diubah oleh
pemindahan, jadi server kembali ke keadaan saat dihentikan di langkah 3.
Perubahan yang dibuat setelah pindah ke PostgreSQL tidak ikut kembali.

## Catatan

- **Backup.** Job backup otomatis server hanya untuk SQLite. Di PostgreSQL,
  jadwalkan `pg_dump` sendiri, misalnya harian lewat cron.
- **Rantai audit.** Entri lama dari SQLite di-hash dengan waktu berpresisi
  nanodetik, sedangkan PostgreSQL menyimpan mikrodetik. Perintah ini menyimpan
  teks waktu aslinya (`hash_time`), sehingga entri lama tetap terverifikasi.
- **Pengaturan opsional:** `DB_MAX_OPEN`, `DB_MAX_IDLE`, `DB_CONN_LIFETIME`, dan
  `DB_STATEMENT_TIMEOUT` (misalnya `30s`) untuk membatasi query yang macet.
