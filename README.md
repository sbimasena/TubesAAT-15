# Sistem Koordinasi Bencana IF4031

Purwarupa koordinasi bencana BNPB yang menggabungkan gempa/peringatan tsunami BMKG dan laporan gunung api PVMBG menjadi `HazardEvent` kanonis.

## Milestone 2: Kubernetes

Prasyarat: Docker, `kubectl`, [`k3d`](https://k3d.io) (cluster dev), `make`, dan bash (di Windows: Git for Windows; `make` dapat dijalankan dari PowerShell). Buat `.env` dari `.env.example` seperti di bagian 1.

```sh
make dev-up    # cluster k3d lokal: 1 server + 2 agent (3 node, k3s v1.33.5)
make deploy    # perintah tunggal: Secret dari .env, build + import image (tag git SHA), apply, Tester Agent, tunggu rollout
make status    # node, pod per node beserta label role
make help      # semua target
```

Client API tersedia di `http://127.0.0.1:8080` (k3d) atau `http://<IP node>:30080` (NodePort, cluster demo). Untuk cluster demo: `make deploy OVERLAY=cluster REGISTRY=docker.io/<user>` dengan kubeconfig cluster tersebut sebagai context aktif; image di-push sebagai `linux/amd64` dengan tag git SHA. `deploy.sh` idempoten, menolak push bila `services/` belum di-commit, dan menunggu semua rollout. Manifest ada di `deploy/k8s` (Kustomize `base` + overlay `dev`/`cluster`), skrip di `scripts/k8s`, dan daftar aksi bersama di `scripts/m2/actions.json`.

### Deklarasi topologi

| Node | Host fisik | VM | IP |
|---|---|---|---|
| `node-a` (control plane + worker) | PC Windows (AMD, 32 GB) | VirtualBox, Ubuntu 24.04, 8 GB / 4 vCPU | `192.168.18.201` |
| `node-b` (worker) | MacBook Intel (16 GB) | VirtualBox, Ubuntu 24.04, 6 GB / 2 vCPU | `192.168.18.202` |
| `node-c` (worker) | PC Windows (VM kedua) | VirtualBox, Ubuntu 24.04, 8 GB / 4 vCPU | `192.168.18.203` |

- k3s, satu router (bridged), IP statis. Node tersebar di dua host fisik. Semua node x86_64, karena image Tester Agent dan Sensor Pantai hanya amd64.
- **`node-a` bukan target `wipe-node` atau node mati.** Consumer (jurnal di volume local-path) dan stack observabilitas dipaku ke `node-a`. Gangguan node diarahkan ke `node-b`/`node-c`.
- Data store berada di disk node tempat pod berjalan (`local-path`).

### Komponen M2

| Komponen | Bentuk di cluster | Ketahanan |
|---|---|---|
| Aggregator (`role=aggregator`) | Deployment 3 replika, Lease `aggregator-leader` | Hanya leader yang polling, reconcile, dan publish outbox (epoch fencing). Semua replika melayani baca dan memproses field report. Lihat bagian P2 di bawah |
| Canonical Store (`role=store`) | StatefulSet `store-0..2`, satu per node | 24 partisi, RF=2, peta partisi di ConfigMap (CAS). Re-replikasi setelah unit hilang 60 s (`REREPLICATE_AFTER_SECONDS`), amnesia, rebalancing. |
| RabbitMQ (`role=broker`) | StatefulSet `rabbitmq-0..2`, satu per node | Cluster 3 node (`classic_config`), quorum queue `notification`, `dashboard`, `field-reports`, `alerts.critical`. Tetap melayani dengan 2 dari 3 member; member yang kembali dengan disk kosong bergabung sendiri |
| Consumer (`role=consumer`) | Deployment 1 replika di `node-a` | Dedup per `MessageId` (snapshot outbox) sebelum efek samping |
| Observabilitas | Namespace `observability` | Prometheus, Grafana (dashboard "Overview" diprovision dari repo), Jaeger |

Hanya pod `role=aggregator` yang bisa konek ke store (NetworkPolicy `store-ingress`, bukti `make p1-netpol`).

### Kebijakan skema PVMBG (P3)

Setiap laporan PVMBG divalidasi sendiri-sendiri (`services/aggregator/internal/source/pvmbg_batch.go`). Laporan yang gagal tidak menghentikan laporan lain dan tidak menahan cursor: pesannya disimpan utuh beserta alasan, sumber, dan waktu (`GET /internal/audit/unprocessed`), lalu cursor maju.

| Perubahan dari mock (`POST /admin/schema-version`) | Keputusan | Alasan DLQ |
|---|---|---|
| Field baru yang tidak dikenal (versi 2, `confidence_level`) | **Diterima otomatis**, field diabaikan mapper | — |
| Urutan field berubah, whitespace | Diterima otomatis | — |
| Rename field wajib (`rename_field`, `alert_level` → `status`) | Ditolak, di-park | `schema_incompatible` |
| Tipe field berubah (`change_type`, `reported_at` jadi unix seconds) | Ditolak, di-park | `schema_incompatible` |
| Field wajib hilang (`drop_required`; wajib: `report_id`, `volcano_id`, `alert_level`, `reported_at`) | Ditolak, di-park | `schema_incompatible` |
| Body terpotong (`broken_json`) | Ditolak, di-park | `malformed_json` |
| Laporan >1 MiB (`oversize_json`) | Ditolak, di-park | `too_large` |
| Nesting >10 000 (`deep_json`) | Ditolak, di-park | `too_deep` |
| UTF-8 tidak valid (`invalid_utf8`) | Ditolak, di-park | `invalid_utf8` |

Kesalahan sementara (`timeout`, `upstream_5xx`, `store_unavailable`, `internal_panic`) dicoba ulang dengan backoff eksponensial (basis 2 s, maksimal 5 percobaan), lalu di-park. Setelah mapper diperbaiki, pesan yang di-park diproses ulang lewat `POST /internal/audit/unprocessed/replay` (Client API: hanya scope Internal Ops), berurutan per `source_ref_id` dengan penjaga versi sehingga tidak menimbulkan duplikat. Alasan: data bencana yang salah tafsir lebih berbahaya daripada data yang terlambat, jadi hanya perubahan yang tidak bisa mengubah makna laporan yang diterima otomatis.

**Compose setelah M2:** queue broker sekarang bertipe quorum. Volume broker Compose dari M1 masih berisi queue classic, dan consumer akan gagal dengan `PRECONDITION_FAILED`. Hapus sekali dengan `docker compose down -v`.

### Variabel konfigurasi M2

Tambahan dari tabel [Variabel konfigurasi](#variabel-konfigurasi). `deploy.sh` membaca kredensial dan token dari `.env` lalu menjadikannya Secret; variabel non-rahasia diset di manifest.

| Variabel | Kegunaan |
|---|---|
| `AUDIT_TOKEN` | Bearer token Audit API `/internal/audit/*` (Secret `audit-token`) |
| `PANTAI_AUTH_TOKEN` | Token yang diminta Sensor Pantai dari kliennya (Secret `pantai-token`) |
| `FIELD_REPORT_PUBLISH_TIMEOUT_MS` | Compose saja (di cluster memakai default 5000): batas tunggu publisher confirm `POST /field-reports`; lewat batas ini klien menerima 503, bukan 202 |
| `REREPLICATE_AFTER_SECONDS` | Manifest Aggregator (default 60): lama unit store tak terlihat sebelum partisinya dipindah |
| `OVERLAY`, `REGISTRY` | Hanya untuk `make deploy`: `dev` (k3d import) atau `cluster` (push ke `REGISTRY`) |

### Memicu skenario

Setiap aksi tersedia sebagai target `make` dan tercatat di `scripts/m2/actions.json`. Daftar lengkap: `make help` dan `make tester-list`.

| Problem | Target | Isi |
|---|---|---|
| Umum | `make status`, `make system-status`, `make obs-open`, `make open`, `make prepull` | Pod per node, status Aggregator (storage, sumber, leader, DLQ), Grafana + Jaeger, RabbitMQ UI. `prepull` menarik semua image workload di setiap node sebelum demo agar pod yang dijadwal ulang tidak menunggu Docker Hub; pastikan ruang disk node cukup. |
| Control Console | `make console` | Halaman lokal `http://127.0.0.1:8090` (hanya loopback, memakai kubeconfig sendiri): node dan pod per node, status Aggregator (leader, storage, sumber, breaker, satelit), tombol untuk setiap aksi di `scripts/m2/actions.json` dengan output live dan transkrip di `artifacts/m2/runs/`, event terbaru, dan klien per peran (Media, Tim Lapangan, Internal Ops) dengan `time_total` serta `correlation_id`. Aksi yang merusak meminta konfirmasi. |
| Postman | `docs/api/postman/` | Collection `bnpb-m2` (Client API per peran, field report, DLQ, Audit API, status) dan environment `cluster`/`local`. Isi password peran dan `audit_token` di environment (jangan di-commit); token login diambil otomatis. `auth_url` cluster memakai `kubectl -n sistem-informasi-kebencanaan-bnpb port-forward svc/auth 18084:8084`. |
| Tester Agent | `make tester S=<skenario>` | `kill-store`, `kill-leader`, `partition`, `clock-skew`, `schema`, `wipe-node` (profil practice) |
| P1 | `make p1-checksum`, `p1-distribution`, `p1-wipe-node`, `p1-amnesia S=store-N`, `p1-load FILE=...`, `p1-surge`, `p1-audit`, `p1-netpol`, `p1-dashboard` | Bandingkan checksum sebelum dan sesudah gangguan. Setelah pemulihan, total baris di ketiga unit semestinya dua kali jumlah event unik. Dashboard tersedia di `http://127.0.0.1:18083/dashboard` setelah `make p1-dashboard`. |
| P2 | `make p2-status`, `p2-kill-leader`, `p2-failover`, `p2-freeze`, `p2-load`, `p2-throughput`, `p2-slo` | Lihat bagian P2 di bawah. `p2-kill-leader` selalu menghapus pemegang Lease; `p2-failover` memakai Tester `kill-leader`. `p2-freeze` dijalankan di host VirtualBox: VM leader dijeda >15 s lalu dilanjutkan; sesudahnya sinkronkan ulang jam VM (`sudo systemctl restart systemd-timesyncd`). `p2-load` butuh `TARGET_URL` dan k6 di luar node cluster; tambahkan `ROLLOUT_IMAGE` untuk ganti versi di tengah beban. |
| P3 | `make p3-check` | Stack Compose terisolasi: DLQ byte-identik, replay/idempotensi, otorisasi Internal Ops, dan laporan 202 yang bertahan setelah penerima SIGKILL. Hasil di `artifacts/m2/runs/`; bukan bukti failover node Kubernetes. |
| P4 | `make p4-sample`, `p4-check`, `p4-export`, `p4-compare` | Sampling, pemulihan, dan perbandingan dengan referensi |
| P5 | `make p5-sample`, `p5-resilience`, `p5-ingestion` | Sampel HTTP Sensor Pantai resmi, uji gangguan adapter (breaker, retry budget, bulkhead, validasi kontrak), dan ingestion coastal pada stack terisolasi. Breaker di cluster: `kubectl -n sistem-informasi-kebencanaan-bnpb scale deploy/sensor-pantai --replicas=0`, amati `coastal.breaker` di `make system-status` dan `sources.SENSOR_PANTAI` di `GET /hazards`, lalu kembalikan `--replicas=1`. |
| Broker | `make broker-status`, `make broker-recover S=rabbitmq-N` | Anggota cluster dan replika setiap queue; kosongkan disk member yang crash-loop agar bergabung ulang |
| Pemulihan | `make reset` | Kembalikan manifest dan mode mock PVMBG ke normal |

Jika skenario latihan `wipe-node` mengulang shutdown setelah node menyala kembali, isolasi jaringan VM yang terdampak. Dari node yang masih sehat, periksa `configmap/tester-run` di namespace `tester-agent`. Cadangkan dan hapus ConfigMap itu hanya setelah memastikan isinya adalah run `wipe-node` yang macet dan tidak ada skenario lain berjalan. Setelah itu sambungkan kembali jaringan VM. Jangan jalankan skenario destruktif di luar cluster latihan.

## 1. Jalankan sistem

Prasyarat: Docker Engine dengan Compose v2 aktif dan Python 3. Pada macOS, jalankan Docker Desktop. Semua perintah berikut dijalankan dari root repository; Go di host tidak diperlukan untuk menjalankan stack.

Dari clone baru, buat konfigurasi lokal sekali:

```sh
python3 scripts/setup-env.py
```

Skrip menyalin konfigurasi `.env.example` ke `.env`, membuat secret/password acak, dan menyelaraskan URL database/broker dengan password tersebut. File dibuat dengan izin `0600`; file `.env` yang sudah ada dipertahankan. Jika sebelumnya sudah menyalin template dengan secret kosong, isi lima nilai wajib pada tabel konfigurasi sebelum lanjut. `.env` tidak masuk Git.

Build dan jalankan seluruh sistem dengan satu perintah:

```sh
docker compose up --build -d --wait
```

Compose membangun tujuh aplikasi Go dan menjalankan PostgreSQL/RabbitMQ, menunggu dependency sehat, serta menyiapkan migrasi dan topology broker. Sesudah perintah selesai, langsung jalankan checker pada langkah 2. Checker menunggu ingestion yang dibutuhkan; tidak perlu mengaktifkan overlay operator.

| Akses dari host | Alamat default |
|---|---|
| Client API | `http://localhost:8080` |
| Auth | `http://127.0.0.1:8084` |
| Mock BMKG / PVMBG | `http://localhost:8081` / `http://localhost:8082` |
| RabbitMQ management | `http://127.0.0.1:15672` — akun dari `.env` |

Aggregator dan PostgreSQL tidak membuka port host. Checker membaca API Aggregator dari dalam container melalui `docker compose exec`, sehingga deployment yang diperiksa sama dengan deployment biasa.

## 2. Jalankan skenario satu per satu

Setiap checker memakai `.env` dan project Compose yang sama dengan perintah startup di atas. Hasil terbaru otomatis disimpan di `artifacts/checks/<nama-checker>/`; skrip mencetak lokasi hasil dan progresnya. Exit code `0` berarti lulus, selain itu berarti gagal. Berhenti jika satu skenario gagal dan baca hasil/log sebelum melanjutkan. Volume tidak dihapus oleh checker pada stack ini.

| Problem | Jalankan | Skenario yang dipicu |
|---|---|---|
| Deployment | `python3 scripts/check-deployment.py` | Sembilan layanan sehat, batas jaringan/secret, port Aggregator/DB tertutup, tiga identitas dan trace HTTP. |
| P1: ingestion dan evolusi skema | `python3 scripts/check-ingestion.py` | Pemetaan PVMBG v1/v2 dan delivery ke dua consumer; outage PVMBG, freshness, data terakhir, independensi BMKG, recovery tanpa restart. PVMBG dikembalikan ke v1/outage=false. |
| P2: lambat dan outage di bawah beban | `python3 scripts/check-client-p2.py` | Dua run k6, masing-masing 50 VU selama 60 detik: PVMBG delay 3000 ms lalu outage. Memeriksa latency, error, refresh, socket, data terakhir, dan recovery. Delay/schema/outage awal dipulihkan. **Perlu k6 dan lsof di host**, serta access TTL 60 detik. |
| P3: kredensial upstream | `python3 scripts/check-cross-credentials.py` | Kredensial sumber sendiri harus diterima; kredensial silang dan request tanpa kredensial harus ditolak. Tidak mengubah layanan. |
| P3: autentikasi downstream | `python3 scripts/check-downstream-auth.py --natural-expiry` | Login tiga peran, scope/proyeksi, penolakan field mentah, refresh/replay, token lama dan expiry alami. Menunggu TTL access yang nyata. |
| P4: deployment mandiri dan storage | `python3 scripts/check-p4.py` | Rebuild PVMBG saja, JSONB v1/v2 tanpa migrasi tambahan, restart Aggregator/DB, isolasi storage, stop/start Auth/API dan trace. Schema PVMBG awal dipulihkan; restart Auth menghapus sesi lama. |
| P5: fanout dan recovery | `python3 scripts/check-stage-3.py` | Notifikasi offline sementara dashboard tetap menerima; backlog, replay ID sama sebelum/sesudah restart consumer, lalu broker outage/reconnect. Layanan dipulihkan. |
| P5: subscriber baru | `python3 scripts/check-p5-third.py` | Build/start subscriber ketiga, cocokkan event baru di tiga subscriber, periksa producer tidak berubah, lalu bersihkan subscriber/queue sementara. |

Urutan praktis untuk memeriksa semua problem:

```sh
python3 scripts/check-deployment.py
python3 scripts/check-ingestion.py
python3 scripts/check-cross-credentials.py
python3 scripts/check-downstream-auth.py --natural-expiry
python3 scripts/check-p4.py
python3 scripts/check-stage-3.py
python3 scripts/check-p5-third.py
python3 scripts/check-client-p2.py
```

Skenario tambahan:

| Jalankan | Tujuan dan efek |
|---|---|
| `python3 scripts/check-stage-2.py` | Persistence backlog broker. Skrip mematikan kedua consumer, menunggu backlog, menghentikan/menjalankan broker, lalu menghidupkan hanya consumer yang sebelumnya berjalan. |
| `python3 scripts/check-downstream-resilience.py` | Kontrak respons publik, filter kosong, galat JSON, refresh, trace dan redaksi log. |
| `python3 scripts/check-downstream-resilience.py --exercise-outages --burst-connections 64` | Tambahkan outage PVMBG/DB/Auth dan burst singkat; dependency/outage dipulihkan. Burst ini berbeda dari pengukuran P2 selama 60 detik. |
| `python3 scripts/check-p1-report.py` | Jalankan ingestion, lalu cocokkan payload BMKG/warning dan PVMBG dengan data kanonis, migrasi, identitas container, trace dan consumer. |
| `python3 scripts/check-clean-clone.py` | Build/start **commit HEAD** pada project/volume baru dengan kredensial sendiri, periksa deployment/fanout, lalu hapus hanya resource sementara. Perubahan belum di-commit tidak ikut. Port 28081/28082/28084/28080/35672 harus bebas. |

Detail threshold, efek dan opsi checker ada di [panduan skrip](scripts/README.md). Pengujian unit/integrasi tiap modul ada di [panduan pengujian](tests/README.md).

## 3. Hentikan, lanjutkan, atau lihat log

```sh
docker compose ps
docker compose logs --tail 100 aggregator client-api auth
docker compose stop
# Menjalankan kembali dengan data/volume yang sama:
docker compose up -d --wait
```

Jika startup gagal, lihat `docker compose ps -a` dan log layanan yang gagal. Jika port host terpakai, ubah variabel port di `.env`, lalu ulangi startup; checker otomatis mengikuti port efektif Compose. Jangan memakai `docker compose down -v` untuk demo persistence karena volume data akan dihapus.

## 4. Lihat event dan gunakan Client API

Pastikan stack sudah berjalan. API mengembalikan JSON; dashboard consumer belum memiliki halaman web. Semua contoh berikut dijalankan dari root repository dan mengikuti port/kredensial pada `.env`.

### Login dan baca event kanonis

Muat konfigurasi lokal, lalu login sebagai Tim Lapangan agar seluruh sebelas field kanonis terlihat:

```sh
source .env

TOKEN=$(curl -fsS "http://localhost:${AUTH_PORT:-8084}/login" \
  -H 'Content-Type: application/json' \
  -d "{\"client_id\":\"$FIELD_TEAM_CLIENT_ID\",\"password\":\"$FIELD_TEAM_CLIENT_PASSWORD\"}" \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')

curl -fsS "http://localhost:${CLIENT_API_PORT:-8080}/hazards?limit=5" \
  -H "Authorization: Bearer $TOKEN" \
  | python3 -m json.tool
```

Respons berisi `data` (event kanonis), `count`, dan `sources` (ketersediaan/freshness BMKG serta PVMBG). Data sudah tersimpan di Canonical Store; query tidak menunggu polling upstream. Kredensial pada contoh mengikuti format yang dihasilkan `setup-env.py`. Access token secara default berlaku 60 detik; ulangi login jika mendapat `401`, termasuk setelah restart Auth.

Filter dapat digabung pada URL:

| Parameter | Contoh | Kegunaan |
|---|---|---|
| `source` | `BMKG` atau `PVMBG` | Pilih instansi sumber. |
| `hazard_type` | `SEISMIC` atau `VOLCANIC` | Pilih jenis hazard. |
| `limit` | `5` | Batasi jumlah event, diurutkan berdasarkan waktu kejadian terbaru. |
| `since` | `2026-10-10T00:00:00Z` | Pilih kejadian sejak timestamp RFC3339. |

Contoh lima event BMKG terbaru, memakai token dari login di atas:

```sh
curl -fsS "http://localhost:${CLIENT_API_PORT:-8080}/hazards?source=BMKG&limit=5" \
  -H "Authorization: Bearer $TOKEN" \
  | python3 -m json.tool
```

Untuk mencoba peran Media, ganti variabel login menjadi `MEDIA_CLIENT_ID` dan `MEDIA_CLIENT_PASSWORD`. Media menerima tujuh field ringkasan; Tim Lapangan/Internal Ops menerima sebelas field termasuk `attributes`.

### Lihat event asli dari mock

Setelah menjalankan `source .env`, gunakan kredensial masing-masing instansi:

```sh
# Kejadian seismik BMKG
curl -fsS "http://localhost:${BMKG_PORT:-8081}/seismic-events" \
  -H "X-BMKG-Key: $BMKG_API_KEY" | python3 -m json.tool

# Peringatan tsunami BMKG
curl -fsS "http://localhost:${BMKG_PORT:-8081}/tsunami-warnings" \
  -H "X-BMKG-Key: $BMKG_API_KEY" | python3 -m json.tool

# Laporan vulkanik PVMBG
curl -fsS "http://localhost:${PVMBG_PORT:-8082}/volcanic-reports" \
  -H "Authorization: Bearer $PVMBG_TOKEN" | python3 -m json.tool
```

Endpoint sumber mengembalikan array JSON dalam format instansi masing-masing. Secara default kedua mock membuat event baru setiap 10 detik, lalu Aggregator melakukan polling setiap 3 detik.

### Ikuti log event dan consumer

```sh
docker compose logs -f --tail=20 bmkg pvmbg notification-consumer dashboard-consumer
```

Log menunjukkan pembuatan event dan pemrosesan oleh dua consumer. Tekan `Ctrl+C` untuk berhenti mengikuti log; layanan tetap berjalan.

### Gunakan Postman

1. Buat request `POST http://localhost:8084/login`. Pilih **Body → raw → JSON**, lalu isi `client_id` dan `password` dari `FIELD_TEAM_CLIENT_ID`/`FIELD_TEAM_CLIENT_PASSWORD` pada `.env`:

   ```json
   {"client_id":"field-team","password":"<password dari .env>"}
   ```

2. Salin nilai `access_token` dari respons login.
3. Buat request `GET http://localhost:8080/hazards?limit=5`. Pada **Authorization → Bearer Token**, tempel token tersebut, lalu pilih **Send**.
4. Tambahkan filter pada tab **Params** sesuai tabel di atas. Sesuaikan port URL jika `AUTH_PORT` atau `CLIENT_API_PORT` pada `.env` diubah.

Membuka `/hazards` langsung di browser tanpa header Bearer menghasilkan `401`. Untuk pemeriksaan health tanpa token, buka `http://localhost:8080/health` atau `http://127.0.0.1:8084/health`. Jangan menyertakan password/token dalam screenshot laporan atau file yang di-commit.

## Variabel konfigurasi

Nilai pada tabel adalah nilai template `.env.example`. `setup-env.py` mengganti semua kredensial contoh dengan nilai acak. Perubahan environment layanan diterapkan dengan `docker compose up -d`; setelah perubahan kode gunakan `docker compose up --build -d --wait`.

| Variabel | Nilai template | Kegunaan |
|---|---|---|
| `BMKG_PORT`, `PVMBG_PORT` | `8081`, `8082` | Port host mock sumber. |
| `CLIENT_API_PORT`, `AUTH_PORT` | `8080`, `8084` | Port host Client API/Auth; port container tetap 8080/8084. |
| `AGGREGATOR_PORT` | `8083` | Port host hanya untuk overlay operator manual; tidak diperlukan checker. Port container tetap 8083. |
| `RABBITMQ_MANAGEMENT_PORT` | `15672` | Port UI broker pada localhost. |
| `BMKG_BASE_URL`, `PVMBG_BASE_URL` | `http://bmkg:8081`, `http://pvmbg:8082` | Alamat upstream dari Aggregator pada jaringan Compose. |
| `AGGREGATOR_BASE_URL`, `AUTH_BASE_URL` | `http://aggregator:8083`, `http://auth:8084` | Dependency Client API pada jaringan Compose. |
| `BMKG_API_KEY`, `PVMBG_TOKEN` | `dev-bmkg-key`, `dev-pvmbg-token` | Kredensial mock dan Aggregator; wajib nonkosong dan berbeda. |
| `BMKG_GENERATE_INTERVAL_SECONDS`, `PVMBG_GENERATE_INTERVAL_SECONDS` | `10`, `10` | Interval pembuatan data mock untuk skenario M1. |
| `PVMBG_DELAY_MS` | `750` | Delay respons mock PVMBG. |
| `POLL_INTERVAL_SECONDS` | `3` | Interval polling Aggregator. |
| `AGGREGATOR_REQUEST_TIMEOUT_MS`, `AUTH_REQUEST_TIMEOUT_MS` | `5000`, `5000` | Timeout dependency Client API. |
| `CLIENT_API_MAX_CONCURRENT_REQUESTS` | `64` | Request hazard serentak; kapasitas penuh menghasilkan 429. |
| `ENABLE_PROVISIONAL_HAZARD_ENDPOINT` | `false` | Alias endpoint hazard lama; tetap memerlukan Auth/proyeksi field. |
| `JWT_SIGNING_SECRET` | **Wajib diisi** | Secret tanda tangan JWT, minimal 32 byte. |
| `AUTH_INTERNAL_SECRET` | **Wajib diisi** | Secret Auth/API, minimal 32 byte dan berbeda dari signing secret. |
| `ACCESS_TOKEN_TTL_SECONDS`, `REFRESH_TOKEN_TTL_SECONDS` | `60`, `3600` | Masa berlaku token; P2 mensyaratkan access TTL 60 detik. |
| `MEDIA_CLIENT_ID`, `FIELD_TEAM_CLIENT_ID`, `INTERNAL_OPS_CLIENT_ID` | `media`, `field-team`, `internal-ops` | ID tiga klien, harus berbeda. |
| `MEDIA_CLIENT_PASSWORD`, `FIELD_TEAM_CLIENT_PASSWORD`, `INTERNAL_OPS_CLIENT_PASSWORD` | **Wajib diisi** | Password tiap identitas. |
| `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD` | `hazards`, `aggregator`, `dev-postgres-password` | Database dan akun PostgreSQL. |
| `DATABASE_URL` | `postgres://aggregator:dev-postgres-password@canonical-store:5432/hazards?sslmode=disable` | Koneksi Aggregator; harus cocok dengan akun PostgreSQL. M2: daftar URL dipisah koma, satu per unit store (nama unit = label pertama host, mis. `store-1`); partisi disebar dengan RF=2. |
| `RABBITMQ_DEFAULT_USER`, `RABBITMQ_DEFAULT_PASS` | `hazard`, `dev-broker-password` | Akun RabbitMQ. |
| `BROKER_URL` | `amqp://hazard:dev-broker-password@message-broker:5672/` | Koneksi Aggregator/consumer; harus cocok dengan akun broker. |

Biarkan URL internal memakai nama layanan Compose. Port host yang diubah tidak mengubah URL internal. Jika mengisi secret manual, buat setiap nilai secara terpisah:

```sh
python3 -c 'import secrets; print(secrets.token_urlsafe(32))'
```

Jangan mengganti akun/password PostgreSQL atau RabbitMQ pada volume lama tanpa menyesuaikan akun di layanan tersebut; environment inisialisasi tidak otomatis mengubah akun yang sudah tersimpan. Encode karakter khusus password pada URL. Versi Go dipatok di Dockerfile/modul; tidak ada variabel environment untuk mengubahnya.

## Alur data dan akses

| Komponen | Tanggung jawab |
|---|---|
| BMKG / PVMBG | Mock upstream dengan kredensial terpisah dan generator data periodik. |
| Aggregator | Polling sumber mandiri, mapping/korelasi, freshness, API kanonis internal. |
| PostgreSQL | Hazard JSONB dan snapshot transactional outbox pada transaksi yang sama. |
| RabbitMQ | Exchange fanout `hazard.events`; queue durable `notification` dan `dashboard`. |
| Notification / Dashboard consumer | Simulasi notifikasi dan state/audit; jurnal persisten, ack setelah commit, deduplikasi setelah restart. |
| Auth | Identitas/scope, JWT dan rotasi refresh token. Sesi berada di memori. |
| Client API | Autentikasi, proyeksi field, filter, timeout dependency dan batas konkurensi. |

Klien login melalui `POST /login` Auth dengan `client_id`/`password`, lalu memakai `access_token` pada `GET /hazards` Client API dengan `Authorization: Bearer <token>`. `POST /refresh` merotasi token; access token generasi lama ditolak. Media menerima tujuh field ringkasan; Field Team/Internal Ops menerima sebelas field kanonis. Media yang meminta field mentah menerima 403.

Filter publik: `source`, `hazard_type`, `since`, `limit`. Respons berbentuk `data/count/sources`. `sources` memuat ketersediaan dan freshness BMKG/PVMBG. Outage upstream tetap memungkinkan pembacaan data terakhir dengan status 200; kegagalan storage menghasilkan 503. Gangguan broker menahan outbox pending sambil ingestion/query Aggregator yang sudah berjalan tetap berlanjut.

Freshness mengukur pipeline polling/commit, bukan umur tiap hazard atau keberhasilan consumer. Ambangnya adalah nilai terbesar antara 15 detik dan tiga interval polling. Aggregator sendiri mengakses jaringan storage dan `DATABASE_URL`; Auth/API/consumer tidak membaca Canonical Store langsung. Signing secret hanya masuk Auth; internal secret hanya Auth/API.

## Referensi

- Kontrak endpoint: [Auth](services/auth/README.md), [Client API](services/client-api/README.md), [Aggregator](services/aggregator/README.md).
- Infrastruktur: [PostgreSQL](infrastructure/canonical-store/README.md), [RabbitMQ](infrastructure/message-broker/README.md).

Hasil checker baru disimpan terpisah dari bukti laporan lama. Cara menampilkan hasil dan merender gambar dijelaskan di [panduan skrip](scripts/README.md).

### M2 P2: Aggregator leadership

Kubernetes runs three Aggregator replicas; a Lease holder runs ingestion/reconciliation/publishing with a fencing epoch. Hazard reads use a one-second per-instance cache; Audit reads remain uncached. Compose remains single-instance. No new runtime configuration is required: Kubernetes supplies `KUBERNETES_SERVICE_HOST`, `KUBERNETES_SERVICE_PORT`, service-account files and `POD_NAME`.

Use `make p2-status`, `make p2-failover` and `TARGET_URL=http://<aggregator-load-balancer> make p2-load`. Set `ROLLOUT_IMAGE=<registry>/bnpb-aggregator:<sha>` to roll out a distinct image and require zero failed requests. Run load tests outside the cluster against the load balancer, not a pod port-forward. Evidence is saved under `artifacts/m2/runs/`. Run `TARGET_URL=$AGGREGATOR_LB make p2-throughput` against the same dataset and load configuration to check the required 2× throughput ratio. For clock skew run `make tester S=clock-skew`. For a VM freeze, suspend the leader's worker VM for more than 15 seconds and verify stale writes are rejected after takeover.

### M2 P4: satellite processing

Set `SATELLITE_TOKEN` in the ignored `.env` to enable the P4 worker in Compose or through the Kubernetes deployment script. The elected Aggregator connects to the feed, restores its replicated checkpoint, and materializes WILDFIRE events. To inspect the wire format first, run `make p4-sample` before starting the worker. The course feed allows one connection per group.

Use `make p4-check` for offline and recovery tests. Set `TEST_DATABASE_URL` to a disposable PostgreSQL instance to enable integration tests. Evidence is saved under ignored `artifacts/m2/runs/`. Check progress via `GET /internal/v1/status` and the Audit API with `source=SATELIT`. During a 60× run capture peak offset lag, p95 ingest latency, and seconds from burst end to zero lag (target ≤60 seconds). For cluster validation, compare the final checksum and fire membership after uninterrupted and interrupted runs over identical input. To compare with the course reference, wait for the feed to catch up, run `make p4-export > actual.json`, and then run `make p4-compare ACTUAL=actual.json REFERENCE=reference.json`. The province reference data retains its [MIT license and attribution](docs/m2/data/province-centres-LICENSE.txt).
