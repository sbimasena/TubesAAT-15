# Pengujian Lintas Layanan

Tahap 1–2 memakai PostgreSQL dan RabbitMQ nyata, schema sementara, serta exchange/queue uji terpisah:

```sh
docker compose --env-file .env.example up --build -d canonical-store message-broker bmkg pvmbg aggregator
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-storage.yml run --build --rm --no-deps aggregator
```

Override menjalankan tes modul Aggregator pada builder image dan jaringan layanan. Tes tidak mengubah data canonical atau queue dasar; schema, exchange, dan queue uji dibersihkan setelah selesai normal. Pemeriksaan meliputi deduplikasi, update/revisi, JSONB v1/v2, rollback, retry cursor, HTTP outage, publisher confirms, metadata persistent, cancellation, pengiriman ulang accepted-but-unmarked, dan mandatory return tanpa route.

Tanpa `TEST_DATABASE_URL`/`TEST_BROKER_URL`, tes integrasi lokal dilewati; tes HTTP/poller tetap berjalan. `go test -race ./...` dan `go vet ./...` dapat dijalankan dari `services/aggregator`.

## Recovery broker tahap 2

```sh
python3 scripts/check-stage-2.py
```

Gunakan konfigurasi development `.env.example` dan lima layanan yang sudah berjalan. Skrip memerlukan Python stdlib/Docker serta tidak boleh dijalankan ketika consumer aktif. Ia stop/start broker sementara; memeriksa API 200, ingestion/outbox selama downtime, recovery tanpa restart Aggregator, serta pesan durable yang masih ada setelah restart. Probe queue memakai requeue, tidak ack permanen/purge; field redelivery dapat berubah. Statistik management setelah boot ditunggu hingga tersedia. Broker selalu di-start pada blok `finally` jika pemeriksaan downtime gagal.

Hasil sanitasi: `docs/evidence/anggota-c/stage-2/broker-recovery.json`. Output tes Compose: `docs/evidence/anggota-c/stage-2/integration-check.log`. Bukti tahap 1 tetap pada direktori `stage-1/`. Bukti kedua consumer/idempotensi tersedia pada tahap 3; load/integrasi B menunggu tahap 5.

## Dua consumer tahap 3

```sh
docker compose --env-file .env.example up --build -d notification-consumer dashboard-consumer
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-consumers.yml run --build --rm --no-deps notification-consumer
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-consumers.yml run --build --rm --no-deps dashboard-consumer
python3 scripts/check-stage-3.py
```

Override menjalankan race detector pada dua modul mandiri, menggunakan file sementara dan fake acknowledgement untuk memeriksa urutan persist/ack, commit sebelum ack gagal, replay lintas restart, writer kedua, update ID baru/revisi stale, torn tail, payload tambahan besar, rejection, IO failure tanpa ack, dan korupsi jurnal. Volume consumer runtime tidak dipakai container tes.

Skrip memakai broker nyata/konfigurasi `.env.example`: producer serta dashboard tetap berjalan ketika notifikasi offline; event yang sama dibaca notifikasi sesudah restart. Replay satu ID producer dilakukan sebelum/sesudah restart consumer, lalu jumlah hasil pada setiap jurnal diperiksa tetap satu. Replay aplikasi ini dapat memiliki redelivered=false; crash sebelum ack disimulasikan pada tes journal. Restart broker memeriksa health unavailable dan reconnect tanpa restart consumer/producer. Semua layanan yang dihentikan dipulihkan; volume dan queue tidak dihapus. Tujuh layanan harus sudah berjalan dan menghasilkan event mock periodik. Output: `docs/evidence/anggota-c/stage-3/`.

Skrip tahap 2 secara khusus mengharuskan consumer offline; untuk stack aktif tahap 3 gunakan skrip tahap 3.

## Demo P4/P5 tahap 4

```sh
python3 scripts/check-p4.py
python3 scripts/check-stage-3.py --output docs/evidence/anggota-c/stage-4/fanout-downtime.json
python3 scripts/check-p5-third.py
```

Tujuh layanan fungsional harus sudah berjalan. P4 melakukan rebuild PVMBG saja, membaca v1/v2 JSONB sesudah restart Aggregator/DB, serta memakai `tests/compose-probes.yml` untuk probe jaringan dengan image PostgreSQL tanpa password database. Probe merupakan container operator dengan jaringan setara peran layanan, **bukan** request dari kode Client API yang belum ada.

P5 memakai program `services/dashboard-consumer/cmd/review` pada container tersendiri/queue baru melalui `tests/compose-review.yml`. Producer dan kedua consumer dasar tidak dibuild/restart pada demo subscriber ketiga. Queue sementara exclusive/non-durable dibersihkan setelah selesai. Program memakai client AMQP yang sudah terpasang pada modul dashboard; tidak memanggil handler/logic dashboard. Build/testing demo ini tidak memerlukan modul aplikasi tambahan.

Skrip tahap 3 sekarang memakai `demo_support.py` dan argumen `--output`; pemeriksaan nyata tahap 4 menjalankan ulang alur tersebut, sehingga bukti historis tahap 3 tetap disimpan. Laporan: `docs/anggota-c-p4-p5-report.md`.
