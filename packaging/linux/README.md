# Linux Agent Packaging & GUI Installer

Folder ini menyediakan paket distribusi agen untuk sistem operasi Linux, baik dalam bentuk **Paket Debian (`.deb`)** maupun **Installer GUI Desktop (`install-gui.sh`)**.

---

## 1. Opsi Instalasi Klien Linux

### Opsi utama: instalasi satu baris dari server (disarankan)
Buka console → **Devices → Enroll device**, buat token, lalu salin perintah **Linux** yang muncul. Bentuknya:

```bash
curl -fsSL https://SERVER/install/linux.sh | sudo bash -s -- --server https://SERVER --token TOKEN [--site "Cabang"]
```

Script itu:
- mengunduh agent untuk arsitektur mesin (x86_64 atau aarch64) dari server, hanya dengan token enrollment yang masih berlaku;
- memeriksa checksum SHA-256;
- memasang `/usr/local/bin/endpoint-agent`, mendaftarkan perangkat, lalu menjalankan service systemd `endpoint-agent`.

Menjalankannya lagi akan memperbarui binary tanpa mendaftar ulang. Tambahkan `--insecure` hanya untuk server uji dengan sertifikat self-signed.

Sumber binary di server, berurutan:
1. rilis Linux aktif terbaru di menu **Agent Updates**;
2. `DATA_DIR/agents` (atau `AGENT_BUNDLE_DIR` bila di-set), untuk salinan manual;
3. folder `agents/` di sebelah binary server, yang ikut terpasang dari paket rilis `endpoint-server-linux-amd64.tar.gz` lewat `update-server.sh`.

Terminal di console untuk perangkat Linux dan macOS adalah terminal penuh (PTY), seperti PuTTY: warna, Ctrl+C, Tab completion, serta `vim`, `top` dan `htop`.

### Opsi A: GUI Installer Interaktif (`install-gui.sh`)
Cocok untuk pengguna desktop (Ubuntu Desktop, Linux Mint, Debian GNOME, Fedora, KDE):
1. Pastikan paket `zenity` (standar di GNOME/Ubuntu) atau `kdialog` (KDE) terpasang.
2. Jalankan:
   ```bash
   ./packaging/linux/gui-installer/install-gui.sh
   ```
3. Installer GUI akan muncul di layar:
   - Menampilkan jendela Selamat Datang.
   - Menanyakan URL Server Pusat dan Enrollment Token.
   - Menyalin biner ke `/usr/local/bin/endpoint-agent`.
   - Mengonfigurasi `/etc/endpoint-agent/creds.json`.
   - Mendaftarkan dan menjalankan daemon Systemd `endpoint-agent`.
   - Menambahkan menu **"Uninstall Endpoint Agent"** di daftar aplikasi desktop.

#### Uninstaller GUI:
- Buka menu aplikasi dan klik **"Uninstall Endpoint Agent"**, atau jalankan:
  ```bash
  /usr/local/bin/endpoint-agent-uninstall-gui
  ```

---

### Opsi B: Paket Debian (`.deb`) untuk Distribusi Massal (APT / Ansible)
Paket standar Debian yang dapat didistribusikan melalui repositori APT privat atau diinstal via `dpkg`:

```bash
# Instalasi paket
sudo dpkg -i endpoint-agent_1.0.0_amd64.deb

# Konfigurasi token & mulai service
sudo endpoint-agent -server https://mgmt.example.com -enroll <TOKEN> -enroll-only -creds /etc/endpoint-agent/creds.json
sudo systemctl restart endpoint-agent

# Hapus paket (service dihentikan otomatis)
sudo apt-get remove endpoint-agent

# Hapus total termasuk kredensial
sudo apt-get purge endpoint-agent
```

---

## 2. Cara Membangun Paket Linux

Jalankan skrip build di lingkungan Linux:
```bash
./packaging/linux/build.sh amd64   # untuk arsitektur x86_64
./packaging/linux/build.sh arm64   # untuk arsitektur ARM64 / Raspberry Pi
```
Skrip akan mengompilasi biner Go dan membungkusnya menjadi file `.deb`.
