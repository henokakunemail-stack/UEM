# Penentuan Kebijakan: System Policy & Network Filter

Dokumen ini menjelaskan cara server menentukan kebijakan yang berlaku di sebuah perangkat, apa yang benar-benar diterapkan agent di tiap OS, dan arti status yang tampil di console.

## System policy

### Tiga pilihan per setting

Setiap setting di sebuah kebijakan punya salah satu nilai berikut:

| Nilai | Arti |
|---|---|
| **Tidak diatur** | Kebijakan ini tidak menentukan setting itu. Nilai dari kebijakan lain tetap berlaku. Ini default untuk kebijakan baru. |
| **Izinkan / Blokir** (atau nilai lain: *Read-only* untuk USB, menit untuk timeout, mode Windows Update) | Kebijakan ini menentukan setting itu. |

"Izinkan" eksplisit adalah pengecualian yang sengaja dibuat. Contohnya, kebijakan perangkat dengan USB "Izinkan" mengangkat blokir USB dari kebijakan armada.

### Urutan penentuan

Untuk setiap setting, yang berlaku adalah nilai eksplisit dari kebijakan aktif yang paling menentukan:

1. Cakupan paling spesifik: **perangkat > grup > semua perangkat**.
2. Pada cakupan yang sama, **angka prioritas lebih kecil** yang menang.
3. Jika prioritasnya sama, kebijakan **yang dibuat lebih baru** yang menang.
4. Jika waktu pembuatannya juga sama, ID kebijakan yang lebih besar yang menang, supaya hasilnya tidak pernah acak.

Setting yang tidak ditentukan kebijakan mana pun dikirim ke agent sebagai nilai dasar: USB *allow*, tanpa wallpaper, Windows Update *default*, restriksi mati, timeout 0. Di console nilai ini tampil sebagai "Tidak dikonfigurasi (bawaan OS)".

Tab **Kebijakan efektif** di halaman System Policy menampilkan, untuk satu perangkat:
- nilai setiap setting;
- nama dan cakupan kebijakan yang menentukannya.

Datanya berasal dari `GET /api/devices/{id}/system-policy`, yaitu field `sources` (setting → kebijakan) dan `matched_policies` (urut dari yang paling menentukan).

### Kebijakan yang dibuat sebelum aturan ini

Kebijakan lama menyimpan nilai untuk semua field, karena form dan server dulu mengisinya otomatis. Saat dibaca, nilai `allow`, `default`, `false`, dan `0` dianggap **Tidak diatur**. Nilai yang menyalakan sesuatu tetap berlaku, misalnya `block`, `true`, atau timeout lebih dari 0.

Data di database tidak diubah. Kebijakan tersimpan dengan penanda `settings_version: 2` saat berikutnya disimpan dari console.

### Perubahan anggota grup

Saat perangkat ditambahkan ke atau dikeluarkan dari grup, server langsung mengirim ulang system policy dan network filter yang baru ke perangkat itu. Perangkat yang sedang offline menerimanya saat terhubung lagi, karena versi kebijakannya berubah.

Grup yang masih menjadi target system policy atau filter policy tidak bisa dihapus.

### Dukungan per OS

| Setting | Windows | Linux | macOS |
|---|---|---|---|
| USB storage | block / read-only / allow | block / allow (blacklist modul `usb_storage`, `uas`); read-only **tidak didukung** | **tidak didukung** |
| Wallpaper | ya | **tidak didukung** | **tidak didukung** |
| Kunci saat tidak aktif | ya | **tidak didukung** | **tidak didukung** |
| Task Manager, Command Prompt, Registry Editor, Control Panel | ya | tidak ada (khusus Windows) | tidak ada (khusus Windows) |
| Windows Update, Firewall, Defender, PowerShell, Autoplay, Screen saver | ya | tidak ada (khusus Windows) | tidak ada (khusus Windows) |
| Akun admin yang dikelola | ya | ya (`useradd`, `chpasswd`) | ya (`sysadminctl`) |

Di Linux, wallpaper dan kunci layar tidak bisa diterapkan karena agent berjalan sebagai service sistem. Dari sana `gsettings` tidak menjangkau sesi desktop pengguna.

Setting khusus Windows yang dibiarkan di nilai dasar tidak dilaporkan sebagai masalah di OS lain.

### Status di console

| Status | Arti |
|---|---|
| Menunggu (`pending`) | Kebijakan sudah dikirim, laporan agent belum datang. |
| Diterapkan (`synced`) | Semua setting diterapkan. |
| **Sebagian diterapkan** (`partial`) | Kebijakan diterapkan, tetapi ada setting yang gagal atau tidak didukung di OS perangkat. Daftar setting dan alasannya tampil di bawah status. |
| Gagal (`failed`) | Tidak ada setting yang berhasil diterapkan. |

Aturan laporan:
- Agent mengirim hasil per setting: `applied`, `failed`, atau `unsupported`. Server menurunkan status dari hasil itu.
- Agent versi lama yang hanya mengirim status tetap diterima. Status yang tidak dikenal ditolak.
- Laporan untuk versi kebijakan yang sudah diganti dijawab `stale` dan tidak disimpan.
- Saat terhubung ulang, kebijakan tidak dikirim lagi ke perangkat yang `synced`, atau yang `partial` hanya karena setting tidak didukung. Perangkat dengan setting yang gagal tetap dikirimi ulang.

## Network filter

### Kebijakan tanpa rule

Kebijakan filter yang belum punya rule untuk mode-nya tidak ikut menentukan mode perangkat. Contohnya, whitelist baru yang belum diisi tidak mematikan blacklist yang sudah berlaku. Jika semua kebijakan yang mengenai perangkat kosong, perangkat tidak difilter.

### Input domain

- Domain dinormalisasi seperti yang dilakukan agent: skema, `*.`, port, dan path dibuang, lalu diubah ke huruf kecil.
- Domain ditolak dengan kode alasan: `empty`, `invalid_chars`, `ip_literal`, `too_long`, `no_tld`, `idn_not_supported` (gunakan bentuk punycode `xn--...`), `duplicate_existing`, atau `duplicate_in_batch`.
- **Tambah massal**: `POST /api/filter/policies/{id}/rules/bulk`, maksimal 5000 baris dan 1 MiB.
  - Hasilnya per baris: `added`, `duplicate`, atau `invalid` beserta alasannya.
  - Domain yang valid disimpan dalam satu transaksi, dan perangkat menerima kebijakan baru **satu kali**.
  - Console bisa menempel teks atau mengimpor `.txt`/`.csv`. Format file hosts (`0.0.0.0 contoh.com`) dan kolom pertama CSV dikenali.

### Cakupan subdomain

| Mode | Yang terkena |
|---|---|
| Whitelist | Domain beserta semua subdomainnya (`contoh.com` juga mengizinkan `mail.contoh.com`). |
| Blacklist | Domain itu sendiri serta versi `www.` dan `m.`-nya, lewat file hosts, plus alamat IP hasil resolve-nya di firewall. Subdomain lain perlu dimasukkan tersendiri. |

### Persistensi di agent

Agent menyimpan dua file di folder yang sama dengan `creds.json`:
- **`filter-state.json`**: kebijakan terakhir yang diterapkan. Saat agent start, kebijakan ini diterapkan lagi sebelum agent terhubung ke server, lalu hasilnya dilaporkan.
- **`dns-backup.json`**: setting DNS adapter sebelum mode whitelist mengarahkannya ke `127.0.0.1`. File ini ditulis sebelum adapter diubah dan dihapus setelah DNS dikembalikan.
  - Jika agent mati mendadak saat whitelist aktif, start berikutnya memakai backup ini untuk menjalankan whitelist lagi, atau untuk mengembalikan DNS jika whitelist tidak lagi berlaku.

Agent baru mengirim capability `filter.persist`. Server hanya melewati pengiriman ulang saat reconnect untuk agent yang punya capability itu. Agent lama tetap dikirimi ulang kebijakannya setiap kali terhubung.

### Batasan per OS

- **Whitelist**:
  - Windows: DNS IPv4 tiap adapter diarahkan ke `127.0.0.1`. Adapter yang menolak membuat status menjadi *degraded*, dan jika semua adapter menolak, status menjadi *gagal*.
  - Linux: memakai `resolvectl`. Jika tidak tersedia, `/etc/resolv.conf` ditulis ulang dan nameserver lain dikomentari.
  - macOS: **tidak didukung** dan dilaporkan gagal.
- **Blacklist di Linux**: entri firewall nftables kedaluwarsa setelah 1 jam, sehingga agent memasangnya ulang setiap 30 menit.
