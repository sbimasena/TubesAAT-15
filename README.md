# Sistem Koordinasi Bencana IF4031 — Milestone 1

Purwarupa sistem koordinasi bencana yang menggabungkan data gempa dan peringatan tsunami BMKG dengan laporan gunung api PVMBG untuk BNPB. Aggregator mengambil data dari kedua sumber, mengubahnya menjadi `HazardEvent`, lalu menyediakan API internal untuk layanan lain.

## Status dan pembagian pekerjaan

Status di bawah ini mengikuti konteks proyek dan isi cabang saat README ini diperbarui. Tanggung jawab anggota tetap mengikuti pembagian kerja tim.

### Anggota A — Data dan Aggregator

Pekerjaan yang sudah tersedia pada cabang ini:

- Mock BMKG menyediakan data gempa dan peringatan tsunami, autentikasi `X-BMKG-Key`, data historis, serta rekaman simulasi baru secara berkala.
- Mock PVMBG menyediakan laporan aktivitas gunung api, autentikasi Bearer, jeda respons yang dapat diatur, serta simulasi gangguan dan perubahan skema saat layanan berjalan.
- Aggregator melakukan polling BMKG dan PVMBG secara mandiri, memetakan data menjadi `HazardEvent`, mengorelasikan peringatan tsunami, dan menghindari duplikasi berdasarkan identitas sumber.
- Kolom PVMBG tambahan, termasuk `confidence_level`, diteruskan ke `HazardEvent.attributes`.
- API Aggregator menyediakan pemeriksaan kesehatan, daftar hazard, dan filter sumber, tipe hazard, waktu, serta jumlah hasil.
- Log dan permintaan menggunakan correlation ID.
- Status sumber membedakan fetch upstream dan siklus ingestion yang sudah tersimpan melalui `last_ingested_at`, `ingestion_error`, `stale`, serta `stale_since`. Kegagalan parsial tidak ditandai fresh.

Penanggung jawab pada konteks proyek: P1 dan bagian Aggregator/upstream dari P2.

**Alur penyimpanan dan distribusi event:** Aggregator menyimpan hazard/outbox secara atomik dan mengirim snapshot ke RabbitMQ. Consumer notifikasi dan dashboard menerima event secara mandiri, dengan manual ack serta jurnal persisten untuk deduplikasi setelah restart.

### Anggota B — API Klien, Auth, dan Resiliensi

Ruang lingkup sesuai konteks proyek:

- API Klien mengambil data melalui API Aggregator dan tidak mengakses Canonical Store secara langsung.
- Layanan Auth mengelola identitas klien Media, Tim Lapangan, dan Operasi Internal BNPB, termasuk token akses, token penyegar, TTL, dan ruang lingkup akses.
- API Klien menerapkan otorisasi di sisi server serta membatasi field yang boleh diterima tiap klien.
- Ketahanan permintaan klien hilir dan perilaku degradasi ditangani bersama Anggota A dan C.

Penanggung jawab pada konteks proyek: koordinasi P2 dan P3. **Status pada cabang ini:** direktori `services/client-api` dan `services/auth` masih berupa kerangka; endpoint, token, scope, dan alur refresh belum diimplementasikan di `main`. Fondasi HTTP Auth/API Klien dan adapter API Aggregator sedang diajukan pada [PR #2](https://github.com/sbimasena/TubesAAT-15/pull/2); login, token, dan scope masih menjadi tahap berikutnya Anggota B.

### Anggota C — Infrastruktur, Penyimpanan, dan Pesan

Ruang lingkup sesuai konteks proyek:

- Menyiapkan Docker Compose dan infrastruktur PostgreSQL sebagai Canonical Store milik Aggregator.
- Menyiapkan RabbitMQ dan kontrak distribusi `HazardEvent`.
- Menyiapkan layanan konsumen notifikasi dan dasbor/audit yang menerima peristiwa secara mandiri.
- Mendukung observabilitas, pengujian beban, dan pengujian kegagalan.

Penanggung jawab pada konteks proyek: P4 dan P5. PostgreSQL, adapter Aggregator, migrasi, outbox atomik, RabbitMQ, publisher, serta kedua consumer sudah tersedia.

### Pekerjaan integrasi tim berikutnya

1. Anggota B meninjau kontrak freshness Aggregator dan melanjutkan fondasi API Klien/Auth pada PR #2.
2. Anggota B menerapkan login, token akses/penyegar, scope, pembatasan field Media, dan respons data stale pada API Klien.
3. Anggota A dan C menjaga pemeriksaan integrasi skema, persistence/outbox, fanout, dan recovery tetap lolos setelah perubahan.
4. Seluruh tim menjalankan uji ujung ke ujung dan beban setelah Auth/API Klien siap; bukti tujuh layanan A/C belum membuktikan sembilan layanan lengkap.

## Teknologi dan batas arsitektur

- Go 1.27.1 dan `net/http`; setiap layanan Go memiliki modul sendiri.
- Docker Compose untuk menjalankan layanan secara lokal.
- PostgreSQL + JSONB dan RabbitMQ tersedia pada Compose, masing-masing memakai volume persisten.
- BMKG dan PVMBG tetap menjadi layanan mock mandiri. Aggregator melakukan polling melalui HTTP dengan kredensial terpisah.
- Hanya Aggregator yang boleh mengakses Canonical Store. Layanan lain mengambil data melalui API Aggregator.

## Menjalankan stack development

Prasyarat: Docker Engine yang aktif, Docker Compose, dan Python 3. Panduan pemeriksaan tidak membutuhkan `jq`.

Pada macOS, buka Docker Desktop dan tunggu sampai engine berjalan:

```sh
open -a Docker
docker info
```

Jika `docker info` menampilkan `Cannot connect to the Docker daemon`, tunggu proses startup Docker Desktop lalu ulangi. Compose baru bisa membangun atau menjalankan kontainer setelah daemon bisa diakses.

Kredensial BMKG dan PVMBG harus berbeda. Perintah berikut memakai `.env.example`, termasuk konfigurasi PostgreSQL/RabbitMQ, tanpa mengubah `.env` lokal:

```sh
docker compose --env-file .env.example up --build -d canonical-store message-broker bmkg pvmbg aggregator notification-consumer dashboard-consumer
```

> Jika layanan sudah berjalan, langkah start ini tidak perlu diulang. Jangan gunakan kredensial contoh di luar pengembangan lokal. Jangan commit `.env`.

Periksa kontainer dan status kesehatan layanan:

```sh
docker compose --env-file .env.example ps
curl -sS http://localhost:8083/health | python3 -m json.tool
```

Pada respons kesehatan Aggregator, tunggu sampai `storage.available` dan `sources.BMKG.available`/`sources.PVMBG.available` bernilai `true`, serta kedua sumber memiliki `stale=false`. Polling bawaan berjalan setiap 3 detik.

Metadata sumber memiliki arti berikut:

| Field | Arti |
|---|---|
| `available` | Fetch sumber terakhir berhasil; untuk BMKG, kedua endpoint harus berhasil. |
| `last_success_at` | Waktu fetch upstream terakhir yang berhasil, sebelum commit database. |
| `last_error` | Kegagalan fetch upstream terakhir; hilang setelah fetch berhasil. |
| `last_ingested_at` | Waktu siklus lengkap terakhir yang berhasil dinormalisasi dan disimpan. Polling kosong juga memeriksa transaksi storage. |
| `ingestion_error` | Kesalahan penyimpanan/normalisasi/korelasi; hilang setelah siklus lengkap berhasil. |
| `stale` | Belum ada siklus ingestion sukses, ada kegagalan, atau siklus sukses terakhir sudah melewati ambang waktu. |
| `stale_since` | Awal periode stale, termasuk waktu ambang freshness terlewati jika lebih awal. Hilang setelah recovery. |
| `stale_after_seconds` | Ambang freshness pipeline: nilai terbesar antara 15 detik dan tiga interval polling. |

Freshness ini menjelaskan kondisi pipeline polling, bukan umur setiap peristiwa atau kepastian pengiriman broker. Umur rekaman dapat dilihat dari `occurred_at`/`ingested_at`; status outbox/consumer diperiksa secara terpisah. Gangguan upstream tetap memungkinkan API mengembalikan data PostgreSQL terakhir dengan metadata stale; gangguan database menghasilkan HTTP 503.

Lihat beberapa rekaman bahaya dan status sumber tanpa `jq`:

```sh
curl -sS 'http://localhost:8083/hazards?limit=5' |
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(json.dumps({"count":d["count"],"sources":d["sources"],"events":[{k:e.get(k) for k in ("source","hazard_type","hazard_id")} for e in d["data"]]},indent=2))'
```

Filter API internal juga dapat diperiksa:

```sh
curl -sS 'http://localhost:8083/internal/v1/hazards?source=PVMBG&hazard_type=VOLCANIC&limit=3' |
  python3 -m json.tool
```

### Memeriksa autentikasi sumber

Permintaan tanpa kredensial atau dengan format kredensial yang salah seharusnya menghasilkan HTTP `401`. Kredensial yang benar seharusnya menghasilkan HTTP `200`:

```sh
# Tanpa kunci BMKG: harapkan HTTP 401
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' \
  http://localhost:8081/seismic-events

# Dengan kunci BMKG: harapkan HTTP 200
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' \
  -H 'X-BMKG-Key: dev-bmkg-key' \
  http://localhost:8081/seismic-events

# Token tanpa skema Bearer: harapkan HTTP 401
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' \
  -H 'Authorization: dev-pvmbg-token' \
  http://localhost:8082/volcanic-reports

# Token Bearer yang benar: harapkan HTTP 200
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  http://localhost:8082/volcanic-reports
```

### Memeriksa evolusi skema PVMBG

Aktifkan skema v2. Endpoint mengembalikan ID laporan baru; setelah polling, cari laporan itu di Aggregator dan pastikan `confidence_level` muncul di `attributes`:

```sh
V2_ID=$(curl -sS -X POST http://localhost:8082/admin/schema-version \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  -H 'Content-Type: application/json' \
  -d '{"version":2}' |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["report_id"])')

sleep 5

curl -sS 'http://localhost:8083/hazards?source=PVMBG&limit=1000' |
  V2_ID="$V2_ID" python3 -c 'import json,os,sys; d=json.load(sys.stdin); rid=os.environ["V2_ID"]; rows=[{"source_ref_id":e.get("source_ref_id"),"confidence_level":e.get("attributes",{}).get("confidence_level")} for e in d["data"] if e.get("source_ref_id")==rid]; print(json.dumps(rows,indent=2))'
```

Hasil yang diharapkan adalah satu laporan dengan nilai `confidence_level` numerik. Untuk mengembalikan laporan baru ke skema v1:

```sh
curl -sS -X POST http://localhost:8082/admin/schema-version \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  -H 'Content-Type: application/json' \
  -d '{"version":1}'
```

Laporan yang sudah dibuat dengan skema v2 tetap menyimpan kolom tersebut.

### Memeriksa simulasi gangguan PVMBG

Aktifkan simulasi gangguan, tunggu beberapa siklus polling, lalu lihat status sumber. PVMBG seharusnya menjadi tidak tersedia sedangkan BMKG tetap tersedia:

```sh
curl -sS -X POST http://localhost:8082/admin/outage \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true}'

sleep 5
curl -sS http://localhost:8083/health |
  python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["sources"],indent=2))'
```

Matikan simulasi gangguan, tunggu polling berikutnya, lalu periksa bahwa PVMBG kembali tersedia:

```sh
curl -sS -X POST http://localhost:8082/admin/outage \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  -H 'Content-Type: application/json' \
  -d '{"enabled":false}'

sleep 5
curl -sS http://localhost:8083/health |
  python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["sources"],indent=2))'
```

## API Aggregator

- `GET /health` — status Aggregator dan status polling kedua sumber.
- `GET /hazards` — daftar kanonis dengan bentuk `{ "data": [...], "count": n, "sources": {...} }`.
- `GET /hazards?source=BMKG&hazard_type=SEISMIC&since=<RFC3339>&limit=100` — filter hasil; batas `limit` adalah 1.000.
- `GET /internal/v1/hazards` — alias untuk daftar hazard bagi klien internal.

ID hazard dibuat deterministik dari sumber dan ID referensi sumber. PostgreSQL mempertahankan identitas tersebut setelah restart; polling identik tidak membuat record atau pesan baru. Update bermakna membuat revisi dan snapshot outbox baru untuk hazard yang sama.

## Batas implementasi saat ini

- PostgreSQL menggunakan satu instance dan volume; belum ada high availability, backup otomatis, atau cleanup record/outbox. Cursor/cache sumber tetap di memori dan dibangun ulang dari riwayat upstream saat restart Aggregator.
- Outbox dikirim setelah commit dengan at-least-once; kegagalan broker menahan row pending. Duplikasi mungkin terjadi sebelum `published_at` berhasil disimpan. Queue durable menahan backlog consumer offline; kedua consumer menggunakan volume jurnal terpisah. Management UI ada di http://127.0.0.1:15672 dengan kredensial development `.env.example`.
- Auth dan API Klien masih berupa kerangka. Bukti ownership lengkap melalui Client API menunggu pekerjaan B.
- Jalankan pemeriksaan persistence/outbox sesuai [README Aggregator](services/aggregator/README.md). Recovery broker, fanout, downtime, idempotensi consumer, serta penambahan subscriber ketiga sudah diuji.

## Memeriksa pekerjaan terbaru Anggota A setelah integrasi Anggota C

```sh
python3 scripts/check-ingestion.py --output /tmp/tubesaat-ingestion-check.json
```

Skrip memeriksa rekaman v1 tetap utuh, laporan v2 memiliki `confidence_level` yang sama dengan PVMBG, dan snapshot kanonis sampai ke jurnal kedua consumer. Setelah itu skrip mengaktifkan gangguan PVMBG, memastikan BMKG tetap fresh dan data vulkanik tersimpan tetap terbaca, lalu memeriksa recovery. ID kontainer dan waktu startup harus tetap sama. Skrip menggunakan tujuh layanan yang sudah berjalan dengan `.env.example`, tanpa `jq`.

Skrip menambah satu laporan v2 beserta data/outbox/jurnal hasilnya. Pada akhir pemeriksaan, PVMBG dikembalikan ke skema v1 dan outage=false. Volume/queue tidak dihapus. Hasil `PASS` dan rincian pemeriksaan ditulis ke berkas output. Jangan menjalankannya bersamaan dengan demo yang mengubah status sumber.

Uji Go dan uji database/broker nyata dapat dijalankan sesuai [README Aggregator](services/aggregator/README.md).

## Pemeriksaan fanout, downtime, dan idempotensi consumer

```sh
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-consumers.yml run --build --rm --no-deps notification-consumer
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-consumers.yml run --build --rm --no-deps dashboard-consumer
python3 scripts/check-stage-3.py
```

Skrip memakai konfigurasi development dan tujuh container yang sudah berjalan; melakukan stop/start notifikasi, replay ID pesan dengan restart consumer, lalu restart broker untuk memeriksa reconnect. Volume/queue dipertahankan. Lihat README [notifikasi](services/notification-consumer/README.md)/[dashboard](services/dashboard-consumer/README.md). Skrip `check-stage-2.py` menguji recovery broker dengan kedua consumer offline; untuk stack dengan consumer aktif, gunakan `check-stage-3.py` seperti perintah di atas.

## Demo independensi container, storage, dan fanout event (P4/P5)

```sh
python3 scripts/check-p4.py
python3 scripts/check-stage-3.py --output /tmp/fanout-downtime.json
python3 scripts/check-p5-third.py
```

Pengujian P4 memeriksa rebuild layanan secara mandiri, record JSONB sebelum/sesudah penambahan field tanpa migrasi, serta isolasi akses storage. Pengujian P5 memeriksa fanout, backlog saat consumer offline, dan penerimaan oleh subscriber ketiga tanpa perubahan producer. Request Client API → Aggregator dan sembilan container fungsional masih menunggu B. Lihat [panduan skrip](scripts/README.md) untuk efek restart/cleanup dan demo binding secara manual.

## Uji beban dan outage Aggregator (P2)

```sh
python3 scripts/check-p2.py
python3 scripts/check-clean-clone.py
```

Native k6/lsof diperlukan untuk P2. Tujuh layanan development harus sudah berjalan. Skrip menguji 50 VU selama 60 detik, mencatat socket TCP nyata, memperlambat PVMBG ke 3 detik, memeriksa data tersimpan/status sumber saat outage dan recovery tanpa restart Aggregator. PVMBG dipulihkan ke delay `.env.example`, schema v2, outage=false. Pengujian mengukur throughput, p50/p95/p99, error rate, dan jumlah respons 429 langsung pada Aggregator. Detail parameter dan threshold ada di [panduan pengujian](tests/README.md).

Skrip clone memakai project/volume terpisah, port 18081/18082/18083/25672, serta menghapus hanya resource uji. Perbaikan startup broker sudah tersedia pada `main`; gunakan pemeriksaan HEAD tanpa opsi patch. Auth/Client API tetap perlu implementasi dan verifikasi B.
