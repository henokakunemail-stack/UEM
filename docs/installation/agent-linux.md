# Instalasi Agen Linux

Sama seperti versi Windows: koneksi agen **outbound 100%**, jadi tidak ada
port inbound yang perlu dibuka di firewall jaringan.

---

## 1. Yang Anda butuhkan

- Binary `endpoint-agent` untuk arsitektur mesin (lihat
  [prerequisites.md](prerequisites.md) untuk build).
- URL server pusat, misal `https://mgmt.perusahaan.com`.
- Satu enrollment token dari Web Console.
- `nft` (nftables) dan salah satu dari `apt-get` / `dnf` / `yum`. Keduanya
  bukan opsional — tanpa `nft` filter jaringan mati, dan tanpa package manager
  scan patch **gagal dengan error**, bukan melaporkan "tidak ada patch".

---

## 2. Buat Enrollment Token

Di Web Console: **Devices → Generate Enrollment Token**.

Modal menampilkan token sekali saja, bersama perintah siap-salin untuk Linux:

```bash
endpoint-agent -server https://mgmt.perusahaan.com -enroll <TOKEN>
```

> Token punya masa berlaku (`ENROLLMENT_TTL`, default 30 menit). Setelah lewat
> akan ditolak dengan `invalid or expired enrollment token`.

---

## 3. Pasang binary

```bash
sudo install -m 0755 endpoint-agent /usr/local/bin/endpoint-agent
```

---

## 4. Jalankan sekali untuk enrollment

```bash
sudo -u endpointmgmt /usr/local/bin/endpoint-agent \
  -server https://mgmt.perusahaan.com \
  -creds /var/lib/endpoint-agent/creds.json \
  -enroll <TOKEN>
```

Sekitar 30 detik kemudian komputer muncul di console sebagai **online**.

Kredensial disimpan di file yang Anda sebutkan lewat `-creds`. Tanpa flag itu,
agen memakai bawaan `$HOME/.endpoint-mgmt/agent-creds.json` milik user yang
menjalankannya — yang untuk `endpointmgmt` bukan tempat yang masuk akal, dan
pasti bukan yang akan dibaca service di langkah berikutnya.

Jalankan perintah ini sebagai `endpointmgmt`, bukan `root`, supaya file
kredensial langsung dimiliki user yang akan dipakai service:

```bash
sudo install -d -o endpointmgmt -g endpointmgmt -m 0700 /var/lib/endpoint-agent
```

**File ini adalah bukti kepemilikan device itu.** Siapa pun yang membacanya bisa
menyamar sebagai mesin tersebut — jadi atur permission-nya `0600` dan jangan
pernah menempelkan token `-enroll` ke command line yang tercatat di log
(history shell atau systemd journal).

---

## 5. Daftarkan sebagai systemd service

Buat `/etc/systemd/system/endpoint-agent.service`:

```ini
[Unit]
Description=Endpoint Management Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=endpointmgmt
Group=endpointmgmt
ExecStart=/usr/local/bin/endpoint-agent -server https://mgmt.perusahaan.com -creds /var/lib/endpoint-agent/creds.json

# Agen membuka WebSocket ke server; butuh hak admin untuk filter dan patch.
AmbientCapabilities=CAP_NET_ADMIN
CapabilityBoundingSet=CAP_NET_ADMIN

Restart=always
RestartSec=5s

# Secret tidak boleh bocor lewat log systemd.
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
NoNewPrivileges=true

[Install]
WantedBy=multi-user.target
```

`-creds` di sini harus **sama persis** dengan yang dipakai di langkah 4. Kalau
berbeda, service berjalan tapi tidak punya device secret, jadi tidak pernah
menghubungi server.

Buat user-nya kalau belum ada:

```bash
sudo useradd --system --home /var/lib/endpoint-agent \
             --shell /usr/sbin/nologin endpointmgmt
```

Lalu aktifkan:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now endpoint-agent
sudo systemctl status endpoint-agent
```

### Kenapa `CAP_NET_ADMIN` wajib

Agen menulis aturan nftables sendiri. Tanpa capability itu, filter jaringan
ditolak kernel — dan console akan menandai firewall tidak aktif. Scan dan
install patch butuh hak yang sama: `apt`/`dnf` menolak menjalankan operasi
sebagai user biasa, dan `apt-get install` ditolak tanpa itu.

Kalau Anda tidak berniat menjalankan filter di mesin ini, capability itu tetap
dibutuhkan untuk patch management.

---

## 6. Verifikasi

```bash
systemctl status endpoint-agent
journalctl -u endpoint-agent -f
```

Di Web Console, mesin harus tampil **online** dengan *last seen* bergerak.

| Gejala | Penyebab |
|---|---|
| `invalid or expired enrollment token` | Token lewat masa berlaku. Buat yang baru. |
| Timeout ke server | URL salah, atau firewall keluar memblokir. |
| Service jalan tapi device tidak pernah muncul | `-creds` di `ExecStart` tidak sama dengan file yang dipakai saat enrollment. |
| Console online tapi filter/patch tidak aktif | Capability hilang, atau package manager tidak ada. |
| `no supported package manager found` | Scan gagal di mesin ini. Itu error yang disengaja — mesin tanpa package manager memang tidak bisa di-manage patch, dan itu harus terlihat, bukan dilaporkan sebagai compliant. |

---

## 7. Uninstall

```bash
sudo systemctl disable --now endpoint-agent
sudo rm /etc/systemd/system/endpoint-agent.service
sudo systemctl daemon-reload
sudo rm -rf /var/lib/endpoint-agent    # menghapus kredensial
```

Device-nya sendiri di-retire dari console, bukan dihapus, supaya riwayat audit
tetap bisa dibaca.

---

## 8. Pemasangan massal

Untuk jumlah lebih dari beberapa dozen mesin, pakai `.deb` dari
`packaging/linux/` — dependensi sudah dideklarasikan di `control` file-nya.
Lihat [`packaging/README.md`](../../packaging/README.md).

Script tercepat untuk server ada di
[`deploy/install-ubuntu.sh`](../../deploy/install-ubuntu.sh) — tapi itu untuk
**server**, bukan agen. Untuk agen, `.deb`-nya yang dipakai.
