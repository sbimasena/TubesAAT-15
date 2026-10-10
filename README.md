# Sistem Koordinasi Bencana IF4031 — Milestone 1

Purwarupa koordinasi bencana BNPB yang menggabungkan gempa/peringatan tsunami BMKG dan laporan gunung api PVMBG menjadi `HazardEvent` kanonis.

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
| P3: autentikasi downstream | `python3 scripts/check-member-b-auth.py --natural-expiry` | Login tiga peran, scope/proyeksi, penolakan field mentah, refresh/replay, token lama dan expiry alami. Menunggu TTL access yang nyata. |
| P4: deployment mandiri dan storage | `python3 scripts/check-p4.py` | Rebuild PVMBG saja, JSONB v1/v2 tanpa migrasi tambahan, restart Aggregator/DB, isolasi storage, stop/start Auth/API dan trace. Schema PVMBG awal dipulihkan; restart Auth menghapus sesi lama. |
| P5: fanout dan recovery | `python3 scripts/check-stage-3.py` | Notifikasi offline sementara dashboard tetap menerima; backlog, replay ID sama sebelum/sesudah restart consumer, lalu broker outage/reconnect. Layanan dipulihkan. |
| P5: subscriber baru | `python3 scripts/check-p5-third.py` | Build/start subscriber ketiga, cocokkan event baru di tiga subscriber, periksa producer tidak berubah, lalu bersihkan subscriber/queue sementara. |

Urutan praktis untuk memeriksa semua problem:

```sh
python3 scripts/check-deployment.py
python3 scripts/check-ingestion.py
python3 scripts/check-cross-credentials.py
python3 scripts/check-member-b-auth.py --natural-expiry
python3 scripts/check-p4.py
python3 scripts/check-stage-3.py
python3 scripts/check-p5-third.py
python3 scripts/check-client-p2.py
```

Skenario tambahan:

| Jalankan | Tujuan dan efek |
|---|---|
| `python3 scripts/check-stage-2.py` | Persistence backlog broker. Skrip mematikan kedua consumer, menunggu backlog, menghentikan/menjalankan broker, lalu menghidupkan hanya consumer yang sebelumnya berjalan. |
| `python3 scripts/check-member-b-resilience.py` | Kontrak respons publik, filter kosong, galat JSON, refresh, trace dan redaksi log. |
| `python3 scripts/check-member-b-resilience.py --exercise-outages --burst-connections 64` | Tambahkan outage PVMBG/DB/Auth dan burst singkat; dependency/outage dipulihkan. Burst ini berbeda dari pengukuran P2 selama 60 detik. |
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
| `DATABASE_URL` | `postgres://aggregator:dev-postgres-password@canonical-store:5432/hazards?sslmode=disable` | Koneksi Aggregator; harus cocok dengan akun PostgreSQL. |
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
