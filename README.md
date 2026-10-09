# Sistem Koordinasi Bencana IF4031 — Milestone 1

Purwarupa koordinasi bencana BNPB yang menggabungkan gempa/peringatan tsunami BMKG dan laporan gunung api PVMBG menjadi `HazardEvent` kanonis.

## Komponen dan tanggung jawab

| Anggota | Komponen |
|---|---|
| A | Mock BMKG/PVMBG, polling mandiri, mapping/korelasi, API dan freshness Aggregator |
| B | Auth, identitas/scope, JWT/refresh, Client API, proyeksi field dan resiliensi request |
| C | Compose, PostgreSQL/JSONB, transactional outbox, RabbitMQ, consumer dan pengujian infrastruktur |

Tujuh layanan Go memiliki modul dan Dockerfile sendiri; PostgreSQL dan RabbitMQ menjadi dua komponen infrastruktur. Semua komunikasi bisnis antarlayanan memakai HTTP atau AMQP. Go 1.27.1 dan `net/http` dipakai konsisten.

Aggregator menyimpan hazard dan snapshot outbox dalam transaksi PostgreSQL yang sama. Worker menerbitkan snapshot ke exchange fanout `hazard.events`; queue durable `notification` dan `dashboard` memberikan subscription independen. Consumer menyimpan hasil simulasi pada jurnal persisten lalu melakukan ack, dengan deduplikasi ID pesan setelah restart.

## Menjalankan stack lengkap

Prasyarat: Docker Engine/Compose aktif dan Python 3 untuk checker. Pada macOS, jalankan Docker Desktop terlebih dahulu dan periksa `docker info`.

```sh
cp .env.example .env
```

Isi `JWT_SIGNING_SECRET`, `AUTH_INTERNAL_SECRET`, dan password ketiga klien di `.env`. Kedua secret minimal 32 byte dan berbeda. Untuk menghasilkan satu nilai lokal acak:

```sh
python3 -c 'import secrets; print(secrets.token_urlsafe(32))'
```

Jalankan kembali untuk setiap secret/password. `.env` diabaikan Git; `.env.example` sengaja tidak berisi secret Auth/password klien. Nilai PostgreSQL/RabbitMQ/upstream contoh hanya untuk development. File yang belum diisi akan gagal validasi Compose, termasuk saat memilih sebagian layanan.

```sh
docker compose up --build -d --wait
docker compose ps
curl -fsS http://localhost:8080/health
curl -fsS http://localhost:8084/health
```

Compose utama menjalankan sembilan layanan beserta healthcheck, environment, dan dependency Auth/API. `AUTH_PORT`, `CLIENT_API_PORT`, serta port mock memilih port host; port container tetap. Auth memakai port internal 8084; URL internal API adalah `http://auth:8084` dan `http://aggregator:8083`.

Healthcheck mock memeriksa proses HTTP; flag outage PVMBG tetap memungkinkan `/health` 200. Readiness ingestion diperiksa dari metadata sumber, bukan hanya status container.

## Membaca data sebagai klien

Login melalui `POST /login` Auth dengan JSON `client_id` dan `password`. Gunakan `access_token` pada `GET /hazards` Client API dengan header `Authorization: Bearer <token>`. Gunakan `POST /refresh` Auth untuk rotasi tanpa login ulang.

- Media menerima tujuh field Ringkasan; permintaan `include_raw=true` atau field mentah menghasilkan 403.
- Field Team/Internal Ops menerima sebelas field kanonis, termasuk `attributes` tambahan.
- JWT memiliki TTL awal 60 detik; refresh TTL awal 3600 detik. Token akses generasi lama ditolak setelah refresh.
- Sesi Auth berada di memori. Restart Auth memerlukan login ulang.
- Batas konkurensi awal API adalah 64; kapasitas penuh menghasilkan 429 dengan `Retry-After: 1`. Timeout dependency awal 5000 ms; tuning harus mengikuti hasil pengukuran.

Filter `source`, `hazard_type`, `since`, dan `limit` tersedia. Respons berbentuk `data/count/sources`; query storage yang berhasil tetap 200 saat upstream mati atau hasil filter kosong. Detail endpoint/galat ada di [README Auth](services/auth/README.md) dan [README Client API](services/client-api/README.md).

## Batas akses dan inspeksi operator

Compose utama tidak mempublikasikan port Aggregator atau PostgreSQL. Hanya Aggregator berbagi jaringan `storage` dengan PostgreSQL dan menerima `DATABASE_URL`. Auth/API/consumer tidak mengakses Canonical Store langsung. Signing secret hanya masuk Auth; internal secret hanya dibagikan kepada Auth/API.

Untuk demo lama yang membaca API internal dari host, operator dapat mengaktifkan overlay lokal:

```sh
docker compose --env-file .env -f docker-compose.yml -f tests/compose-operator.yml up -d aggregator
curl -fsS 'http://127.0.0.1:8083/internal/v1/hazards?limit=5'
```

Overlay hanya membuka `127.0.0.1:${AGGREGATOR_PORT}:8083`. Endpoint tersebut mengembalikan data penuh tanpa token: gunakan hanya sebagai akses operator tepercaya, jangan sebagai endpoint klien. Pengguna lain pada host yang sama tetap dapat menjangkaunya. Untuk kembali ke deployment default:

```sh
docker compose up -d aggregator
```

RabbitMQ management UI tersedia di `http://127.0.0.1:15672`; AMQP hanya pada jaringan `events`. Inspeksi SQL administratif dilakukan melalui `docker compose exec canonical-store`, bukan port DB host.

## Freshness dan kegagalan

`sources` publik selalu memuat BMKG/PVMBG dengan `available`, `last_ingested_at`, `stale`, `stale_since`, dan `stale_after_seconds`. `available` menandai fetch upstream; `last_ingested_at` menandai siklus lengkap yang sudah tersimpan. Kegagalan mapping/commit atau interval ingestion melebihi ambang membuat sumber stale. Ambangnya adalah nilai terbesar antara 15 detik dan tiga interval polling.

Freshness menunjukkan kondisi pipeline, bukan umur tiap hazard atau keberhasilan consumer. API tetap membaca data terakhir saat upstream mati; kegagalan storage menghasilkan 503. Gangguan broker menahan outbox pending tanpa menghentikan query/ingestion pada Aggregator yang sudah berjalan. Rincian ada di [README Aggregator](services/aggregator/README.md).

## Pemeriksaan deployment dan integrasi

```sh
python3 scripts/check-deployment.py --env-file .env --output /tmp/deployment-check.json
python3 scripts/check-member-b-auth.py --env-file .env --natural-expiry --output /tmp/auth-check.json
```

Checker deployment memeriksa sembilan container healthy, port Aggregator/DB tertutup, ownership konfigurasi, query kedua sumber lewat ketiga identitas, serta correlation ID/latensi pada log Client API, Auth, dan Aggregator. Gunakan `--start` untuk build/start Compose utama. `--project-name` memilih project uji; siapkan file environment dengan port host yang bebas jika stack lain sedang berjalan.

Checker autentikasi memeriksa scope, refresh/replay, token lama, dan expiry alami. Ia tidak mengubah lifecycle atau status sumber. Checker outage/resiliensi harus memakai project uji yang dipilih secara eksplisit.

Checker C yang memakai `demo_support.py` menerima `--env-file` (awal `.env`), `--project-name`, dan `--evidence-dir` untuk hasil baru, serta memakai overlay operator:

```sh
python3 scripts/check-ingestion.py --env-file .env --output /tmp/ingestion-check.json
python3 scripts/check-p4.py --env-file .env --evidence-dir /tmp/p4-check
python3 scripts/check-stage-3.py --env-file .env --output /tmp/fanout-check.json
python3 scripts/check-p5-third.py --env-file .env
```

Pemeriksaan P2 publik dan clean clone:

```sh
python3 scripts/check-client-p2.py --env-file .env --project-name project-uji --evidence-dir /tmp/client-p2
python3 scripts/check-clean-clone.py --evidence-dir /tmp/clean-clone
```

Clean clone menghasilkan kredensial sendiri; `--project-name` pada checker clone hanya memilih project lama yang diamati.

Pastikan project yang diperiksa sudah berjalan dengan overlay operator jika checker membutuhkan HTTP Aggregator dari host. Jalankan skenario satu per satu; skrip dapat mengubah schema/outage atau restart layanan. Volume utama dipertahankan. [Panduan skrip](scripts/README.md) dan [panduan pengujian](tests/README.md) menjelaskan efek masing-masing pemeriksaan.

## Urutan pemeriksaan penerimaan P1–P5

Jalankan dari root repository pada project uji tersendiri. Salin `.env` yang sudah lengkap ke `.env.acceptance.local`, lalu pilih port host yang bebas untuk `BMKG_PORT`, `PVMBG_PORT`, `AUTH_PORT`, `CLIENT_API_PORT`, `AGGREGATOR_PORT`, dan `RABBITMQ_MANAGEMENT_PORT`. Nama project memisahkan container/volume, tetapi tidak mengubah port host. P2 memerlukan k6/lsof native dan TTL akses 60 detik. Jalankan skenario satu per satu.

```sh
cp .env .env.acceptance.local
# Edit port host di .env.acceptance.local sebelum menjalankan stack uji.
export ACCEPTANCE_ENV=.env.acceptance.local
export ACCEPTANCE_PROJECT=tubesaat-acceptance
export ACCEPTANCE_RESULTS="$(mktemp -d /tmp/tubesaat-acceptance.XXXXXX)"

# Stack default: sembilan layanan, ownership storage, scope, dan trace HTTP.
docker compose --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" up --build -d --wait
python3 scripts/check-deployment.py --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" --output "$ACCEPTANCE_RESULTS/deployment.json"

# P3: silang kredensial upstream, scope, expiry alami, dan rotasi token.
python3 scripts/check-cross-credentials.py --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" --output "$ACCEPTANCE_RESULTS/p3-upstream.json"
python3 scripts/check-member-b-auth.py --env-file "$ACCEPTANCE_ENV" --natural-expiry --output "$ACCEPTANCE_RESULTS/p3-downstream.json"

# Akses operator localhost untuk checker berikutnya.
docker compose --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" -f docker-compose.yml -f tests/compose-operator.yml up -d --wait aggregator

# P1: schema v1/v2 tanpa restart, fanout, dan freshness saat outage.
python3 scripts/check-ingestion.py --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" --output "$ACCEPTANCE_RESULTS/p1-ingestion.json"

# P4: rebuild/restart mandiri, JSONB, dan isolasi storage.
python3 scripts/check-p4.py --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" --evidence-dir "$ACCEPTANCE_RESULTS/p4"

# P5: backlog/reconnect/idempotensi dan subscriber ketiga.
python3 scripts/check-stage-3.py --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" --output "$ACCEPTANCE_RESULTS/p5-consumers.json"
python3 scripts/check-p5-third.py --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" --evidence-dir "$ACCEPTANCE_RESULTS/p5-third"

# P2: API publik, 50 koneksi/60 detik per kondisi, slow/outage/recovery.
python3 scripts/check-client-p2.py --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" --evidence-dir "$ACCEPTANCE_RESULTS/p2"

# Sesudah perubahan aplikasi di-commit: clone HEAD pada project/volume baru.
python3 scripts/check-clean-clone.py --project-name "$ACCEPTANCE_PROJECT" --evidence-dir "$ACCEPTANCE_RESULTS/clean-clone"
```

Hentikan urutan jika satu perintah gagal, periksa hasilnya, lalu ulangi skenario setelah penyebabnya diperbaiki. File hasil tidak berarti lulus; periksa status `PASS` dan threshold. Clone hanya memakai commit yang dipilih; perubahan aplikasi/config yang belum di-commit tidak ikut diuji. Port clone 28081/28082/28084/28080/35672 harus bebas.

Hasil berada di direktori sementara `ACCEPTANCE_RESULTS`; tim dapat menyalinnya ke lokasi laporan sendiri. Checker upstream hanya melakukan GET dan menyimpan status tanpa kredensial/payload. Checker lain dapat menambah data, mengubah schema/outage, atau restart layanan; efek pemulihannya dijelaskan dalam [panduan skrip](scripts/README.md). Untuk menutup port operator dan menghentikan stack uji tanpa menghapus volume:

```sh
docker compose --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" up -d --no-deps --wait aggregator
docker compose --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" stop
```

## Artefak laporan Member A

[Bukti P1](docs/evidence/member-a/p1/README.md) memuat JSON/log pengujian nyata dan gambar terminal untuk Gambar 1–3. [Diagram Bab 3](docs/diagrams/README.md) menyediakan SVG, PNG, dan perintah rendering. [Panduan tambahan laporan](docs/report/member-a-additions.md) menjelaskan lokasi paragraf dan tabel; [audit Google Docs](docs/report/google-docs-audit.md) mencatat koreksi terhadap versi laporan yang diperiksa pada 9 Oktober 2026.

Pengumpulan bukti baru memakai `scripts/check-p1-report.py`; gambar terminal memakai Freeze dan `scripts/show-p1-evidence.py`. Perintah rendering membaca hasil tersimpan dan tidak menjalankan ulang pengujian. Simpan hasil run baru di direktori berbeda agar ID/timestamp pada bukti yang sudah dirujuk laporan tetap tersedia. Hasil P1 ini tidak menggantikan pengukuran P2.

## Batas hasil dan implementasi

Pengukuran lokal 6 Oktober 2026 mencatat p95 API publik 562 ms saat PVMBG lambat dan 539 ms saat outage, melampaui target <300 ms. Pemeriksaan ulang 7 Oktober pada Docker 8 CPU/sekitar 4 GB RAM lulus: setelah reuse koneksi HTTP dan penghapusan decode envelope berulang, p95 mencapai 203 ms dan 221 ms. Setiap kondisi memakai 50 koneksi selama 60 detik, 50 refresh berhasil, nol error tidak terkontrol/429, serta last-known data dan recovery yang lulus. Query BMKG memakai limit 100; generator sementara 1 detik digunakan untuk menyiapkan katalog penuh.

Baseline pada stack uji yang sama juga lulus (209/224 ms); kegagalan 6 Oktober tidak tereproduksi pada run ini. Latensi membaik sedikit, sementara jumlah query 200 selama pengukuran bertambah dari 31.037/30.096 menjadi 33.634/30.987. Angka ini berlaku pada mesin/config yang diuji; tim perlu mengulang checker pada lingkungan demo akhir dan menyimpan hasilnya sendiri.

- Checker P2 API publik memakai 50 sesi/VU selama 60 detik per kondisi lambat/outage, refresh per VU, bukti socket, serta metrik 200/429/error/latensi. Baseline langsung Aggregator tetap terpisah. Hasil pengukuran bergantung pada mesin/config yang diuji.
- Checker clone menjalankan sembilan layanan dari HEAD dengan Compose utama dan secret sementara sendiri, tanpa port operator/DB. Perubahan workspace yang belum di-commit tidak ikut checkout; SHA aplikasi selalu dicatat.
- PostgreSQL/RabbitMQ memakai satu instance dan volume, tanpa high availability atau cleanup otomatis outbox/jurnal. Cursor/cache sumber tetap di memori dan dibangun ulang dari riwayat upstream saat restart.
- Delivery bersifat at-least-once pada binding/volume yang dipertahankan. Confirm tidak membuktikan consumer selesai; mandatory hanya membuktikan sedikitnya satu route. Consumer menolak payload tidak valid tanpa requeue dan belum memiliki DLQ.
- Notifikasi berupa simulasi; dashboard consumer menyimpan audit/state tanpa UI. Subscriber ketiga untuk demo memakai queue sementara dan tidak memperoleh riwayat sebelum binding.

Jangan memakai `docker compose down -v` pada stack utama untuk demo persistence. Startup broker pada volume kosong dan reconnect consumer dijelaskan pada [README RabbitMQ](infrastructure/message-broker/README.md).
