# Changelog

Semua perubahan penting pada Enterprise Endpoint Management dicatat di sini.
Format mengikuti [Keep a Changelog](https://keepachangelog.com/id-ID/1.1.0/), dan versi mengikuti
[Semantic Versioning](https://semver.org/lang/id/).

Saat rilis dibuat, bagian **[Unreleased]** diganti namanya menjadi nomor versi (mis. `## [1.5.5] - 2026-10-23`).
Workflow rilis memakai bagian dengan nomor versi yang sama dengan tag sebagai catatan rilis di GitHub.
Riwayat sebelum v1.5.4 ada di `git log`.

## [Unreleased]

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

### Diperbaiki
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
