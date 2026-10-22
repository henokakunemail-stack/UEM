# Deploy Agent Windows ke Banyak Komputer dengan GPO Active Directory

Panduan ini memasang dan mendaftarkan Endpoint Agent ke seluruh komputer Windows di domain secara otomatis,
memakai **Computer Startup Script** di Group Policy dan skrip siap pakai
[`packaging/windows/gpo/deploy-agent.cmd`](../../packaging/windows/gpo/deploy-agent.cmd).

Installer agent berupa `.exe` (NSIS), bukan `.msi`, sehingga dipasang lewat startup script, bukan lewat Software
Installation. Startup script berjalan saat komputer menyala, sebelum user login, sebagai `NT AUTHORITY\SYSTEM`. Di
jaringan, ia tampil sebagai akun komputer (`DOMAIN\NAMAPC$`).

## Cara kerja skrip

Setiap kali komputer menyala, skrip:

1. **Keluar tanpa melakukan apa pun** bila service `endpoint-agent` sudah ada dan
   `C:\ProgramData\EndpointAgent\creds.json` sudah ada (agent sudah terpasang dan terdaftar).
2. Bila belum, menyalin `EndpointAgent-Setup.exe` dari share ke `C:\Windows\Temp`, lalu menjalankannya senyap:
   `/S /SERVER=... /TOKEN=... [/SITE=...]`.
3. Mencatat hasilnya di `C:\ProgramData\EndpointAgent\gpo-deploy.log`. Bila gagal, mencoba lagi di boot berikutnya.

Skrip **tidak** memasang ulang agent yang sudah ada. Agent yang terpasang di-update dari console
(**Agent Updates**), bukan dari GPO. Bila installer di share dijalankan ulang, binary di share (yang mungkin lebih
lama) akan menimpa agent yang sudah meng-update dirinya sendiri.

> **Kenapa bukan `install-agent.ps1`?** Skrip itu dibuat untuk instalasi manual. Skrip itu tidak memeriksa apakah
> agent sudah terpasang, jadi bila dijadikan startup script, setiap reboot ia menghentikan service, menyalin ulang
> binary dari share, dan mendaftarkan ulang service. Gunakan `deploy-agent.cmd`.

---

## Prasyarat

| Kebutuhan | Keterangan |
|---|---|
| Server UEM | Bisa dijangkau dari subnet workstation di port server (default `8443`). |
| URL server | Tulis **persis sama** dengan yang dipakai agent lain, misalnya `https://uem.corp.local:8443`. Penulisan berbeda untuk server yang sama (IP vs nama, dengan atau tanpa port) membuat agent dianggap pindah server dan mendaftar ulang. |
| Sertifikat HTTPS | Bila server memakai sertifikat CA internal, root CA itu harus dipercaya Windows (lihat Langkah 5). Agent memakai trust store Windows. |
| File server | Folder share untuk installer dan skrip. |
| Hak admin | Domain Admin (atau delegasi GPO) untuk membuat dan menautkan GPO. |
| Installer | `EndpointAgent-Setup.exe` dari halaman rilis GitHub, sebaiknya versi yang sama dengan server. |

---

## Langkah 1 — Buat token enrollment multi-perangkat

1. Buka console UEM → **Devices** → **Buat Token Pendaftaran**.
2. **Jenis Pendaftaran:** pilih **Multi-Perangkat / Batch**.
3. **Nama / Label Token:** misalnya `GPO - Workstation HQ`.
4. **Batas Perangkat:** isi kira-kira jumlah komputer target ditambah cadangan. Hindari "Tanpa batas" agar token
   yang bocor tidak bisa dipakai tanpa batas.
5. **Durasi Kedaluwarsa:** sesuaikan dengan lama rollout, misalnya **7 Hari** atau **30 Hari**.
6. **Site Tujuan:** opsional. Bisa juga diisi per skrip lewat `SITE`.
7. Klik **Buat Token**, lalu **salin token sekarang**. Token hanya ditampilkan sekali; server hanya menyimpan
   hash-nya.

Satu token bisa mendaftarkan banyak komputer, dan setiap komputer tetap mendapat identitas dan secret sendiri.
Komputer yang sudah terdaftar tidak memakai kuota token lagi.

---

## Langkah 2 — Siapkan share khusus (jangan NETLOGON)

`deploy-agent.cmd` berisi token enrollment. **Jangan** taruh di `NETLOGON` atau `SYSVOL`: keduanya bisa dibaca
semua user domain, dan siapa pun yang memegang token bisa mendaftarkan perangkat ke server Anda.

Buat share tersembunyi yang **hanya bisa dibaca akun komputer**. Jalankan di file server (PowerShell sebagai
Administrator) dan ganti `CORP` dengan nama NetBIOS domain Anda:

```powershell
New-Item -ItemType Directory -Path D:\UEM -Force
New-SmbShare -Name 'UEM$' -Path 'D:\UEM' -ReadAccess 'CORP\Domain Computers' -FullAccess 'CORP\Domain Admins'
```

```powershell
icacls D:\UEM /inheritance:r /grant:r 'CORP\Domain Computers:(OI)(CI)RX' 'CORP\Domain Admins:(OI)(CI)F' 'SYSTEM:(OI)(CI)F'
```

Hasilnya `\\fileserver.corp.local\UEM$`: komputer domain bisa membaca, user biasa tidak. Tanda `$` menyembunyikan
share dari daftar browse, tetapi izinnyalah yang benar-benar melindungi token.

---

## Langkah 3 — Taruh installer dan skrip di share

1. Unduh `EndpointAgent-Setup.exe` dan `checksums.txt` dari rilis GitHub.
2. Cocokkan hash installer dengan baris `EndpointAgent-Setup.exe` di `checksums.txt`:
   ```powershell
   (Get-FileHash .\EndpointAgent-Setup.exe -Algorithm SHA256).Hash.ToLower()
   ```
3. Salin `EndpointAgent-Setup.exe` ke `D:\UEM\`.
4. Salin `packaging/windows/gpo/deploy-agent.cmd` ke `D:\UEM\`, lalu isi empat nilai di bagian atasnya:

   ```bat
   set "SHARE=\\fileserver.corp.local\UEM$"
   set "SERVER=https://uem.corp.local:8443"
   set "TOKEN=<token dari Langkah 1>"
   set "SITE=Jakarta-HQ"
   ```

   `SITE` boleh kosong. Bila diisi, jangan pakai spasi; gunakan `Jakarta-HQ`, bukan `Jakarta HQ`.

Untuk cabang dengan site berbeda, buat salinan skrip per cabang (misalnya `deploy-agent-medan.cmd` dengan
`SITE=Medan-Branch`) dan tautkan GPO terpisah ke OU cabang itu.

---

## Langkah 4 — Uji di satu komputer sebelum GPO

Jalankan skrip sebagai SYSTEM, persis seperti saat startup, di satu PC uji (Command Prompt sebagai Administrator):

```bat
schtasks /create /tn "UEM-GPO-Test" /tr "\\fileserver.corp.local\UEM$\deploy-agent.cmd" /sc once /st 00:00 /ru SYSTEM /f
schtasks /run /tn "UEM-GPO-Test"
```

Tunggu sekitar 30 detik, lalu periksa:

```bat
type C:\ProgramData\EndpointAgent\gpo-deploy.log
sc query endpoint-agent
schtasks /delete /tn "UEM-GPO-Test" /f
```

Log harus berisi `installed and enrolled (exit 0)`, service harus `RUNNING`, dan perangkat harus muncul di console
**Devices**. Jalankan `schtasks /run` sekali lagi: kali ini log tidak boleh bertambah, karena skrip langsung keluar.

---

## Langkah 5 — Buat dan tautkan GPO

Di Domain Controller, buka **Group Policy Management** (`gpmc.msc`).

1. Klik kanan **OU pilot** (mulai dari OU kecil, misalnya `IT-Pilot`) →
   **Create a GPO in this domain, and Link it here…** → beri nama `UEM - Deploy Endpoint Agent`.
2. Klik kanan GPO itu → **Edit**.
3. **Startup script:**
   `Computer Configuration → Policies → Windows Settings → Scripts (Startup/Shutdown) → Startup`
   → tab **Scripts** → **Add** → *Script Name*: `\\fileserver.corp.local\UEM$\deploy-agent.cmd`, *Script
   Parameters*: kosong → **OK**.
4. **Tunggu jaringan saat boot** (wajib; tanpa ini share sering belum terjangkau saat skrip berjalan, terutama di
   Wi-Fi atau Fast Startup):
   `Computer Configuration → Policies → Administrative Templates → System → Logon →
   Always wait for the network at computer startup and logon` → **Enabled**.
5. **Root CA internal** (hanya bila server memakai sertifikat CA internal):
   `Computer Configuration → Policies → Windows Settings → Security Settings → Public Key Policies →
   Trusted Root Certification Authorities` → klik kanan → **Import** → pilih file root CA (`.cer`).
6. Opsional, untuk membatasi ke grup komputer tertentu: di tab **Scope** GPO, pada **Security Filtering**, ganti
   `Authenticated Users` dengan grup komputer pilot. Pastikan `Domain Computers` tetap punya izin **Read** di tab
   **Delegation**.

---

## Langkah 6 — Verifikasi di komputer pilot

Di komputer pilot (Command Prompt sebagai Administrator):

```bat
gpupdate /force
gpresult /r /scope computer
```

Pastikan `UEM - Deploy Endpoint Agent` muncul di **Applied Group Policy Objects**, lalu **restart** komputer. Setelah
login, periksa:

| Cek | Perintah / tempat | Hasil yang diharapkan |
|---|---|---|
| Service | `sc query endpoint-agent` | `STATE : 4 RUNNING` |
| Log skrip | `type C:\ProgramData\EndpointAgent\gpo-deploy.log` | `installed and enrolled (exit 0)` |
| Kredensial | `C:\ProgramData\EndpointAgent\creds.json` | file ada |
| Console | **Devices** | perangkat muncul **ONLINE**, site sesuai |
| Pemakaian token | **Devices → tab Token enrollment** | jumlah mesin yang enroll bertambah |

---

## Langkah 7 — Rollout bertahap, lalu cabut token

1. Setelah pilot lancar, tautkan GPO yang sama ke OU lain secara bertahap: klik kanan OU →
   **Link an Existing GPO…**.
2. Pantau **Devices** dan **Devices → tab Token enrollment** (kuota terpakai).
3. Setelah rollout selesai, buka **Devices → tab Token enrollment** → **Cabut** token GPO. Mesin yang sudah terdaftar
   tetap berjalan; hanya mesin baru yang tidak bisa enroll lagi.
4. Untuk komputer baru di kemudian hari, buat token baru dan perbarui `TOKEN` di `deploy-agent.cmd`. GPO tetap
   tertaut; komputer yang sudah terdaftar tidak terpengaruh, karena skrip keluar lebih dulu.

---

## Apakah perangkat bisa terdaftar dua kali?

Dalam pemakaian normal, **tidak**:

- Skrip keluar sebelum memanggil installer bila agent sudah terpasang dan terdaftar.
- Bila installer tetap dijalankan di mesin yang sudah terdaftar, agent lebih dulu menanyakan ke server apakah
  identitasnya masih valid (`/api/agent/identity`). Bila masih valid, identitas lama dipertahankan
  (`already enrolled; keeping the existing identity`) dan kuota token tidak terpakai.

Agent **mendaftar sebagai perangkat baru** hanya bila:

| Keadaan | Akibat |
|---|---|
| URL di `SERVER` berbeda penulisannya dari URL saat agent pertama kali enroll | Agent menganggap pindah server dan enroll ulang. Skrip mencegah ini pada mesin yang sudah terpasang, tetapi tetap gunakan URL yang sama persis. |
| Perangkat dihapus dari console, atau secret-nya ditolak server | Agent enroll ulang dengan token yang ada. Perangkat lama sudah tidak aktif, jadi tidak ada duplikat aktif. |
| Agent dijalankan dengan `-reenroll` | Sengaja mendaftar sebagai perangkat baru. |

---

## Troubleshooting

Semua kegagalan dicatat di `C:\ProgramData\EndpointAgent\gpo-deploy.log` pada komputer target.

| Isi log / gejala | Penyebab | Solusi |
|---|---|---|
| Tidak ada log sama sekali | GPO belum berlaku, atau skrip tidak terbaca | `gpresult /r /scope computer`; pastikan GPO tertaut ke OU tempat **objek komputer** berada (bukan OU user). |
| `...EndpointAgent-Setup.exe is not reachable` | Share belum terjangkau saat boot, atau akun komputer tidak punya izin baca | Aktifkan *Always wait for the network…*; cek izin `Domain Computers` di share dan NTFS. |
| `TOKEN is not configured` | Nilai `TOKEN` belum diganti | Isi token di `deploy-agent.cmd`. |
| `installer exit 2` | `SERVER` kosong | Isi `SERVER`. |
| `installer exit 3` | Enroll ditolak atau server tak terjangkau: `token_expired`, `token_used_up`, `token_revoked`, port diblokir, atau sertifikat tidak dipercaya | Buat token baru atau naikkan batas perangkat; cek firewall ke port server; deploy root CA (Langkah 5.5). Skrip mencoba lagi di boot berikutnya. |
| Service tidak `RUNNING` setelah exit 0 | Service gagal start | `sc query endpoint-agent`, Event Viewer → Windows Logs → System. |
| Installer diblokir | AppLocker atau WDAC menolak `.exe` tak bertanda tangan dari `C:\Windows\Temp` | Tanda tangani installer (`packaging/windows/sign.ps1`) atau buat aturan izin untuk publisher/hash installer. |

## Update dan uninstall

- **Update agent:** lakukan dari console **Agent Updates** (impor dari paket server, lalu jalankan kampanye). Tidak
  perlu mengganti installer di share, kecuali agar komputer baru langsung mendapat versi terbaru.
- **Uninstall:** perangkat yang terdaftar hanya bisa dicopot dengan token uninstall dari console
  (tombol **Buat token uninstall** pada baris perangkat di menu **Devices**), lalu `"C:\Program Files\EndpointAgent\uninstall.exe" /S /TOKEN=<token>`.
  Lepas tautan GPO deploy lebih dulu, agar skrip tidak memasang agent lagi di boot berikutnya.
