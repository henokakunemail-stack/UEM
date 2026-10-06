# Instalasi Agen Windows

Agen adalah proses yang berjalan di setiap komputer/perpisahan yang ingin Anda
kelola. Koneksinya **outbound 100%** ke server — tidak ada port inbound yang
perlu dibuka di firewall kantor cabang.

---

## 1. Yang Anda butuhkan

- Binary `endpoint-agent.exe` (lihat [prerequisites.md](prerequisites.md)
  untuk cara build).
- URL server pusat, misal `https://mgmt.perusahaan.com`.
- Satu enrollment token dari Web Console.

---

## 2. Buat Enrollment Token

Di Web Console: **Devices → Generate Enrollment Token**.

Isi hostname (nama yang akan dikenal komputer ini di fleet), OS, dan site
opsional. Tombol **Generate Token** menampilkan token plaintext **sekali
saja** — ditutup atau di-refresh, token itu hilang dan tidak bisa ditampilkan
lagi, karena server hanya menyimpan hash-nya.

Modal itu juga memberi perintah siap-salin untuk Windows dan Linux, dengan URL
server yang sama dengan tempat console Anda diakses.

> Token punya masa berlaku (`ENROLLMENT_TTL`, default 30 menit). Setelah lewat,
> token ditolak dengan `invalid or expired enrollment token`. Buat token baru
> kalau sudah kedaluwarsa.

---

## 3. Pasang binary

Salin `endpoint-agent.exe` ke folder, misalnya `C:\Program Files\EndpointAgent\`
atau `C:\EndpointAgent\`.

---

## 4. Jalankan

Sekali, untuk mengambil secret permanen:

```cmd
endpoint-agent.exe -server https://mgmt.perusahaan.com -enroll <TOKEN>
```

Sekitar 30 detik kemudian komputer muncul di console dengan status **online**,
sudah terisi hardware dan software pertama.

Secara default agen menyimpan kredensial di:

```
%USERPROFILE%\.endpoint-mgmt\agent-creds.json
```

Installer resmi menimpanya ke `C:\ProgramData\EndpointAgent\creds.json` — bisa
Anda lihat di baris `nsExec` `packaging/windows/agent.nsi:108`. Kalau Anda
mengatur sendiri, sebutkan lokasinya dengan flag `-creds`:

```cmd
endpoint-agent.exe -server https://mgmt.perusahaan.com -creds "C:\ProgramData\EndpointAgent\creds.json" -enroll <TOKEN>
```

Pakai path yang sama persis saat menjalankan service di langkah berikutnya,
karena `-service install` meneruskan flag yang Anda berikan.

**File ini adalah bukti kepemilikan device itu.** Siapa pun yang membacanya
bisa menyamar sebagai komputer tersebut. Jangan pernah menyalinnya, dan
jangan pernah menempelkan token `-enroll` atau `creds.json` ke command line
yang tercatat di log.

---

## 5. Daftarkan sebagai service

Sekali enrollment selesai, jadikan permanen. Service mewarisi **tidak ada**
environment dari shell Anda, jadi `-server` dan `-creds` harus disebutkan
lagi di baris install:

```cmd
endpoint-agent.exe -server https://mgmt.perusahaan.com -creds "C:\ProgramData\EndpointAgent\creds.json" -service install
endpoint-agent.exe -service start
```

Tanpa `-creds`, service akan mencari kredensial di `%USERPROFILE%` milik
akun **LocalSystem** — bukan tempat file Anda ada — dan akan gagal
menghubungi server karena tidak punya device secret.

Service berjalan sebagai **LocalSystem**, yang dibutuhkan untuk filter
jaringan, scan/install patch, dan uninstall software. Kalau Anda menjalankan
binary manual dari command prompt, jalankan dari PowerShell yang sudah
di-elevate — kalau tidak, ketiga fitur itu akan ditolak dan console akan
menandainya tidak aktif.

Status service:

```cmd
endpoint-agent.exe -service status
```

---

## 6. Uninstall

```cmd
endpoint-agent.exe -service stop
endpoint-agent.exe -service uninstall
```

Lalu hapus folder kredensial yang Anda pakai di langkah 4 kalau Anda ingin
menghapus device secret juga. Device-nya sendiri di-retire dari console, bukan
dihapus, supaya riwayat audit tetap bisa dibaca.

---

## 7. Verifikasi

```cmd
endpoint-agent.exe -service status
curl.exe -s https://mgmt.perusahaan.com/healthz
```

Lalu di Web Console: komputer harus tampil **online** dengan
*last seen* yang bergerak.

Kalau komputer tetap offline:

| Gejala | Penyebab |
|---|---|
| `invalid or expired enrollment token` | Token lewat masa berlaku. Buat token baru. |
| Koneksi ditolak / timeout | URL server salah, atau firewall keluar memblokir. |
| Service jalan tapi device tidak pernah muncul | `-creds` di baris `-service install` tidak sama dengan file yang dipakai saat enrollment. |
| Console bilang online tapi filter/patch tidak aktif | Agen tidak berjalan sebagai administrator. |

---

## 8. Pemasangan massal

Untuk jumlah lebih dari beberapa dozen mesin, jangan pakai langkah manual.
`packaging/windows/agent.nsi` membangun `EndpointAgent-Setup.exe` yang
menerima `-server` dan `-enroll` dan mendaftarkan service-nya sendiri. Lihat
[`packaging/README.md`](../../packaging/README.md).
