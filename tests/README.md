# Pengujian Lintas Layanan

Isi file environment lokal lengkap seperti README utama. Semua checker C menerima `--env-file` dan `--project-name`; gunakan project uji untuk outage/restart. Demo yang membaca Aggregator dari host membutuhkan `tests/compose-operator.yml`. Compose utama sendiri menutup port Aggregator/DB.

```sh
python3 scripts/check-deployment.py --env-file .env --output /tmp/deployment-check.json
python3 -B -m unittest discover -s scripts -p test_demo_support.py
```

Checker deployment memeriksa stack default dan alur HTTP terlindungi. Tes helper memeriksa pemilihan project, port host efektif, penerusan opsi checker, serta redaksi secret saat Compose gagal.


## Persistence PostgreSQL dan publisher RabbitMQ

Pengujian memakai PostgreSQL dan RabbitMQ nyata, schema sementara, serta exchange/queue uji terpisah:

```sh
docker compose --env-file .env up --build -d canonical-store message-broker bmkg pvmbg aggregator
docker compose --env-file .env -f docker-compose.yml -f tests/compose-storage.yml run --build --rm --no-deps aggregator
```

Override menjalankan tes modul Aggregator pada builder image dan jaringan layanan. Tes tidak mengubah data canonical atau queue dasar; schema, exchange, dan queue uji dibersihkan setelah selesai normal. Pemeriksaan meliputi deduplikasi, update/revisi, JSONB v1/v2, rollback, retry cursor, HTTP outage, publisher confirms, metadata persistent, cancellation, pengiriman ulang accepted-but-unmarked, dan mandatory return tanpa route.

Tanpa `TEST_DATABASE_URL`/`TEST_BROKER_URL`, tes integrasi lokal dilewati; tes HTTP/poller tetap berjalan. `go test -race ./...` dan `go vet ./...` dapat dijalankan dari `services/aggregator`.

## Recovery broker dengan consumer offline

```sh
python3 scripts/check-stage-2.py
```

Gunakan konfigurasi lokal `.env` dan lima layanan yang sudah berjalan. Skrip memerlukan Python stdlib/Docker serta tidak boleh dijalankan ketika consumer aktif. Ia stop/start broker sementara; memeriksa API 200, ingestion/outbox selama downtime, recovery tanpa restart Aggregator, serta pesan durable yang masih ada setelah restart. Probe queue memakai requeue, tidak ack permanen/purge; field redelivery dapat berubah. Statistik management setelah boot ditunggu hingga tersedia. Broker selalu di-start pada blok `finally` jika pemeriksaan downtime gagal.

Hasil pemeriksaan menunjukkan apakah ingestion, pending outbox, dan delivery pulih setelah broker tersedia kembali. Checker ini berfokus pada broker, bukan pengujian Auth/Client API.

## Fanout, downtime, dan idempotensi kedua consumer

```sh
docker compose --env-file .env up --build -d notification-consumer dashboard-consumer
docker compose --env-file .env -f docker-compose.yml -f tests/compose-consumers.yml run --build --rm --no-deps notification-consumer
docker compose --env-file .env -f docker-compose.yml -f tests/compose-consumers.yml run --build --rm --no-deps dashboard-consumer
python3 scripts/check-stage-3.py
```

Override menjalankan race detector pada dua modul mandiri, menggunakan file sementara dan fake acknowledgement untuk memeriksa urutan persist/ack, commit sebelum ack gagal, replay lintas restart, writer kedua, update ID baru/revisi stale, torn tail, payload tambahan besar, rejection, IO failure tanpa ack, dan korupsi jurnal. Volume consumer runtime tidak dipakai container tes.

Skrip memakai broker nyata/konfigurasi `.env`: producer serta dashboard tetap berjalan ketika notifikasi offline; event yang sama dibaca notifikasi sesudah restart. Replay satu ID producer dilakukan sebelum/sesudah restart consumer, lalu jumlah hasil pada setiap jurnal diperiksa tetap satu. Replay aplikasi ini dapat memiliki redelivered=false; crash sebelum ack disimulasikan pada tes journal. Restart broker memeriksa health unavailable dan reconnect tanpa restart consumer/producer. Semua layanan yang dihentikan dipulihkan; volume dan queue tidak dihapus. Tujuh layanan harus sudah berjalan dan menghasilkan event mock periodik. Gunakan `--output <file.json>` untuk menentukan lokasi hasil pemeriksaan.

Gunakan `check-stage-3.py` untuk stack dengan consumer aktif. Skrip `check-stage-2.py` khusus menguji broker dengan kedua consumer offline.

## Demo independensi container, storage, dan fanout event (P4/P5)

```sh
python3 scripts/check-p4.py
python3 scripts/check-stage-3.py --output /tmp/fanout-downtime.json
python3 scripts/check-p5-third.py
```

Tujuh layanan fungsional harus sudah berjalan. P4 melakukan rebuild PVMBG saja, membaca v1/v2 JSONB sesudah restart Aggregator/DB, serta memakai `tests/compose-probes.yml` untuk probe jaringan dengan image PostgreSQL tanpa password database. Probe merupakan container operator dengan jaringan setara peran layanan, bukan pengganti bukti request Client API yang nyata.

P5 memakai program `services/dashboard-consumer/cmd/review` pada container tersendiri/queue baru melalui `tests/compose-review.yml`. Producer dan kedua consumer dasar tidak dibuild/restart pada demo subscriber ketiga. Queue sementara exclusive/non-durable dibersihkan setelah selesai. Program memakai client AMQP yang sudah terpasang pada modul dashboard; tidak memanggil handler/logic dashboard. Build/testing demo ini tidak memerlukan modul aplikasi tambahan.

Skrip fanout/recovery `check-stage-3.py` memakai helper `demo_support.py` dan argumen `--output` untuk menyimpan rekaman demo terpisah dari hasil pemeriksaan sebelumnya.

## Uji beban, outage, dan recovery Aggregator (P2)

```sh
python3 scripts/check-p2.py
```

Memerlukan native k6 dan lsof, Python stdlib/Docker, serta tujuh layanan development yang sudah berjalan. `tests/load-p2.js` menjalankan 50 VU konstan selama 60 detik, HTTP keep-alive, query BMKG-only dengan limit 100; threshold p95 waktu request lengkap <300 ms dan non-200 <1%. Semua 429 ikut dihitung error karena endpoint Aggregator yang diuji tidak memiliki limiter. Correlation ID dan data kanonis diperiksa setiap request. Sampel lsof pada detik sekitar 15/30/45 mencatat socket ESTABLISHED milik proses k6, bukan hanya jumlah VU.

`tests/compose-load.yml` merecreate PVMBG saja dengan delay 3000 ms. Skrip memeriksa log panggilan lambat saat load, simulasi 503 melalui admin/outage, data vulkanik lama/status sumber, polling BMKG berlanjut, dan report baru sesudah recovery tanpa restart Aggregator. `finally` memulihkan delay file environment yang dipilih, outage=false dan schema v2. Jangan jalankan bersamaan dengan demo lain. Scope langsung Aggregator; Pengukuran berkelanjutan melalui Auth/Client API belum dicakup checker ini.

Metrik mencakup throughput, p50/p95/p99, error rate, dan jumlah respons 429. `bmkg_elapsed_ms` mengukur waktu sejak sebelum request sampai seluruh body diterima, termasuk connection setup; `http_req_duration` bawaan k6 mengukur sending/waiting/receiving. Respons Aggregator menyediakan available/last_success_at/last_error untuk status sumber; `last_success_at` menandai fetch upstream; `last_ingested_at` menandai siklus lengkap yang sudah commit. `stale`/`stale_since` menandai kegagalan atau pipeline yang melewati ambang waktu, bukan usia tiap rekaman atau delivery broker. Lihat kontrak freshness di README Aggregator.

## Reproduksi dari commit pada volume kosong

```sh
python3 scripts/check-clean-clone.py
```

Clone lokal HEAD ke temporary directory, build/start tujuh layanan A/C memakai file environment lokal lengkap dan overlay operator, dengan project, jaringan, volume baru serta localhost port 18081/18082/18083/25672. Pastikan port itu bebas. Memeriksa kedua sumber, canonical API, satu snapshot outbox confirmed yang diterima kedua jurnal, dan health. Project uji dibersihkan dengan `down --volumes --remove-orphans`; ID/StartedAt stack utama harus tetap. Docker build cache/image yang sudah tersedia dapat digunakan, jadi ini bukan tes download internet tanpa cache.

Untuk diagnosis perubahan bootstrap yang belum di-commit saja, tersedia `--with-working-broker-fix`. Opsi ini menyalin **hanya** script startup broker ke clone, merekam nama file/SHA256/dirty status, dan menandai hasil sebagai clone dengan patch eksplisit. Perbaikan bootstrap broker sudah berada pada main; pemeriksaan default membuktikan HEAD tanpa perubahan. Tidak ada commit yang dibuat skrip.
