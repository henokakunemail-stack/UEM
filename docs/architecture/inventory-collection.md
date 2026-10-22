# Pengumpulan Inventory: Alur, Route, dan Penyimpanan

Dokumen ini menjelaskan cara agent mengumpulkan inventory (hardware, software, detail OS termasuk status domain),
cara inventory sampai ke server, di mana disimpan, dan siapa yang membacanya. Semua isi dibaca dari kode saat ini.

## Ringkasan alur

```
Agent                                   Server                                    Console
-----                                   ------                                    -------
inventory.Scheduler (periodik)  ─┐
                                 ├─ Collect() per OS ─► Envelope "inventory" ─► WSHandler.handleInventory
inventory.collect (on-demand)   ─┘   (WebSocket)                                  └► inventoryHandler.acceptInventory
                                                                                      ├ diff vs snapshot lama → audit
                                                                                      └ UPSERT device_inventory
                                                                    GET /api/devices/{id}/inventory ◄─ DeviceDetailModal
                         ◄── Envelope "inventory.collect" ── POST /api/devices/{id}/inventory/collect ◄─ tombol Collect
```

Inventory **tidak** dikirim lewat HTTP. Agent mengirimnya lewat koneksi WebSocket yang sama dengan heartbeat dan perintah
(`/api/agent/connect`), sehingga server sudah tahu perangkat mana pengirimnya dari autentikasi koneksi itu.

## 1. Agent: kapan mengumpulkan

| Pemicu | Kode | Perilaku |
|---|---|---|
| Start agent | `inventory.Scheduler.Run` (`agent/shared/inventory/scheduler.go`) | Langsung mengumpulkan satu kali saat agent start. |
| Periodik | sama | Setiap **4 jam** (`inventoryPeriod`, `agent/cmd/agent/main.go`). Waktu mulainya digeser stabil per perangkat dalam jendela **30 menit** (`inventoryStaggerWindow`, hash dari device ID), supaya armada tidak mengirim bersamaan. |
| Diminta operator | `client.SetCollectHandler` → `inventory.CollectOnce` | Saat server mengirim envelope `inventory.collect`. Hasil selalu dikirim. |

Pengiriman periodik **dilewati** bila isi laporan (tanpa timestamp) sama persis dengan laporan terakhir dan laporan
terakhir belum berumur 24 jam (`hashReport`). Permintaan on-demand tidak pernah dilewati.

Kegagalan pengumpulan hanya dicatat di log agent. Inventory bersifat best-effort dan tidak pernah menghentikan agent
dari memproses perintah.

## 2. Agent: apa yang dikumpulkan

Bentuk laporan (`inventory.Report`, `agent/shared/inventory/inventory.go`):

```json
{
  "hardware": { "cpu": {...}, "ram_total_bytes": 0, "disks": [...], "nics": [...], "model": {...} },
  "software": [ { "name": "", "version": "", "publisher": "", "product_code": "", "install_date": "" } ],
  "os": {
    "edition": "", "build_number": "", "architecture": "", "install_timestamp": 0,
    "domain": { "joined": true, "type": "active_directory", "name": "corp.example.com", "tenant": "" }
  },
  "collected_at": "2026-10-22T07:00:00Z"
}
```

Sumber data per OS:

| Bagian | Windows (`agent/windows`) | Linux (`agent/linux`) | macOS (`agent/macos`) |
|---|---|---|---|
| CPU, RAM, model | CPU dari registry `CentralProcessor`, RAM dan chassis dari CIM (PowerShell) | `/proc/cpuinfo`, `/proc/meminfo`, `/sys/class/dmi` | `sysctl`, `system_profiler` |
| Disk | volume per huruf drive | `/proc/mounts` | `mount` |
| Software | registry Uninstall (mesin + per user) | `/var/lib/dpkg/status` (distro berbasis dpkg) | bundle `.app` |
| Edition, build | `RtlGetVersion` + registry `CurrentVersion` | `/etc/os-release` | `sw_vers` |
| **Domain** | `NetGetJoinInformation`, `GetComputerNameEx(DnsDomain)`, registry `CloudDomainJoin\JoinInfo` (Entra ID) | `/etc/sssd/sssd.conf` (realmd, ipa-client), `/etc/samba/smb.conf` (`security = ads`) | `dsconfigad -show` (batas waktu 10 detik) |

### Status domain (`os.domain`)

| `type` | `joined` | Arti | `name` |
|---|---|---|---|
| `active_directory` | true | Join ke Active Directory (Windows, sssd `id_provider = ad`, winbind, atau Mac yang di-bind) | Nama domain DNS. Windows memakai nama NetBIOS bila DNS tidak ada. |
| `entra_id` | true | Windows yang join ke Microsoft Entra ID (Azure AD) | Nama tenant |
| `hybrid` | true | Windows yang join ke AD dan Entra ID sekaligus | Domain AD; tenant ada di `tenant` |
| `freeipa` | true | Linux dengan sssd `id_provider = ipa` | Domain IPA |
| `other` | true | Linux dengan domain sssd selain AD/IPA | Nama domain sssd |
| `workgroup` | false | Windows yang tidak join domain | Nama workgroup |
| `none` | false | Tidak join ke direktori mana pun | — |

Field `domain` **tidak dikirim** bila agent tidak dapat menentukannya, atau bila versinya lebih lama dari fitur ini.
Console menampilkannya sebagai "Tidak diketahui", bukan "Belum join". Perangkat Windows yang hanya *registered*
(akun kerja ditambahkan ke PC pribadi) tidak dihitung sebagai join Entra ID.

Semua sumber data domain dibaca secara lokal, jadi pengumpulan tidak pernah menunggu domain controller.

## 3. Server: menerima dan menyimpan

1. `WSHandler.handleInventory` (`server/core/transport/ws.go`) meneruskan payload mentah ke
   `inventoryHandler.acceptInventory` (`server/modules/device-management/inventory_handler.go`).
2. Bagian `hardware`, `software`, dan `os` disimpan **sebagai JSON apa adanya**. Server tidak meng-import tipe agent
   (tipe itu memakai build tag OS), sehingga field baru dari agent, seperti `os.domain`, ikut tersimpan tanpa perubahan
   server maupun migrasi.
3. Kolom cache diisi ulang untuk sorting dan filter: `hw_ram_bytes`, `hw_disk_free_pct` (volume paling penuh), dan
   `hw_cpu_model`.
4. Snapshot baru dibandingkan dengan yang lama (`inventory_diff.go`). Perubahan CPU, RAM, jumlah disk, model,
   build OS, atau **keanggotaan domain** dicatat di audit sebagai `inventory.hw_changed`, misalnya
   `changed: domain`. Perubahan domain hanya dibandingkan bila kedua snapshot memuatnya, jadi laporan pertama dari
   agent yang baru diperbarui tidak dihitung sebagai perubahan.
5. `UPSERT` ke tabel `device_inventory`, satu baris per perangkat: `hw`, `software`, `os_detail`, kolom cache,
   `collected_at` (jam agent), dan `updated_at` (jam server).

## 4. Route HTTP

| Method & path | Role minimum | Fungsi |
|---|---|---|
| `GET /api/devices/{id}/inventory` | viewer | Snapshot terakhir: `hw`, `software`, `os`, `collected_at`, `ram_bytes`, `disk_free_pct`, `cpu_model`. Mengembalikan 404 bila belum pernah ada laporan. |
| `POST /api/devices/{id}/inventory/collect` | technician | Mengirim `inventory.collect` ke agent yang online (`200 {"status":"sent"}`). Untuk perangkat offline mengembalikan `202 {"status":"queued"}`, artinya data datang dari laporan periodik berikutnya. Mengembalikan `409` bila koneksi sibuk. Tercatat di audit sebagai `inventory.collect`. |

Setelah menekan **Collect**, console memeriksa `GET .../inventory` setiap 2 detik sampai `collected_at` berubah,
paling lama sekitar 60 detik (`COLLECT_POLL_MS`, `COLLECT_TIMEOUT_TICKS` di `DeviceDetailModal.tsx`). Snapshot yang
berumur lebih dari 1 jam ditandai usang.

## 5. Siapa yang memakai `device_inventory`

| Modul | Yang dibaca |
|---|---|
| Console → Devices → detail perangkat | Seluruh snapshot: tab Hardware (termasuk kartu **Keanggotaan Domain**), Software, Network |
| Dashboard (`server/modules/dashboard`) | Kolom cache untuk ringkasan dan peringatan disk |
| Alerting (`server/modules/alerting/evaluator.go`) | `hw_disk_free_pct` untuk aturan disk hampir penuh |
| Reports (`server/modules/reports`) | Kolom cache untuk laporan perangkat |
| Asset & License (`server/modules/assetlicense`) | `software` untuk mencocokkan lisensi |
| Agent Updates (`server/modules/agentupdate`) | `os_detail.architecture` untuk memilih biner rilis yang tepat |
