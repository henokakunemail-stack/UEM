# Rencana: Kepatuhan, Aset, Paritas Linux/macOS, dan Mode Offline

Status: **keputusan K1–K4 sudah diambil (2026-11-19)**, lihat bagian "Keputusan". Belum ada kode yang diubah.
Dibuat: 2026-11-19. Semua kondisi "saat ini" di bawah diambil dari kode di commit `051f09e` (v1.6.3).

Dokumen ini merencanakan lima fitur yang diminta:

1. Laporan kepatuhan perangkat siap audit (PDF)
2. Paritas Linux/macOS di Group Policy
3. Paket kepatuhan regulasi Indonesia (UU PDP, ISO 27001, POJK)
4. Dashboard biaya dan umur aset
5. Mode offline (update agen dan patch tanpa internet)

---

## 0. Ringkasan dan urutan kerja

Kelima fitur ini saling bergantung. Laporan kepatuhan (1) dan paket regulasi (3) butuh data postur keamanan
perangkat yang **belum dikumpulkan sama sekali** oleh agen. Karena itu urutan yang disarankan:

| Fase | Isi | Butuh rilis agen? | Ukuran |
|---|---|---|---|
| **A** | Fondasi: agen mengumpulkan data postur keamanan + scan patch terjadwal | Ya | Besar |
| **B** | Fitur 1: laporan kepatuhan perangkat + mesin PDF | Tidak (server/console) | Sedang |
| **C** | Fitur 3: template regulasi (UU PDP, ISO 27001, POJK) di atas Fase B | Tidak | Sedang |
| **D** | Fitur 4: dashboard biaya & umur aset | Sedikit (tanggal BIOS) | Sedang |
| **E** | Fitur 5: mode offline | Ya (patch dari server) | Besar |
| **F** | Fitur 2: paritas Linux/macOS | Ya | Besar, tergantung jumlah device Linux/macOS |

Setiap fase bisa dirilis sendiri. Fase A dan D (bagian agen) sebaiknya digabung dalam satu rilis agen supaya armada
tidak perlu di-update dua kali.

Fitur 2 sengaja diletakkan terakhir: armada saat ini hampir seluruhnya Windows, sehingga manfaatnya bergantung pada
berapa banyak device Linux/macOS yang benar-benar ada (lihat Keputusan K2).

---

## Fase A — Fondasi: data postur keamanan perangkat

### Kondisi saat ini

- Inventory agen hanya berisi hardware (CPU, RAM, disk, NIC, model, serial), daftar software, detail OS, dan domain
  (`agent/shared/inventory/inventory.go`).
- **Tidak dikumpulkan sama sekali:** status BitLocker/enkripsi disk, status antivirus, status firewall, kebijakan
  password lokal, user yang sedang login.
- Status Defender dan firewall hanya muncul sebagai teks bebas dari tugas maintenance "Security & Antivirus Posture
  Audit" yang dijalankan manual (`agent/shared/maintenance/exec_windows.go:477-479`). Teks itu tidak bisa dipakai
  untuk laporan.
- Field `LastBootUTC` ada di struct tapi tidak pernah diisi di OS mana pun.
- Scan patch hanya jalan kalau server mengirim perintah `patch.scan`. Tidak ada jadwal, jadi data patch bisa basi
  berminggu-minggu.

### Yang akan dibuat

**A1. Laporan postur baru dari agen** (`posture`, terpisah dari inventory, dikirim saat start, tiap 6 jam, dan saat
diminta). Isinya per OS:

| Kontrol | Windows | Linux | macOS |
|---|---|---|---|
| Enkripsi disk | BitLocker per volume (WMI `Win32_EncryptableVolume`): status proteksi, metode, persen | LUKS: volume `crypt` di `lsblk` untuk disk sistem | FileVault: `fdesetup status` |
| Antivirus | `root\SecurityCenter2 AntiVirusProduct` (PC desktop) + Defender `MSFT_MpComputerStatus`: aktif, real-time on, umur signature | Deteksi ClamAV/produk lain yang terpasang (laporan saja) | XProtect versi + Gatekeeper `spctl --status` |
| Firewall | Status per profil (Domain/Private/Public) via API firewall Windows | ufw / firewalld / nftables aktif atau tidak | `socketfilterfw --getglobalstate` |
| Kebijakan password lokal | `NetUserModalsGet` (API NetAPI yang sama dengan fitur akun admin): panjang minimum, umur maks, histori, ambang lockout | `/etc/login.defs` + `pam_pwquality` minlen | `pwpolicy getaccountpolicies` |
| Kunci layar | Timeout & password screensaver (registry kebijakan + user) | dconf GNOME `lock-enabled`, `idle-delay` | `sysadminctl -screenLock status` |
| Lain-lain | Last boot, user login aktif, tanggal BIOS (dipakai Fase D) | Last boot, user aktif, `bios_date` | Last boot, user aktif |

Setiap nilai punya tiga kemungkinan: **nilai nyata**, **tidak diketahui** (gagal dibaca, dengan alasan), atau **tidak
berlaku** (misalnya BitLocker di Windows Home). Tidak boleh ada default "aman" ketika data gagal dibaca.

**A2. Penyimpanan server:** tabel `device_posture` (satu baris per device, kolom terstruktur untuk yang dipakai filter,
JSON untuk detail, `collected_at`). Riwayat disimpan ringkas (`device_posture_history`, satu baris per perubahan)
supaya laporan bisa menunjukkan "sejak kapan tidak patuh".

**A3. Scan patch terjadwal:** server mengirim `patch.scan` otomatis (default tiap 24 jam, bisa diatur). Tanpa ini
laporan kepatuhan patch tidak bisa dipercaya.

**A4. Isi `LastBootUTC`** di ketiga OS.

### Status implementasi (2026-11-19, di-commit 2026-11-20)

- **Selesai:** A1 untuk Windows (kolektor `agent/windows/posture_windows.go`), A2 (tabel `device_posture` dan
  `device_posture_history`, API `GET /api/devices/{id}/posture`), A3 (`PATCH_SCAN_INTERVAL`, default 24 jam), A4
  (boot terakhir di Windows, Linux, macOS), dan tab **Keamanan** di detail device.
- **Penyesuaian dari rencana:** postur ikut laporan inventory, bukan pesan terpisah. Akibatnya jadwalnya tiap 4 jam,
  bukan 6 jam, dan tombol "Collect" yang sudah ada ikut mengumpulkan postur. Kunci layar yang dibaca adalah "Machine
  inactivity limit" (berlaku untuk seluruh mesin); screensaver per-user belum dibaca.
- **Belum:** postur Linux/macOS (dipindah ke Fase F); uji di PC nyata sebagai SYSTEM (BitLocker hanya terbaca dengan
  hak admin, dan dari PC pengembang tanpa elevasi hasilnya "Tidak diketahui", sesuai desain).

### Kriteria selesai (bisa dicek orang lain)

- Agen v-baru di PC Windows dengan BitLocker aktif → console menampilkan "BitLocker: On, XTS-AES 128, 100%" untuk
  drive C:. PC tanpa BitLocker → "Off". PC Windows Home → "Tidak berlaku".
- Agen lama (belum update) → semua kontrol tampil "Tidak diketahui — agen belum mendukung", **bukan** "Patuh".
- Mematikan firewall profil Public di satu PC → dalam ≤ 6 jam (atau langsung dengan tombol "Kumpulkan sekarang")
  status firewall device itu berubah.

### Risiko

- `SecurityCenter2` tidak ada di Windows Server; harus jatuh ke Defender API, dan bila ada antivirus pihak ketiga di
  server, status AV menjadi "Tidak diketahui".
- Membaca BitLocker butuh hak admin; agen berjalan sebagai SYSTEM, jadi aman, tapi perlu diuji di PC nyata.

---

## Fase B — Fitur 1: Laporan kepatuhan perangkat (PDF)

### Kondisi saat ini

- Modul `reports` hanya mengekspor CSV/JSON untuk inventory, patch, deployment, dan audit log.
- **Tidak ada library PDF** di server maupun console, dan tidak ada halaman siap cetak.

### Yang akan dibuat

**B1. Profil kepatuhan (baseline)** yang bisa diatur Admin, misalnya:

- Enkripsi disk sistem wajib aktif
- Antivirus aktif, real-time on, signature ≤ 7 hari
- Firewall aktif di semua profil
- Tidak ada patch kritis/security yang hilang lebih dari 30 hari; scan patch terakhir ≤ 7 hari
- Password lokal minimal 8 karakter, lockout ≤ 10 percobaan
- Kunci layar ≤ 15 menit

Satu profil default disediakan; Admin bisa membuat profil lain per grup/site.

**B2. Mesin evaluasi** di server: setiap device dinilai per kontrol → **Patuh / Tidak patuh / Tidak diketahui / Tidak
berlaku**. Skor device dan skor armada **hanya** dihitung dari kontrol yang benar-benar terevaluasi; jumlah "Tidak
diketahui" ditampilkan terpisah dan jelas (pelajaran dari security audit yang dulu menghitung cek yang tidak dijalankan
sebagai "lulus", dan dari tampilan "Fully Compliant" untuk device tanpa data).

**B3. Halaman "Kepatuhan Perangkat" di console:** ringkasan armada, daftar device tidak patuh (bisa difilter per
kontrol/site/grup), detail per device dengan bukti mentah (misalnya nilai registry/WMI yang dibaca dan waktunya).

**B4. Laporan PDF**, isinya:

1. Sampul: nama organisasi, periode, profil yang dipakai, waktu pembuatan, siapa yang membuat
2. Ringkasan eksekutif: % patuh per kontrol, tren dibanding laporan sebelumnya
3. Daftar pengecualian: device tidak patuh beserta alasannya
4. Lampiran: tabel per device, semua kontrol
5. Metodologi: dari mana data diambil, kapan terakhir dikumpulkan, keterbatasan (device offline, data tidak diketahui)
6. Halaman verifikasi: hash SHA-256 isi laporan, dicatat ke audit log yang tamper-evident, sehingga auditor bisa
   memastikan PDF tidak diubah setelah dibuat

**B5. Laporan terjadwal** (bulanan) yang disimpan di server dan bisa diunduh ulang (arsip bukti untuk audit berikutnya).

### Status implementasi (2026-11-20, sudah di-commit)

- **Selesai:** B1 (profil, tabel `compliance_profiles`, profil default dari ambang di atas), B2 (mesin evaluasi
  `server/modules/compliance`), B3 (halaman **Kepatuhan perangkat**: ringkasan, filter per kontrol/site/status, detail
  per device dengan bukti mentah dan waktu pengumpulan), B4 (PDF via `go-pdf/fpdf`, Go murni), B5 (laporan bulanan
  terjadwal, arsip di `DATA_DIR/compliance-reports/`, tabel `compliance_reports`).
- **Penyesuaian dari rencana:**
  - Hash yang **dicetak di PDF** adalah SHA-256 *isi* laporan, karena file tidak bisa memuat hash dirinya sendiri.
    SHA-256 *file* PDF dicatat di audit log (`compliance.report.generated`) dan arsip, lalu diperiksa lewat bagian
    **Verifikasi laporan** di console (hash dihitung di browser, file tidak diunggah) atau dengan `certutil`.
  - Kunci layar dinilai dari batas inaktivitas tingkat mesin, yaitu nilai yang diatur oleh setting *inactivity
    timeout* di Kebijakan Grup. Device tanpa batas itu dinilai **Gagal** dengan saran perbaikan.
  - Umur update yang hilang dihitung sejak scan pertama kali menemukannya, karena tanggal rilis update tidak disimpan.
  - Profil tidak membuat laporan terjadwal untuk bulan sebelum profil itu dibuat, supaya instalasi baru tidak
    menerbitkan "laporan Oktober" dari data November.
  - Nama organisasi di sampul diambil dari `ORG_NAME`. PDF hanya dalam Bahasa Indonesia, dan alasan per kontrol juga
    tetap Bahasa Indonesia saat console dipakai dalam Bahasa Inggris.
- **Diverifikasi:** server uji lokal dengan 5 device. Skor 25% (1 dari 4 yang terevaluasi), device agen lama
  "Tidak diketahui". Hash dari `certutil`, header unduhan, dan audit log identik, dan file yang diubah satu byte
  ditolak.
- **Belum:** uji dengan data armada nyata. PDF baru dibuka di penampil PDF lokal, belum di Adobe Reader.

### Kriteria selesai

- Dengan data uji 3 device (patuh semua / BitLocker off / agen lama), PDF menunjukkan 1 patuh, 1 tidak patuh dengan
  alasan "BitLocker off pada C:", 1 tidak diketahui; skor armada = 50% (1 dari 2 terevaluasi), bukan 33% atau 66%.
- Hash di halaman verifikasi sama dengan entri di audit log, dan berubah jika satu byte PDF diubah.
- PDF terbuka benar di Adobe Reader dan browser, teks Bahasa Indonesia tampil benar.

---

## Fase C — Fitur 3: Paket kepatuhan regulasi Indonesia

### Kondisi saat ini

- Security audit punya 40 cek, semuanya tentang **server** (konfigurasi, DB, TLS, backup, audit log). Tidak ada yang
  menilai perangkat.
- Satu-satunya pemetaan kerangka adalah label OWASP Top 10. Tidak ada pemetaan ISO 27001, PDP, atau POJK.

### Yang akan dibuat

**C1. Template regulasi sebagai data**, bukan kode: file definisi per kerangka berisi daftar kontrol, deskripsi, dan
pemetaan ke sumber bukti. Contoh sumber bukti yang tersedia setelah Fase A–B:

| Sumber bukti di aplikasi | Contoh kontrol yang didukung |
|---|---|
| Enkripsi disk per device | Kriptografi, perlindungan data pribadi |
| Antivirus + patch | Perlindungan malware, manajemen kerentanan teknis |
| Firewall, USB block, kunci layar (Group Policy + postur) | Perangkat endpoint pengguna |
| Inventory + aset + lisensi | Inventaris aset |
| Audit log hash-chain | Logging dan pemantauan |
| Backup database + umurnya | Backup informasi |
| RBAC, sesi, hashing password console | Kontrol akses dan autentikasi |
| Security audit server (cek yang terevaluasi) | Konfigurasi aman |

**C2. Regulasi acuan (versi berlaku, dicek 2026-11-19)**

Sumber dicek dari peraturan.bpk.go.id dan ojk.go.id. Yang **belum terverifikasi dari sumber resmi** ditandai ⚠ dan
tidak boleh dicetak di laporan sampai dipastikan.

*ISO/IEC 27001*
- Edisi berlaku: **ISO/IEC 27001:2022 + Amd 1:2024** (amandemen hanya menambah klausul iklim di 4.1/4.2; Annex A
  tidak berubah). Belum ada edisi baru; tinjauan berikutnya sekitar 2027. ⚠ iso.org menolak akses saat dicek, jadi ini
  dari sumber sekunder.
- Annex A: 93 kontrol (5 Organisasi 37, 6 Orang 8, 7 Fisik 14, 8 Teknologi 34). Kontrol yang bisa dibuktikan aplikasi
  ini: 5.9 inventaris aset, 5.15 kontrol akses, 5.17 informasi autentikasi, 8.1 perangkat endpoint pengguna,
  8.2 hak akses istimewa, 8.5 autentikasi aman, 8.7 perlindungan malware, 8.8 manajemen kerentanan teknis,
  8.9 manajemen konfigurasi, 8.12 pencegahan kebocoran data (USB block), 8.13 backup, 8.15 logging, 8.16 pemantauan,
  8.24 kriptografi, 8.32 manajemen perubahan.
- **Teks resmi kontrol berhak cipta ISO.** Laporan hanya mencantumkan nomor kontrol dan ringkasan buatan sendiri,
  bukan menyalin teks standar.

*UU No. 27 Tahun 2022 (Pelindungan Data Pribadi)*
- Berlaku sejak 17 Okt 2022; masa transisi (Pasal 74) **berakhir 17 Okt 2024**. Putusan MK 151/PUU-XXII/2024
  mengubah pembacaan Pasal 53(1)(b).
- Pasal yang relevan: **35** (langkah teknis-operasional sesuai risiko), **36** (kerahasiaan), **37** (pengawasan pihak
  yang terlibat), **38** (perlindungan dari pemrosesan tidak sah), **39** (mencegah akses tidak sah, sistem elektronik
  andal dan aman), **46** (pemberitahuan kegagalan pelindungan dalam 3×24 jam).
- ⚠ **PP 33 Tahun 2026** (peraturan pelaksana): menurut sumber sekunder ditetapkan Juli 2026 dan **berlaku 16 Jan
  2027**; belum ditemukan di portal resmi. Template disiapkan, tapi pasal-pasal PP baru dicantumkan setelah teks
  resminya dipastikan.
- Lembaga pengawas PDP **belum terbentuk** (Perpres belum ditandatangani per Nov 2026).

*OJK — dipilih per jenis klien*

| Jenis klien | Regulasi berlaku | Pasal yang relevan untuk endpoint |
|---|---|---|
| Bank Umum | **POJK 11/POJK.03/2022** + **PADK 1 Tahun 2026** (berlaku 1 Mar 2026, menggantikan SEOJK 21/2017) + **SEOJK 29/SEOJK.03/2022** (ketahanan siber) | 15 manajemen risiko TI, 16 keamanan informasi, 21 ketahanan siber, 22 asesmen maturitas tahunan, 23–25 pengujian berbasis kerentanan, 54(2) audit trail, 60 laporan insiden 24 jam |
| BPR / BPRS | **POJK 75/POJK.03/2016 masih berlaku sampai ±18 Des 2026**, lalu diganti **POJK 34 Tahun 2025** | (POJK 34/2025) 14 manajemen risiko, 15 keamanan informasi (ayat 3 kontrol otorisasi), 18 backup harian, 23–25 ketahanan siber, 40 audit trail |
| Lembaga Jasa Keuangan Non-Bank | **POJK 4/POJK.05/2021** + **SEOJK 22/SEOJK.05/2021** | 17 keamanan informasi, 18 pengendalian intern, 19 audit trail, 22 backup, 31 laporan insiden kritis 5 hari kerja |

Catatan: batang tubuh ketiga POJK tidak menyebut enkripsi, malware, atau patch secara eksplisit; rincian itu ada di
SEOJK/PADK pelaksananya, yang **belum dibaca per pasal**. Pemetaan kontrol teknis ke SEOJK 29/2022, PADK 1/2026, dan
SEOJK 22/2021 dikerjakan saat Fase C dimulai, dengan membaca teks resminya.

Karena BPR beralih ke POJK 34/2025 pada akhir 2026, template BPR menyediakan **dua versi** dan memilih otomatis
berdasarkan tanggal periode laporan.

**C3. Laporan per kerangka (PDF, memakai mesin Fase B):** per kontrol menampilkan status **Didukung bukti / Sebagian /
Di luar cakupan aplikasi**, bukti yang dilampirkan, dan catatan. Kontrol yang tidak bisa dibuktikan oleh aplikasi ini
(misalnya kebijakan SDM, kontrak vendor) ditandai **di luar cakupan**, bukan dianggap patuh.

**C4. Penafian di setiap laporan:** laporan ini adalah *paket bukti teknis*, bukan sertifikasi atau opini hukum.

### Status implementasi (2026-11-20, sudah di-commit)

- **Selesai:**
  - C1: template sebagai file JSON. Template bawaan ditanam di server (`server/modules/compliance/frameworks/`), dan
    file di `DATA_DIR/compliance-templates/` menggantikan atau menambah template tanpa build ulang.
  - C3: penilaian per kontrol dengan bukti, ditampilkan di console dan dicetak sebagai PDF.
  - C4: penafian di sampul dan bagian metodologi setiap laporan.
  - Template: ISO 27001:2022 (93 kontrol, 25 dipetakan), UU PDP (Pasal 35–39, 46), POJK Bank Umum 11/2022, POJK BPR
    75/2016 dan 34/2025 (dipilih otomatis menurut periode), dan POJK LJK Non-Bank 4/2021.
- **Penyesuaian dari rencana:**
  - Status punya lima nilai, bukan tiga: *Didukung bukti / Sebagian / Ada celah / Belum ada data / Di luar cakupan
    aplikasi*. "Ada celah" dan "Belum ada data" dibedakan supaya data yang menunjukkan kontrol tidak diterapkan tidak
    tercampur dengan fitur yang belum dipakai.
  - Kontrol yang hanya dapat dibuktikan sebagian oleh aplikasi (`coverage: partial`) paling tinggi "Sebagian".
  - Bukti perangkat paling tinggi "Sebagian" selama ada device yang tidak dapat dinilai.
- **Belum, dan sengaja tidak dikarang:**
  - Pemetaan per butir ke PADK 1/2026, SEOJK 29/2022, SEOJK 22/2021, dan SEOJK BPR. Template hanya memetakan pasal
    batang tubuh POJK yang sudah dicek; sisanya tercantum di bagian "Belum tercakup" setiap laporan.
  - Nomor pasal POJK 75/2016 belum diverifikasi. Laporannya hanya mencetak topik dengan tanda "(rujukan belum
    diverifikasi)".
  - PP 33/2026 belum dicantumkan.
  - Tanggal berlaku POJK 34/2025 (sekitar 18 Desember 2026) perlu dipastikan. Bila berbeda, cukup ubah
    `effective_from`/`effective_until` di file template.
- **Diverifikasi:**
  - Server uji jalan di atas database hasil Fase B, jadi migrasi 0032 terbukti pada data yang sudah ada.
  - File kustom berlaku tanpa restart, dan file rusak dilaporkan.
  - Laporan BPR periode Januari 2027 memakai POJK 34/2025.

### Kriteria selesai

- Laporan ISO 27001 untuk armada uji menampilkan setiap kontrol yang dipetakan dengan statusnya, dan kontrol yang
  tidak dipetakan tercantum di bagian "di luar cakupan".
- Mengubah file template (misalnya menambah satu kontrol) tampil di laporan berikutnya tanpa build ulang.

### Risiko penting

- Regulasi berubah: PP 33/2026 berlaku Jan 2027, BPR beralih ke POJK 34/2025 Des 2026, PADK baru untuk bank umum.
  Setiap template mencantumkan versi regulasi dan tanggal terakhir ditinjau, dan file template bisa diperbarui tanpa
  build ulang.
- Pemetaan kontrol adalah interpretasi teknis. Sebelum laporan diserahkan ke auditor/regulator, sebaiknya ditinjau
  oleh tim compliance atau legal klien.

---

## Fase D — Fitur 4: Dashboard biaya & umur aset

### Kondisi saat ini

- Aset hardware sudah punya `purchase_date`, `purchase_cost`, `warranty_expires_at`, dan bisa ditautkan ke device.
- Lisensi punya `total_seats`, `cost` (total), `purchased_at`, `expires_at`; alokasi per device dan deteksi
  instalasi dari inventory sudah ada.
- Perhitungan yang ada hanya total nilai pembelian dan garansi yang habis dalam 30 hari. **Tidak ada** umur, depresiasi,
  atau prediksi.
- Console **tidak punya library grafik**.

### Yang akan dibuat

**D1. Umur perangkat**, dengan sumber yang jelas:

1. `purchase_date` dari aset (paling akurat)
2. Jika kosong: tanggal BIOS dari agen (ditambahkan di Fase A, Windows `Win32_BIOS.ReleaseDate`, Linux
   `/sys/class/dmi/id/bios_date`) — perkiraan
3. Jika kosong: tanggal instal OS — perkiraan terlemah

Setiap angka umur diberi label sumbernya ("dari tanggal beli" / "perkiraan dari BIOS"), supaya manajemen tidak
mengira perkiraan adalah data pasti.

**D2. Pengaturan siklus hidup** (oleh Admin): umur pakai per kategori (misalnya laptop 4 tahun, desktop 5 tahun),
biaya pengganti per kategori, metode depresiasi garis lurus.

**D3. Dashboard manajemen:**

- Distribusi umur armada (0–1, 1–3, 3–5, >5 tahun)
- Daftar PC yang sudah melewati umur pakai, dan yang spesifikasinya di bawah batas (misalnya RAM < 8 GB, disk penuh)
- **Prediksi anggaran penggantian** per kuartal/tahun untuk 3 tahun ke depan
- Nilai buku saat ini (setelah depresiasi) vs nilai pembelian
- **Lisensi terbuang:** seat dibeli tapi tidak dipakai, dan seat dialokasikan tapi software tidak terdeteksi terpasang,
  beserta nilai rupiahnya (biaya ÷ seat × seat tak terpakai)
- Lisensi yang akan habis dalam 90 hari
- Garansi yang akan habis

Grafik dibuat dengan SVG/CSS sederhana di console (tanpa menambah library), mengikuti gaya yang sudah ada.

**D4. Ekspor PDF ringkasan manajemen** memakai mesin Fase B.

### Status implementasi (2026-11-20, belum di-commit)

- **Selesai:**
  - D1: umur dengan sumbernya (tanggal beli → BIOS → instal OS → tidak diketahui).
  - D2: pengaturan per kategori, dengan nilai rekomendasi K5 sebagai default: laptop 4 tahun, desktop 5, server 5,
    lainnya 5, biaya diisi Admin.
  - D3: tab **Siklus hidup & biaya** di halaman Aset & lisensi.
  - D4: PDF ringkasan, yang SHA-256-nya dicatat ke audit log.
- **Penyesuaian dari rencana:**
  - Aset mendapat kolom **kategori** (migrasi 0033). Bila kosong, kategori diambil dari jenis casing yang kini
    dilaporkan agen: Windows `PCSystemType`, Linux `chassis_type` SMBIOS, dan nama model untuk macOS. Tanpa jenis
    casing, kategori "umur pakai per kategori" untuk 180 PC harus diisi satu per satu.
  - Aset berkategori **Lainnya** (monitor, dock) tidak memakai tanggal BIOS atau instal OS dari PC yang ditautkan.
  - Armada = aset yang belum pensiun/dibuang, ditambah device aktif yang belum punya aset.
  - Tanggal BIOS di Linux dibaca dari `/sys/class/dmi/id/bios_date` dan dikirim di model hardware. Windows tetap
    lewat postur (Fase A). Tanggal placeholder sebelum 1995 ditolak.
  - Prediksi: satu penggantian per unit. Biaya diambil dari biaya kategori; bila belum diatur, dari harga beli aset;
    bila keduanya kosong, unit dihitung "tanpa biaya" dan tidak dianggap Rp 0.
  - Seat tak dipakai = seat − perangkat yang dialokasikan **atau** terpasang (dihitung sekali per perangkat).
    Lisensi situs (`site_license`) tidak dihitung per seat. Alokasi ke PC tanpa inventory dihitung "belum ada
    inventory", bukan "tanpa instalasi".
  - PDF ringkasan tidak diarsipkan seperti laporan kepatuhan. File-nya dibuat saat diunduh, dan hash-nya tercatat
    di audit log.
- **Diverifikasi:**
  - Tes dengan data uji kriteria di bawah: 7 umur dari tanggal beli, 2 dari BIOS (satu dari postur Windows, satu dari
    model Linux), 1 tidak diketahui. Unit yang umurnya tidak diketahui tidak masuk prediksi dan tercatat di
    `missing_age`. Lisensi: 4 seat (Rp 4.000.000) dan 2 alokasi tanpa instalasi (Rp 2.000.000).
  - 18 mutasi kode semuanya tertangkap tes.
  - Server uji: database dibuat oleh versi sebelum Fase D lalu dibuka versi baru. Migrasi 0033 berjalan pada data
    yang sudah ada. Hash PDF sama di `certutil`, header unduhan, dan audit log. Tampilan console dicek di lebar desktop
    dan 390 px.
- **Belum:**
  - Jenis casing dan BIOS dari agen baru terisi setelah rilis agen. Rilis ini sebaiknya digabung dengan rilis agen
    Fase A.
  - Uji dengan data armada nyata.

### Kriteria selesai

- Data uji: 10 aset, 3 tanpa tanggal beli (2 punya tanggal BIOS, 1 tidak). Dashboard menampilkan 7 umur "dari tanggal
  beli", 2 "perkiraan dari BIOS", 1 "tidak diketahui", dan device tanpa umur tidak masuk ke prediksi anggaran tapi
  dihitung di catatan "data kurang".
- Lisensi 10 seat Rp 10.000.000, 6 dialokasikan, 4 terpasang → ditampilkan "4 seat tak dipakai (Rp 4.000.000), 2
  alokasi tanpa instalasi (Rp 2.000.000)".

---

## Fase E — Fitur 5: Mode offline

### Kondisi saat ini

| Bagian | Butuh internet? |
|---|---|
| Update agen (Agent Updates → Impor dari Paket Server) | **Tidak.** Biner agen dibaca dari folder `agents/` di server dan disajikan dari server. Sudah offline. |
| Software deployment | **Tidak.** Paket disimpan dan diunduh dari server. |
| Console web | **Tidak.** Tidak ada CDN/font eksternal. |
| Update server (`update.sh`) | **Ya**, GitHub API/clone. Opsi `--binary-path` sudah bisa offline, tapi hanya mengganti biner server, tidak membawa folder `agents/`. |
| Patch Windows | **Ya.** Agen memakai Windows Update API dengan `ServerSelection = 2`, artinya **langsung ke server Microsoft dan mengabaikan WSUS** meskipun perusahaan punya WSUS. |
| Patch Linux/macOS | Ya, ke repo apt/dnf/Apple yang dikonfigurasi OS. |
| Proxy | Agen sudah menghormati `HTTPS_PROXY` (`http.ProxyFromEnvironment`). Belum ada cara mengaturnya dari installer/console. |

### Yang akan dibuat

**E1. Paket update server offline:** `update.sh --package <file.tar.gz>` memakai paket rilis lengkap (server + folder
`agents/`) yang dibawa lewat flashdisk/SCP, memverifikasi checksum terhadap `checksums.txt` rilis, lalu memasang
server dan agen bundel sekaligus. Dokumentasi langkah offline.

**E2. Patch Windows lewat WSUS bila ada:** ubah agar agen memakai sumber update yang ditetapkan kebijakan Windows
(WSUS bila dikonfigurasi, Microsoft bila tidak), bukan memaksa Microsoft. Perubahan kecil, manfaat besar untuk
perusahaan yang sudah punya WSUS.

**E3. Patch Windows dari server UEM (tanpa WSUS):**

- Admin mengunduh file update (`.msu`) dari Microsoft Update Catalog di komputer yang punya internet, lalu
  mengunggahnya ke console (atau server mengimpor folder).
- Server mencocokkan KB yang diunggah dengan patch yang dilaporkan hilang oleh device.
- Agen mengunduh `.msu` dari server UEM dan memasangnya (`wusa /quiet /norestart`), dengan verifikasi checksum dan
  laporan hasil per device seperti instalasi patch biasa.

**E4. Proxy dari console dan installer:** pengaturan proxy per site (dikirim ke agen) dan parameter `/PROXY=` di
installer, untuk kantor yang hanya bisa keluar lewat proxy.

**E5. Halaman "Status Offline"** di console: daftar fitur yang butuh internet dan apakah server/agen saat ini bisa
menjangkaunya, supaya admin tahu apa yang perlu disiapkan.

### Kriteria selesai

- Server uji tanpa DNS publik: `update.sh --package endpoint-server-linux-amd64.tar.gz` berhasil, dan versi baru agen
  muncul di Agent Updates → Impor dari Paket Server.
- PC uji dengan kebijakan WSUS: scan patch membaca dari WSUS (terlihat di log Windows Update), bukan Microsoft.
- PC uji tanpa internet: `.msu` yang diunggah ke server terpasang, status patch berubah menjadi "installed".

### Risiko

- `.msu` kumulatif berukuran ratusan MB–beberapa GB; perlu memperhatikan kapasitas disk server dan batas waktu unduhan
  (masalah "unexpected EOF" yang sudah ditemukan — batas tulis server 60 detik — **harus diperbaiki dulu**).
- Mengganti sumber Windows Update memengaruhi semua PC; perlu uji di satu PC sebelum armada.

---

## Fase F — Fitur 2: Paritas Linux/macOS di Group Policy

### Kondisi saat ini

- Linux: hanya **USB block** dan **akun admin terkelola** yang benar-benar diterapkan. macOS: hanya **akun admin**.
  Semua setting lain dilaporkan "tidak didukung".
- Agen Linux/macOS berjalan sebagai root (systemd / LaunchDaemon) dan **tidak punya mekanisme menjalankan sesuatu di
  sesi user**.
- Console tidak menunjukkan di editor policy setting mana yang didukung per OS; ketahuannya baru setelah diterapkan.

### Pendekatan

Banyak setting desktop Linux bisa diterapkan **dari root tanpa sesi user**, lewat database sistem dconf GNOME
(`/etc/dconf/db/local.d/` + file `locks`), yang berlaku untuk semua user dan bisa dikunci. Untuk macOS, sebagian besar
pengaturan per-user resminya butuh *configuration profile* lewat MDM; tanpa MDM, beberapa setting harus tetap
"tidak didukung" — dan itu dilaporkan jujur, bukan dipaksakan.

| Setting | Linux (rencana) | macOS (rencana) |
|---|---|---|
| Firewall | Aktifkan ufw / firewalld | `socketfilterfw --setglobalstate on` |
| Wallpaper (+kunci) | dconf `org/gnome/desktop/background` + lock (GNOME) | Tetap tidak didukung tanpa MDM |
| Kunci layar / inactivity | dconf `idle-delay`, `lock-enabled`, `lock-delay` + lock | Login window idle (sistem); per-user tetap tidak didukung tanpa MDM |
| Autoplay | dconf `media-handling/autorun-never` + lock | Tidak berlaku |
| Mode update | unattended-upgrades (Debian/Ubuntu) / dnf-automatic | `softwareupdate --schedule` + preferensi sistem update otomatis |
| USB read-only | Aturan udev/udisks (dipertimbangkan, kompleks) | Tidak didukung |
| Antivirus | Laporan saja (dari Fase A) | Laporan saja (XProtect/Gatekeeper) |
| Task Manager, CMD, Registry, Control Panel, PowerShell | Tetap Windows-only (tidak ada padanan yang setara) | Sama |

Desktop selain GNOME (KDE, XFCE) dan server tanpa desktop dilaporkan "tidak berlaku", bukan "gagal".

**F-console:** matriks dukungan per OS ditampilkan di editor policy (ikon Windows/Linux/macOS per setting), dan panel
Effective Policy menandai setting yang tidak berlaku untuk OS device itu.

### Kriteria selesai

- Ubuntu 24.04 (GNOME) uji: policy wallpaper + lock + kunci layar 10 menit → setelah login, wallpaper dan timeout
  sesuai, dan user tidak bisa mengubahnya di Settings.
- Ubuntu Server tanpa desktop → setting desktop dilaporkan "tidak berlaku", status device tetap "Enforced".
- macOS uji: firewall aktif setelah policy diterapkan.

### Risiko

- Butuh mesin uji Linux (GNOME) dan macOS nyata; saya tidak bisa menguji macOS dari lingkungan ini.
- Variasi distro dan desktop environment besar; cakupan awal dibatasi Ubuntu/Debian + RHEL-family dengan GNOME.

---

## Keputusan

### Sudah diputuskan (2026-11-19)

| # | Keputusan | Dampak ke rencana |
|---|---|---|
| **K1** | PDF dibuat **di server** | Fase B menambah satu library PDF Go murni (tanpa CGO). Laporan terjadwal, arsip, dan hash verifikasi ikut dikerjakan. |
| **K2** | Linux + macOS hanya **10–20 device** | Fase F tetap paling akhir dan cakupannya dikecilkan: firewall, mode update, dan pelaporan postur (dari Fase A) dulu; setting desktop lewat dconf GNOME hanya bila device Linux-nya memang memakai GNOME. |
| **K3** | Template regulasi mengikuti **versi yang berlaku saat ini** | Lihat bagian "Regulasi acuan" di Fase C. |
| **K4** | Ada **WSUS** di jaringan | Fase E2 (agen ikut WSUS) jadi prioritas utama mode offline; E3 (impor `.msu` ke server UEM) ditunda sampai ada kebutuhan nyata. |

### Masih terbuka (dipakai nilai rekomendasi bila tidak diubah)

| # | Pertanyaan | Rekomendasi saya |
|---|---|---|
| **K5** | Nilai default siklus hidup: umur pakai laptop/desktop dan biaya pengganti per kategori. | Laptop 4 tahun, desktop 5 tahun; biaya diisi Admin. |
| **K6** | Profil kepatuhan default (B1): apakah ambang di atas cocok untuk ESTA? | Gunakan sebagai awal, bisa diubah dari console. |

## Hal di luar cakupan rencana ini

- Penegakan (enforcement) otomatis BitLocker/antivirus — Fase A hanya **melaporkan**, sesuai keputusan sebelumnya
  "laporan dulu".
- Integrasi WhatsApp/Telegram, portal self-service, dan pembatasan akses per site (dibahas terpisah).
- Sertifikasi atau opini hukum — laporan regulasi adalah paket bukti teknis.

## Prasyarat yang sebaiknya diselesaikan dulu

1. ~~**Perbaikan batas waktu unduhan server (60 detik)**~~ **Selesai 2026-11-19**: unduhan file kini
   hanya diputus bila macet lebih dari 2 menit; rename di agen dicoba ulang ±12 detik saat file dikunci antivirus.
2. **Code signing agen** (Authenticode) tetap disarankan sebelum rilis agen berikutnya ke banyak PC, karena antivirus
   dan kasus "Access is denied" saat update.
