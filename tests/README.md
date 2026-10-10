# Pengujian Lintas Layanan

Isi file environment lokal lengkap seperti README utama. Checker pada stack yang sudah berjalan menerima `--env-file` dan `--project-name`; gunakan project uji untuk outage/restart. Clean clone menghasilkan environment sendiri. Demo yang membaca Aggregator dari host membutuhkan `tests/compose-operator.yml`. Compose utama sendiri menutup port Aggregator/DB.

```sh
python3 scripts/check-deployment.py --env-file .env --output /tmp/deployment-check.json
python3 -B -m unittest discover -s scripts -p test_demo_support.py
k6 --address '' run --quiet tests/check-load-summary.js
```

Checker deployment memeriksa stack default dan alur HTTP terlindungi. Tes helper memeriksa pemilihan project, port host efektif, penerusan opsi checker, redaksi secret saat Compose gagal, serta penggunaan token baru setelah refresh. Cek summary k6 memastikan token dari data setup tidak disimpan pada hasil beban.


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

Sembilan layanan fungsional harus sudah berjalan. P4 melakukan rebuild PVMBG saja, mencocokkan JSONB v1/v2 dengan respons Client API ketiga peran sebelum/sesudah restart Aggregator/DB, memeriksa restart mandiri Auth/API dan trace HTTP, serta memakai `tests/compose-probes.yml` untuk probe jaringan dengan image PostgreSQL tanpa password database. Probe merupakan container operator dengan jaringan setara peran layanan; request Client API yang nyata diperiksa terpisah dari probe.

P5 memakai program `services/dashboard-consumer/cmd/review` pada container tersendiri/queue baru melalui `tests/compose-review.yml`. Producer dan kedua consumer dasar tidak dibuild/restart pada demo subscriber ketiga. Queue sementara exclusive/non-durable dibersihkan setelah selesai. Program memakai client AMQP yang sudah terpasang pada modul dashboard; tidak memanggil handler/logic dashboard. Build/testing demo ini tidak memerlukan modul aplikasi tambahan.

Skrip fanout/recovery `check-stage-3.py` memakai helper `demo_support.py` dan argumen `--output` untuk menyimpan rekaman demo terpisah dari hasil pemeriksaan sebelumnya.

## Reproduksi dari commit pada volume kosong

```sh
python3 scripts/check-clean-clone.py
```

Clone lokal HEAD ke temporary directory dan build/start sembilan layanan dengan Compose utama tanpa overlay operator. Secret/password acak dibuat pada file 0600 di luar checkout; tidak memerlukan `.env` pribadi. Port host 28081/28082/28084/28080/35672 harus bebas. Aggregator/PostgreSQL tidak memiliki port host. `--revision` memilih commit, `--evidence-dir` memilih lokasi bukti, dan `--project-name` opsional memilih project lama yang diamati, bukan nama project clone.

Checker deployment workspace memeriksa source aplikasi pada checkout commit: sembilan healthy, login tiga peran, field Media/kanonis, data kedua sumber, isolasi konfigurasi/port, dan trace HTTP. Satu event outbox confirmed dicocokkan dengan payload/revisi/correlation pada kedua jurnal. Checkout harus bersih sebelum/sesudah pemeriksaan; tidak ada source yang disalin atau patch sementara.

Cleanup hanya project clone dengan `down --volumes --remove-orphans`; ID/StartedAt container Compose yang sudah ada harus tetap. Build cache/image lokal dapat digunakan, sehingga ini bukan tes download internet tanpa cache. Hasil mencatat SHA aplikasi yang diuji. Perubahan workspace belum ikut checkout commit; jalankan ulang setelah perubahan aplikasi/config yang relevan di-commit.

## P2 melalui Client API

```sh
python3 scripts/check-client-p2.py --env-file .env --project-name project-uji --evidence-dir /tmp/client-p2
```

Sembilan layanan harus healthy. Dua run masing-masing 50 VU/60 detik menggunakan query BMKG-only, pertama saat PVMBG delay 3000 ms, lalu saat PVMBG outage. K6 membuat sesi terpisah per VU dan refresh tanpa login ulang dengan access TTL 60 detik. Socket diambil pada detik sekitar 15/30/45 setelah persiapan sesi selesai; targetnya port Client API, bukan port Aggregator.

Metrik query mencatat throughput, p50/p95/p99, error tidak terkontrol, jumlah/rate 429, dan jumlah 200. p95 waktu request lengkap serta p95 respons 200 harus <300 ms dan error tidak terkontrol <1%. Respons 429 hanya dianggap terkontrol bila kode `concurrency_limit`, correlation ID, dan `Retry-After: 1` benar. Login/refresh memiliki tag/metrik sendiri; sedikitnya 50 refresh berhasil per run dan tidak ada refresh failure.

Checker memeriksa last-known PVMBG/stale di bawah beban, ingestion BMKG yang berlanjut, report baru setelah recovery, dan identitas layanan inti yang tetap. Health mock melaporkan schema/delay/outage aktual; seluruh nilai awal dipulihkan melalui `finally`, termasuk pada run gagal. Hasil direct Aggregator tetap menjadi baseline terpisah. Rincian runner ada di [panduan skrip](../scripts/README.md#beban-client-api-terautentikasi).
