# Deployment dengan Docker

`Dockerfile.server` di root repository membangun biner Go dengan Web console
ter-embed, lalu menjalankannya di atas `gcr.io/distroless/static-debian12:nonroot`.

CI sudah membangun dan menjalankan image ini pada setiap push, jadi image yang
tidak bisa dibangun akan tertangkap sebelum Anda menyentuhnya.

---

## 1. Build image

```bash
docker build -f Dockerfile.server -t endpoint-mgmt-server:local .
```

Build-nya sendiri sudah menjalankan `npm ci && npm run build` di dalam image
sebelum `go build`, jadi Anda **tidak** perlu membangun console di host lebih
dulu. Urutannya penting dan sudah ditulis benar di Dockerfile: `server/` harus
sudah ada sebelum build frontend, karena Vite menulis keluarannya ke
`server/cmd/server/dist` — direktori yang menangkap `//go:embed`.

---

## 2. Jalankan

```bash
docker run -d \
  --name endpoint-mgmt \
  --restart unless-stopped \
  -p 8443:8443 \
  -v endpoint-mgmt-data:/data \
  -e JWT_SECRET="$(openssl rand -hex 32)" \
  -e ADMIN_PASSWORD="ganti-dengan-password-kuat" \
  endpoint-mgmt-server:local
```

Lalu:

```bash
curl -s http://127.0.0.1:8443/healthz
# {"status":"ok","agents_online":0}
```

---

## 3. Dua hal yang paling sering membuat container gagal

### `JWT_SECRET` wajib, dan tidak boleh di-bake

`config.Load()` keluar dengan `JWT_SECRET must be set` kalau nilainya kosong
(`server/core/config/config.go:86`). Tidak ada nilai bawaan yang bisa aman —
secrets yang sudah ada di image bisa dibaca siapa pun yang menarik image itu.

Jangan pernah `ENV JWT_SECRET=...` di dalam Dockerfile. Nilai yang di-bake
masuk ke setiap layer dan bisa dibaca siapa pun yang menarik image itu,
termasuk lewat registry publik kalau image pernah terpush. CI sudah melakukan
yang benar: build image tanpa rahasia, suntik lewat `-e` saat runtime.

Kalau Anda sudah punya orchestrator yang menangani secret, pakai `.env` file
daripada menuliskannya di shell history:

```bash
docker run -d \
  --env-file ./endpoint-mgmt.env \
  -v endpoint-mgmt-data:/data \
  -p 8443:8443 \
  endpoint-mgmt-server:local
```

### Ownership volume

Image berjalan sebagai uid `65532` (nonroot). Direktori `/data` di dalam image
sudah di-`chown` ke uid itu, jadi volume kosong yang **dibuat Docker** langsung
bisa ditulis.

Tapi bind mount membawa ownership dari host:

```bash
# Salah: direktori host milik root, container tidak bisa menulis
-v ./data:/data

# Benar: beri kepemilikan dulu
sudo chown -R 65532:65532 ./data
-v ./data:/data
```

Gejalanya khas: container langsung exit dengan `unable to open database file`,
dan `docker logs` tidak menunjukkan apa-apa yang berguna.

---

## 4. TLS

Container melayani HTTP plaintext di `8443`. Untuk produksi, taruh reverse proxy
di depannya — [Caddy](https://caddyserver.com) paling ringkas karena otomatis
mengelola sertifikat:

```yaml
services:
  endpoint-mgmt:
    image: endpoint-mgmt-server:local
    restart: unless-stopped
    environment:
      # The console is same-origin behind the proxy. Only list other origins here.
      ALLOWED_ORIGIN_DOMAINS: ""
      HTTP_ADDR: 0.0.0.0:8443
      DB_PATH: /data/endpoint-mgmt.db
    volumes:
      - endpoint-mgmt-data:/data
    # Not published to the host; only the proxy reaches it.
    expose:
      - "8443"

  caddy:
    image: caddy:2-alpine
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data

volumes:
  endpoint-mgmt-data:
  caddy-data:
```

```caddyfile
mgmt.perusahaan.com {
    reverse_proxy endpoint-mgmt:8443
}
```

`ALLOWED_ORIGIN_DOMAINS` biarkan kosong kalau console diakses dari host yang
sama dengan API — itu defaultnya, dan diisi dengan benar hanya kalau console
disajikan dari domain berbeda.

---

## 5. compose

```bash
docker compose up -d
docker compose logs -f endpoint-mgmt
```

---

## 6. Batasan Docker di sini

| Kebutuhan | Kenapa tidak |
|---|---|
| Sertifikat dan TLS termination | Bisa, tapi selalu butuh reverse proxy di depan. [server-linux.md](server-linux.md) lebih langsung untuk satu host. |
| Agen di host yang sama | Container tidak punya akses ke WUA, nftables, atau package manager host. |

Agen **tidak pernah** berjalan di container yang sama dengan server. Ia butuh
akses ke Windows Update, nftables, dan package manager mesin yang dikelola — dan
container memblokir ketiganya. Lihat [agent-windows.md](agent-windows.md) atau
[agent-linux.md](agent-linux.md).

---

## 7. Image variables

| Variable | Default di image | Catatan |
|---|---|---|
| `HTTP_ADDR` | `0.0.0.0:8443` | Bind ke `0.0.0.0` supaya reachable dari luar container. |
| `DB_PATH` | `/data/endpoint-mgmt.db` | Harus di bawah `/data` supaya ikut ter-volume. |
| `LOG_LEVEL` | `info` | |
| `JWT_SECRET` | — | **Wajib disuntik.** Tidak ada default. |
| `ADMIN_PASSWORD` | — | Password admin pertama. Dipakai sekali. |
| `ENROLLMENT_TTL` | `30m` | Masa berlaku enrollment token. |
| `BACKUP_INTERVAL` / `BACKUP_RETAIN` | `1h` / `24` | Snapshot `VACUUM INTO` ke `/data/backups`. |

Daftar lengkap ada di `server/core/config/config.go` — `config.Load()` hanya
membaca `os.LookupEnv`, jadi variabel yang tidak ada di tabel itu tidak punya
efek apa pun.
