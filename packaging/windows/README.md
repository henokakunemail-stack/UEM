# Windows Agent Installer (NSIS)

Folder ini berisi konfigurasi dan skrip untuk membuat installer GUI Windows (`EndpointAgent-Setup.exe`) menggunakan **NSIS (Nullsoft Scriptable Install System)**.

---

## 1. Fitur Installer

- **Tampilan GUI Modern**: Menggunakan NSIS Modern UI 2 (`MUI2`).
- **Halaman Konfigurasi Server**: Menanyakan URL Server Pusat dan Enrollment Token.
- **Pemasangan Sebagai Windows Service**:
  - Menyalin biner ke `C:\Program Files\EndpointAgent\endpoint-agent.exe`.
  - Mendaftarkan biner ke Windows SCM (`sc.exe create endpoint-agent`).
  - Mengonfigurasi path kredensial sistem `C:\ProgramData\EndpointAgent\creds.json`.
  - Memulai service secara otomatis.
- **Uninstaller Windows Terintegrasi**:
  - Muncul di menu Windows **"Settings > Apps > Installed apps"** (atau Control Panel *Programs and Features*).
  - Menghentikan dan menghapus service `endpoint-agent`.
  - Menghapus biner, log, dan kredensial.

---

## 2. Cara Build Installer

### Prasyarat
1. Pasang compiler NSIS di komputer Windows Anda:
   ```powershell
   winget install NSIS.NSIS
   ```
2. Pastikan Go 1.24+ terpasang untuk mengompilasi biner agen.

### Menjalankan Build
Jalankan skrip PowerShell:
```powershell
cd packaging\windows
.\build.ps1
```

Skrip akan:
1. Mengompilasi `agent-windows-amd64.exe` secara pure Go (`CGO_ENABLED=0`).
2. Menjalankan `makensis.exe agent.nsi`.
3. Menghasilkan biner installer: `packaging\windows\EndpointAgent-Setup.exe`.

---

## 3. Instalasi Senyap / Otomatis (Silent Deployment via GPO / InTune)

Installer NSIS ini juga mendukung parameter baris perintah untuk instalasi massal tanpa dialog GUI:

```cmd
EndpointAgent-Setup.exe /S
```
*(Catatan: `/S` bersifat case-sensitive pada NSIS)*

---

## 4. Penandatanganan Kode (Wajib Sebelum Distribusi)

Installer yang belum ditandatangani akan memicu SmartScreen di setiap mesin endpoint,
dan ditolak langsung oleh kebijakan WDAC di sebagian besar lingkungan perusahaan.
Penandatanganan bukan opsional.

Penandatanganan dilakukan oleh skrip terpisah, `sign.ps1`, dan **bukan** oleh
`build.ps1` atau `build-server.ps1`. Ini disengaja: sertifikat adalah kredensial,
dan sertifikat tidak boleh pernah terjangkau dari build biasa. Kalau build
skrip menandatangani otomatis ketika sertifikat kebetulan ada, maka
"apakah artefak ini sudah ditandatangani?" menjadi properti dari siapa pun yang
menjalankan build — dan properti itulah yang justru harus tetap.

### 4.1 Dua jenis sertifikat, tidak dapat saling menggantikan

| Jenis | Kapan dipakai | Diterima oleh |
|---|---|---|
| **Code signing dari CA publik** (DigiCert, Sectigo, GlobalSign) | Distribusi ke mana pun, termasuk mesin yang tidak pernah Anda sentuh | SmartScreen, WDAC, GPO |
| **Self-signed** | Armada internal saja, di mana publisher sudah di-install ke store *Trusted Publishers* di setiap mesin | Hanya mesin yang sudah diarahkan untuk mempercayainya |

Self-signed **tidak** cukup untuk distribusi publik. Nama publisher di dalam
sertifikat adalah yang dilihat operator di properti berkas, dan tidak bisa
dipilih pada saat penandatanganan.

### 4.2 Prasyarat

`signtool.exe` berasal dari Windows SDK, **bukan** dari Windows itu sendiri:

```powershell
winget install Microsoft.WindowsSDK.10.0.26100
# atau: Visual Studio Build Tools dengan workload "Desktop development with C++"
```

Skrip menelusuri `Windows Kits\10\bin` sendiri, jadi tidak perlu menambahkan
path secara manual.

### 4.3 Menandatangani

```powershell
cd packaging\windows
.\sign.ps1 `
  -Files EndpointAgent-Setup.exe,EndpointServer-Setup.exe `
  -CertPath C:\certs\publisher.pfx `
  -CertPassword $env:SIGNING_PW `
  -TimestampUrl http://timestamp.digicert.com
```

Untuk sertifikat yang sudah ada di certificate store (HSM / YubiKey, yang tidak
bisa di-export), pakai `-CertThumbprint` sebagai gantinya.

`TimestampUrl` **tidak boleh dikosongkan**. Tanpa timestamp, tanda tangan hanya
valid selama sertifikat masih berlaku; ketika sertifikat kedaluwarsa, setiap
salinan yang sudah tersebar di armada mulai menampilkan peringatan. SmartScreen
juga memperlakukan tanda tangan kedaluwarsa sebagai tidak ditandatangani.

### 4.4 Verifikasi

Skrip memverifikasi setiap berkas segera setelah menandatanganinya, bukan hanya
memercayai exit code. Tanda tangan bisa berhasil dibuat dan tetap tidak valid:
timestamp server yang tidak terjangkau, atau algoritma yang tidak diterima OS
target, keduanya keluar dengan exit 0 pada langkah signing dan gagal di sini.

Verifikasi ulang kapan pun:

```powershell
.\sign.ps1 -Files EndpointAgent-Setup.exe -Verify
```

### 4.5 Di CI

Job `release` di `.github/workflows/ci.yml` menjalankan build, tanda tangan,
verifikasi, lalu mengunggah artefak. Job ini hanya jalan pada push ke branch
default.

Dua repository secret yang dibutuhkan:

| Secret | Isi |
|---|---|
| `WINDOWS_CERTIFICATE` | Isi file `.pfx` dalam base64 |
| `WINDOWS_CERTIFICATE_PASSWORD` | Password file `.pfx` |

Membuat base64 dari `.pfx`:

```powershell
[Convert]::ToBase64String([IO.File]::ReadAllBytes("publisher.pfx")) | Out-File publisher.b64
```

Jika salah satu secret belum ada, job **gagal dengan sengaja** dan tidak
menerbitkan apa pun. Artefak tanpa tanda tangan tidak boleh pernah terbit
karena kelalaian. Berkas `.pfx` dihapus dari runner dengan `if: always()`, jadi
build yang gagal tidak meninggalkan kredensial yang masih bisa dipakai di
runner bersama.
