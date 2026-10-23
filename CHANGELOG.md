# Changelog

Semua perubahan penting pada Enterprise Endpoint Management dicatat di sini.
Format mengikuti [Keep a Changelog](https://keepachangelog.com/id-ID/1.1.0/), dan versi mengikuti
[Semantic Versioning](https://semver.org/lang/id/).

Saat rilis dibuat, bagian **[Unreleased]** diganti namanya menjadi nomor versi (mis. `## [1.5.5] - 2026-10-23`).
Workflow rilis memakai bagian dengan nomor versi yang sama dengan tag sebagai catatan rilis di GitHub.
Riwayat sebelum v1.5.4 ada di `git log`.

## [Unreleased]

## [1.5.7] - 2026-10-23

### Ditambahkan
- **Status join domain di detail perangkat.** Tab Hardware punya kartu baru, **Keanggotaan Domain**. Kartu ini
  menunjukkan apakah perangkat sudah join domain, nama domainnya, dan jenisnya: Active Directory, Microsoft Entra ID,
  Hybrid, FreeIPA, atau Workgroup.
  - Windows membaca status join, domain DNS, dan join Entra ID. Linux membaca konfigurasi sssd (realmd, ipa-client)
    dan winbind. macOS membaca binding Active Directory (`dsconfigad`). Semuanya dibaca lokal, tanpa menghubungi
    domain controller.
  - Data dikirim sebagai field baru `os.domain` di laporan inventory. Server menyimpannya tanpa migrasi database.
- **Penanda "Baru" di menu Devices.** Perangkat yang agent-nya terdaftar dalam 24 jam terakhir diberi label **Baru**
  di samping hostname, dengan keterangan waktu pendaftarannya. Perangkat yang dipensiunkan dan pra-registrasi yang agent-nya
  belum terdaftar tidak diberi label.
- **Audit perubahan domain.** Perangkat yang join, keluar, atau pindah domain tercatat di audit trail sebagai
  `inventory.hw_changed` (`changed: domain`).
- Dokumentasi alur pengumpulan inventory: `docs/architecture/inventory-collection.md`.
- **Deploy agent massal lewat GPO Active Directory.**
  - Startup script siap pakai: `packaging/windows/gpo/deploy-agent.cmd`. Skrip memasang dan mendaftarkan agent
    sekali, melewati komputer yang sudah terpasang (tidak menimpa agent yang sudah update sendiri), dan mencatat
    hasilnya di `C:\ProgramData\EndpointAgent\gpo-deploy.log`.
  - Panduan langkah demi langkah: `docs/installation/agent-gpo.md`. Isinya: token multi-perangkat, share yang hanya
    bisa dibaca akun komputer, uji sebagai SYSTEM, GPO, root CA, verifikasi, dan pencabutan token setelah rollout.

### Diperbaiki
- **`enrolled_at` untuk token satu perangkat kini berisi waktu agent benar-benar mendaftar,** bukan waktu token dibuat.
  Sebelumnya perangkat yang token-nya dibuat beberapa hari sebelum agent dipasang tampak sudah lama terdaftar.

### Catatan upgrade
- Kartu domain dan label "Baru" ada di console server v1.5.7. Status domain memerlukan agen v1.5.7: perbarui agen dari
  **Agent Updates → Impor dari Paket Server**, lalu jalankan kampanye ke 1.5.7.
- Status domain baru muncul setelah agent diperbarui ke versi ini dan mengirim inventory berikutnya. Itu terjadi saat
  agent start, setiap 4 jam, atau saat tombol **Collect** ditekan. Agent lama ditampilkan "Tidak diketahui", bukan
  "Belum join".

## [1.5.6] - 2026-10-22

### Diubah
- **Remote Control:** Menghilangkan banner notifikasi sesi pada layar endpoint agent (`Your screen is being controlled by...` / `Your screen is being viewed by...`).

### Diperbaiki
- **Kampanye update agen tidak menampilkan perangkat yang sudah di versi target.**
  - Setiap perangkat yang dicek kini tercatat di tab **Impacted Agents**, termasuk yang sudah menjalankan versi target. Statusnya "Sudah di versi ini", dan tidak ada yang dikirim ke perangkat itu.
  - Ringkasan kampanye punya kolom baru "Sudah di versi ini", dan progresnya mencapai 100%.
- **Kampanye yang targetnya tidak berisi perangkat aktif tertulis "semua perangkat sudah di versi ini".** Kini tertulis "Tidak ada perangkat yang cocok dengan target kampanye ini". Pesan saat memulai kampanye juga membedakan kedua keadaan itu.
- **Perangkat yang di-retire ikut menjadi target kampanye update.** Ini termasuk baris pending enrollment yang di-retire otomatis. Perangkat seperti ini mendapat tugas yang tidak akan pernah selesai.

### Catatan upgrade
- Perubahan kampanye ada di server dan console.
- Penghapusan banner remote control memerlukan agen v1.5.6 di endpoint agar banner tidak lagi muncul saat sesi remote.
- Kampanye yang dimulai sebelum server diperbarui tidak mencatat perangkat yang sudah di versi target. Untuk kampanye lama seperti itu, jumlah perangkat yang dicek ada di **Rollout Activity Logs**.

## [1.5.5] - 2026-10-22

### Ditambahkan
- **Installer macOS.**
  - `EndpointAgent-macOS.pkg` (universal Apple Silicon + Intel), dibangun di runner macOS.
  - `endpoint-agent-darwin-<arch>.tar.gz` dengan `install-agent.sh` yang memasang LaunchDaemon `com.endpoint-mgmt.agent`.
  - Uninstaller agen yang meminta token (`endpoint-agent-uninstall` ikut terpasang dari `.pkg`).
  - Pendaftaran tanpa interaksi untuk MDM: letakkan `/etc/endpoint-agent/enroll.conf` sebelum `.pkg` dipasang.
- **Server untuk macOS dan Linux arm64.**
  - `endpoint-server-darwin-<arch>.tar.gz` dengan `install-server.sh` (LaunchDaemon `com.endpoint-mgmt.server`).
  - `endpoint-server-linux-arm64.tar.gz`.
  - Skrip install/update Linux memilih biner sesuai arsitektur mesin.
- **Agen untuk semua platform di setiap paket server.** Folder `agents/` (Windows, Linux amd64/arm64, macOS amd64/arm64)
  kini ada di tarball Linux/macOS, `endpoint-server-windows-amd64.zip`, dan `EndpointServer-Setup.exe`.
  `update-server.sh` dan `update-server.ps1` ikut memperbaruinya.
- **Agent Updates → Impor dari Paket Server.** Konsol mendaftarkan agen bawaan server sebagai rilis dalam satu langkah.
  Versi dan platform dibaca dari biner itu sendiri, jadi agen yang sudah terdaftar dapat di-update lewat kampanye
  tanpa mengunduh dan mengunggah ulang secara manual. API: `GET /api/agent-updates/bundled`,
  `POST /api/agent-updates/bundled/import`.
- Aset rilis dibangun oleh satu skrip, `scripts/build-release.ps1`, dipakai oleh `publish-release.ps1` dan workflow rilis.
  `checksums.txt` mencakup semua aset, termasuk `.pkg`.
- CI memeriksa build server untuk 5 platform, isi dan mode file arsip rilis, paket `.pkg` macOS, dan menjalankan E2E
  update agen nyata (`scripts/e2e-agent-selfupdate.ps1`).
- **System Policy: setiap setting bisa "Tidak diatur", "Izinkan", atau "Blokir" per kebijakan** (USB juga "Read-only").
  - Kebijakan hanya menentukan setting yang diisi.
  - Jika beberapa kebijakan mengisi setting yang sama, cakupan paling spesifik yang menang (perangkat > grup > semua), lalu
    prioritas.
  - "Izinkan" eksplisit di tingkat perangkat bisa menjadi pengecualian dari blokir armada.
- **Tab "Kebijakan efektif"** di System Policy: untuk satu perangkat, nilai setiap setting dan kebijakan yang
  menentukannya. API: `sources` dan `matched_policies` di `GET /api/devices/{id}/system-policy`.
- **Status "Sebagian diterapkan".**
  - Agen melaporkan hasil per setting: diterapkan, gagal, atau tidak didukung di OS ini.
  - Console menampilkan setting yang tidak diterapkan beserta alasannya.
- **Network Filter: tambah domain massal.**
  - Tempel banyak baris atau impor `.txt`/`.csv` (format file hosts dikenali), sampai 5000 baris.
  - Ada laporan per baris, dan perangkat menerima kebijakan baru satu kali.
  - API: `POST /api/filter/policies/{id}/rules/bulk`.
- **Dokumentasi** `docs/architecture/policy-resolution.md`: urutan penentuan kebijakan dan dukungan setiap setting per OS.

### Diperbaiki
- **Blokir USB tertimpa diam-diam.** Setiap kebijakan menyimpan USB "allow" secara otomatis, sehingga kebijakan yang hanya
  mengatur wallpaper pun membuka blokir USB. Nilai otomatis pada kebijakan lama kini dibaca sebagai "Tidak diatur".
- **Whitelist kosong mematikan blacklist.** Kebijakan filter tanpa rule tidak lagi ikut menentukan mode perangkat.
- **Whitelist hilang setelah agen restart.**
  - Agen menyimpan kebijakan filter terakhir dan menerapkannya lagi saat start.
  - Setting DNS adapter disimpan sebelum diubah, sehingga agen yang mati mendadak tidak lagi meninggalkan DNS di
    `127.0.0.1`.
  - Entri firewall Linux yang kedaluwarsa setiap jam dipasang ulang.
- **Perubahan anggota grup tidak memicu sinkron ulang.** Menambah atau mengeluarkan perangkat dari grup kini langsung
  mengirim ulang system policy dan filter. Grup yang masih dipakai system policy tidak bisa dihapus.
- **Linux/macOS melapor "synced" untuk setting yang tidak diterapkan.**
  - Setting khusus Windows, wallpaper, dan kunci layar di Linux/macOS, USB di macOS, serta whitelist di macOS kini
    dilaporkan "tidak didukung" atau "gagal".
  - Di Windows, penulisan registry yang ditolak (Windows Update, screen saver, restriksi, USB, wallpaper) kini
    dilaporkan.
- **Laporan agen yang cepat tertimpa kembali menjadi "pending",** dan laporan untuk versi kebijakan lama ikut
  dicatat. Server kini mengabaikan laporan basi dan menolak status yang tidak dikenal.
- **Input domain tidak divalidasi.** IP, nama tanpa TLD, dan duplikat kini ditolak dengan alasan yang jelas.
- **Menghapus perangkat secara permanen gagal di PostgreSQL,** karena kodenya menghapus dari dua tabel yang tidak ada.
- **Jadwal yang mengenai perangkat offline lebih dulu langsung dicatat gagal,** padahal perangkat lain masih berjalan.
- **Agen macOS tidak pernah bisa di-update.** Formulir unggah mengirim `os_name=darwin`, sedangkan agen melaporkan
  `macos`, sehingga setiap Mac mendapat `no_release`. Rilis lama berlabel `darwin` tetap dikenali.
- **Update agen gagal lalu rollback di Linux dengan `/tmp` bertipe tmpfs, atau di Windows bila agen dipasang di drive
  selain C:.** Berkas unduhan kini disimpan di folder yang sama dengan agen.
- **Restart agen macOS setelah update memakai label launchd yang salah.** Agen yang dijalankan manual dari Terminal
  berhenti dan tidak hidup lagi.
- **Unggah rilis kini menolak biner yang tidak cocok dengan formulir:** dibangun untuk OS/arsitektur lain, atau berversi
  lain. Versi yang berbeda membuat kampanye tidak pernah selesai. Awalan `v` pada versi diabaikan.
- **Server di `EndpointServer-Setup.exe` melaporkan versi `dev`.**
- **Pembuatan `.pkg` macOS menulis ulang deklarasi XML sehingga ditolak `productbuild`.** Skrip pkg juga tidak
  dijalankan bila checkout kehilangan bit eksekusi.
- **Arsip rilis yang dibuat di Windows berisi berkas 0666 tanpa bit eksekusi,** sehingga `./install.sh` gagal dengan
  *permission denied*.

### Catatan upgrade
- Agen yang sudah terpasang (termasuk v1.5.4) dapat di-update dari konsol: perbarui server, buka **Agent Updates →
  Rilis → Impor dari Paket Server**, lalu jalankan kampanye ke versi baru.
- **Migrasi database 0029** menambah kolom `results_json` di `device_system_policy_states` dan berjalan otomatis.
- **Kebijakan sistem yang sudah ada:** nilai "allow", "off", dan 0 yang dulu terisi otomatis kini berarti "Tidak
  diatur". Periksa tab **Kebijakan efektif**. Jika memang perlu pengecualian, misalnya USB diizinkan untuk satu perangkat,
  atur "Izinkan" secara eksplisit di kebijakan perangkat itu.
- **Agen lama tetap bekerja.** Mereka belum mengirim hasil per setting, dan server mengirim ulang kebijakan filter
  setiap kali mereka terhubung sampai diperbarui.
