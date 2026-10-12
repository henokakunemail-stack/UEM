# Linux Agent Packaging & GUI Installer

Folder ini menyediakan paket distribusi agen untuk sistem operasi Linux, baik dalam bentuk **Paket Debian (`.deb`)** maupun **Installer GUI Desktop (`install-gui.sh`)**.

---

## 1. Opsi Instalasi Klien Linux

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
