# Aggregator

Aggregator mem-poll BMKG dan PVMBG secara mandiri setiap `POLL_INTERVAL_SECONDS` (bawaan 3 detik), memetakan rekaman menjadi `HazardEvent`, mengorelasikan peringatan tsunami, dan menyediakan API kanonis internal.

## Akses internal

Compose utama tidak mempublikasikan port Aggregator. Client API memanggil `http://aggregator:8083` melalui jaringan Docker. Operator/checker dapat membaca API dari dalam container tanpa overlay atau port host:

```sh
docker compose exec -T aggregator /service --inspect '/internal/v1/hazards?limit=5' operator-inspect
```

Mode `--inspect` menerima path health/hazards dan correlation ID, melakukan GET pada loopback container, dan gagal jika respons bukan 200 atau correlation ID berubah. Endpoint internal mengembalikan data penuh tanpa token; akses klien tetap melalui Client API. Overlay `tests/compose-operator.yml` tersedia untuk inspeksi host manual oleh operator tepercaya, tetapi tidak dipakai checker.

## API dan freshness sumber

- `GET /health` memeriksa ketersediaan storage dan status polling sumber. Gangguan database menghasilkan HTTP 503; gangguan upstream saja tetap memungkinkan data tersimpan dibaca.
- `GET /hazards` mengembalikan `{ "data": [...], "count": n, "sources": {...} }`.
- `GET /hazards?source=BMKG&hazard_type=SEISMIC&since=<RFC3339>&limit=100` menyaring hasil. Nilai `limit` adalah 1–1.000.
- `GET /internal/v1/hazards` merupakan alias daftar bagi klien internal.

Semua endpoint tersebut mengembalikan metadata `sources.BMKG` dan `sources.PVMBG`:

| Field | Makna |
|---|---|
| `available` | Fetch sumber terakhir berhasil; BMKG memerlukan kedua endpoint berhasil. |
| `last_success_at` | Waktu fetch upstream sukses terakhir, sebelum commit database. |
| `last_error` | Kesalahan fetch terakhir; dihapus setelah fetch berhasil. |
| `last_ingested_at` | Waktu siklus penuh terakhir yang berhasil dinormalisasi dan disimpan. |
| `ingestion_error` | Kesalahan penyimpanan atau normalisasi/korelasi yang belum pulih. |
| `stale` | Belum ada siklus ingestion sukses, kegagalan terdeteksi, atau ambang waktu terlewati. |
| `stale_since` | Waktu awal periode stale; hilang setelah siklus lengkap pulih. |
| `stale_after_seconds` | Nilai terbesar antara 15 detik dan tiga interval polling. |

Status awal adalah stale. Fetch yang berhasil belum cukup untuk menghapus status stale; commit harus berhasil dan tidak boleh ada rekaman yang gagal dipetakan atau peringatan tsunami yang belum terkorelasi. Polling tanpa rekaman baru tetap memeriksa transaksi storage dan dapat memperbarui `last_ingested_at`. Transaksi gagal mempertahankan timestamp ingestion terakhir. Fetch parsial BMKG tetap boleh menyimpan data yang tersedia, tetapi sumber tetap stale.

Ambang waktu juga mendeteksi poller yang berhenti menyelesaikan siklus, walaupun kegagalan HTTP belum dilaporkan. Freshness ini menunjukkan kondisi pipeline polling, bukan umur tiap hazard atau bukti delivery broker. Klien tetap perlu menilai `occurred_at`/`ingested_at` tiap rekaman dan memeriksa outbox/consumer untuk delivery. Snapshot status tidak berbagi pointer timestamp dengan state manager.

## Pemetaan dan evolusi skema

ID hazard dibuat deterministik dari sumber dan ID referensi sehingga replay polling idempoten. Nama/koordinat gunung api berasal dari tabel referensi statis Aggregator. Kolom tambahan PVMBG, termasuk `confidence_level`, disimpan dalam `attributes`.

Penambahan properti JSON valid didukung selama kolom identitas dan pemetaan mempertahankan nama/tipe. Menghapus atau mengganti nama `report_id`, `volcano_id`, `alert_level`, atau `reported_at`, serta memakai `volcano_id` tanpa referensi koordinat, tidak didukung. Rekaman tersebut gagal dinormalisasi dan status ingestion menunjukkan kesalahan. Jika ada laporan PVMBG yang gagal dipetakan, cursor ditahan agar laporan baru tidak mengeluarkan kegagalan itu dari jendela retry; rekaman valid tetap disimpan secara idempoten.

## PostgreSQL dan outbox atomik

`DATABASE_URL` wajib tersedia. Aggregator memiliki database kanonis dan menjalankan migrasi tertanam `internal/store/migrations/001_canonical.sql` saat startup. Versinya dicatat dalam `schema_migrations`; restart pada volume lama tidak membuat ulang tabel. Pool dibatasi delapan koneksi, dengan deadline operasi baca/tulis tiga detik.

`hazard_events` menyimpan kolom kanonis tetap, `attributes JSONB`, dan revisi internal. `hazard_id` adalah primary key; `(source, source_ref_id)` unik. Insert atau perubahan bermakna menulis snapshot `HazardEvent` ke `hazard_outbox` dalam transaksi yang sama. Perbedaan urutan key JSONB atau timestamp pemetaan baru saja tidak membuat perubahan. Peringatan tsunami terlambat memperbarui hazard yang sama dan membuat revisi/pesan baru.

Cursor polling hanya maju setelah commit. Kegagalan penyimpanan mempertahankan cursor, termasuk retry peringatan yang sudah di-cache. Cursor/cache masih di memori untuk M1; setelah restart, riwayat upstream membangun ulang cache dan PostgreSQL mencegah duplikasi replay dengan identitas yang sama.

## Publisher outbox RabbitMQ

`BROKER_URL` wajib tersedia. Satu worker membaca hingga 50 snapshot pending berdasarkan ID pesan. Worker memeriksa exchange fanout durable `hazard.events` yang disiapkan infrastruktur, menerbitkan JSON persistent dengan `mandatory=true`, lalu menunggu positive publisher confirm tanpa return sebelum menandai `published_at`. `MessageId` adalah ID outbox; `CorrelationId` dan header `X-Correlation-ID` mempertahankan ID polling. Header lain adalah `schema_version=1` dan `hazard_revision`.

Koneksi/setup/publish dibatasi lima detik. Worker retry setiap tiga detik, reconnect setelah kegagalan publish, dan langsung mengambil batch berikutnya jika batch penuh. Pembatalan menutup I/O socket dan menunggu confirm. Gangguan broker mempertahankan row pending tanpa menghentikan ingestion atau kueri HTTP. `/health` memeriksa storage/upstream, dan tidak membuktikan delivery broker. Log publisher memuat ID, correlation ID, latency, dan ukuran batch, bukan total seluruh outbox pending.

Pesan yang diterima broker lalu gagal ditandai dalam database dapat diterbitkan ulang dengan ID/body sama. Delivery bersifat at-least-once untuk binding yang ada, volume sehat yang dipertahankan, dan recovery eventual. Mandatory routing hanya membuktikan minimal satu queue terikat. Penghapusan queue/volume dapat kehilangan pesan yang sudah ditandai. M1 mendukung satu replika Aggregator; beberapa worker memerlukan row lease. Cleanup/retensi otomatis belum tersedia. Kedua consumer melakukan ack setelah jurnal JSONL tersinkron dan deduplikasi ID setelah restart; consumer tidak mengakses PostgreSQL.

## Pemeriksaan

Dari root repository, jalankan tujuh layanan development seperti panduan root README. Uji lokal dengan race detector:

```sh
cd services/aggregator
go test -race ./...
```

Tanpa `TEST_DATABASE_URL`, uji lokal melewati integrasi database. Untuk PostgreSQL/RabbitMQ nyata:

```sh
docker compose --env-file .env -f docker-compose.yml -f tests/compose-storage.yml run --build --rm --no-deps aggregator
```

Override menggunakan builder image, schema PostgreSQL sementara, dan exchange/queue uji privat; resource uji dibersihkan setelah selesai. Rekaman kanonis development tidak dihapus. Uji mencakup deduplikasi, revisi/snapshot, JSONB v1/v2 tanpa migrasi tambahan, filter, rollback atomik, reopen database, publisher confirm/mandatory return, retry ID sama, dan pembatalan. Uji status mencakup commit gagal, polling kosong, mapping gagal, BMKG parsial, deadline freshness, serta poller BMKG saat PVMBG terhambat.

Untuk alur skema/fanout/outage melalui API nyata, tanpa restart:

```sh
python3 scripts/check-ingestion.py
```

Jalankan dari root repository. Skrip mengembalikan PVMBG ke skema v1 dan outage=false; laporan yang sudah dibuat tetap ada. Lihat [README utama](../../README.md) untuk prasyarat dan efek pemeriksaan.

Konfigurasi wajib: `BMKG_BASE_URL`, `PVMBG_BASE_URL`, `BMKG_API_KEY`, `PVMBG_TOKEN`, `DATABASE_URL`, dan `BROKER_URL`. Kredensial kedua sumber harus berbeda. Nilai contoh Compose hanya untuk pengembangan lokal.
