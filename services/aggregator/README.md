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

## Adapter Sensor Pantai M2

`internal/source/pantai` menyediakan klien `GET /v1/stations` dan `GET /v1/observations`, autentikasi Bearer, correlation/instance ID, verifikasi leader sebelum request, dan pool HTTP terpisah. Timeout default dua detik membatasi header dan body. Redirect ditolak; respons non-200 dikembalikan sebagai `HTTPError` dengan status/`Retry-After`, tanpa retry tersembunyi atau body error di log. Cursor diperlakukan sebagai string buram dan tidak disimpan oleh adapter.

Validasi struktural memeriksa field wajib beserta tipe/null, waktu RFC3339, identitas tidak kosong, koordinat station, UTF-8, field duplikat dan kedalaman JSON. Field tambahan diterima tanpa boleh menimpa field kanonis melalui perbedaan kapitalisasi. Rekaman invalid dan valid dipisah; raw record tetap utuh untuk calon penulisan DLQ. Error menyediakan alasan `contract_violation:<detail>`. Adapter belum menulis DLQ atau HazardEvent.

Batas defensif awal: respons untuk decoding 10 MiB, capture arsip maksimal 32 MiB, rekaman 1 MiB, kedalaman 128, dan permintaan 1–1000 item. Nilai ini kebijakan lokal sementara, bukan batas yang dijanjikan vendor. Bila capture melebihi 32 MiB, `Truncated=true` dan raw dikosongkan; worker tidak boleh menganggap payload lengkap atau memajukan checkpoint. Kegagalan transport/body juga tidak menghasilkan page yang boleh di-commit.

**Aturan mapping sementara yang disetujui:** `critical=true` menjadi `AWAS`, selain itu `NORMAL`; `area_name` memakai `province` dari station. Ini asumsi proyek sementara, bukan aturan eksplisit spek, dan belum dihubungkan ke runtime.

**Belum final:** rentang fisik dan kebijakan `quality`, hubungan waktu virtual dengan freshness, serta kebijakan cursor berulang. Mapping, poller durable leader-only, penyisihan melalui store, circuit breaker/backoff dan alert kritis belum dihubungkan ke runtime. Tidak ada ambang fisik yang ditebak oleh adapter. `generated_at` dan `observed_at` dipertahankan; waktu virtual sensor tidak dibandingkan langsung dengan wall clock host.

```sh
cd services/aggregator
go test ./internal/source/pantai
# Tambahkan TEST_PANTAI_URL/TEST_PANTAI_TOKEN untuk uji kontrak image resmi.
```

Sampling image resmi yang terisolasi tersedia lewat `make p5-sample`; lihat [panduan skrip](../../scripts/README.md). Hasil sampling tidak membuktikan penerimaan P5.

### Pembatas resiliensi Sensor Pantai

`pantai.NewResilient` membungkus adapter dengan satu call aktif untuk kedua endpoint. Call tambahan ditolak cepat sebagai `GateError`, tanpa antrean goroutine di dalam wrapper. Setelah tiga call logis gagal berturut-turut, breaker `open` selama sepuluh detik; setelah itu hanya satu probe `half_open`. Probe berhasil menutup breaker; kegagalan membuka kembali. Pembatalan caller/kehilangan lease tidak dianggap bukti sumber pulih, dan permit tetap dilepas.

Nilai tuning **sementara, belum final/terukur pada P5**: maksimal dua attempt per call; backoff eksponensial dengan equal jitter, awal 250 ms dan cap dua detik; budget dua retry per window sepuluh detik, dibagi antara endpoint station/observation. Attempt pertama tidak memakai budget retry. Retry hanya untuk error jaringan/timeout, HTTP 408/429/5xx; HTTP 401, pelanggaran kontrak dan kegagalan pemeriksaan data tidak di-retry langsung. Call gagal tetap dikenai backoff ketika budget habis. `Retry-After` detik maupun HTTP-date dihormati, termasuk saat lebih panjang daripada cooldown; penundaan di atas cap dikembalikan cepat untuk dijadwalkan worker, bukan ditunggu di request.

`CheckPage` menerima kebijakan data-health dari worker, termasuk freshness virtual-time yang masih belum final. `HTTP 200` dengan data-health gagal dapat membuka breaker. Tanpa callback, wrapper tidak membuktikan data segar. Envelope invalid tetap dikembalikan bersama raw dan error untuk penyisihan durable; rekaman invalid dalam page tidak otomatis memblokir saudara validnya. Wrapper tidak memajukan cursor, menyimpan hazard/DLQ, atau melakukan fallback cache.

`Status()` mengekspos state, call aktif, kegagalan berturut, jadwal retry, waktu sukses/gagal dan counter attempt/retry/penolakan. Counter attempt menghitung pemanggilan adapter, termasuk kegagalan verifikasi leader sebelum HTTP; bukan metrik pasti request yang mencapai sensor. Status ini belum dipasang pada status API atau Prometheus. Integrasi worker/runtime, cache/stale propagation dan bukti game day masih pending.

`make p5-resilience` menjalankan tes adapter dan fault-injection dengan race detector dalam container terisolasi tanpa jaringan eksternal. Uji mencakup budget bersama, satu probe, `Retry-After`, timeout, pembatalan, penjagaan data-health serta penolakan berulang tanpa request storm; hasilnya tidak menggantikan pengujian sumber BMKG/PVMBG di cluster atau target latensi M1.
