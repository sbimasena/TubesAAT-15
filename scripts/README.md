# Skrip Pengujian dan Demo

Dari root repository, cukup siapkan `.env` sekali dan jalankan stack:

```sh
python3 scripts/setup-env.py
docker compose up --build -d --wait
```

Setelah itu jalankan checker satu per satu. Semua checker pada stack ini memakai konfigurasi efektif dari Compose utama, termasuk port host dan environment; tidak perlu overlay operator atau memilih nama project secara manual. Pembacaan Aggregator dilakukan dengan `docker compose exec -T aggregator /service --inspect`, sehingga port Aggregator tetap tertutup.

Hasil terbaru tersimpan di `artifacts/checks/<nama-checker>/` dan tidak menimpa bukti laporan pada `docs/evidence/`. Setiap checker mencetak lokasi hasil. Run berikutnya menimpa hasil terbaru pada direktori yang sama; gunakan `--evidence-dir <direktori>` jika ingin menyimpan run terpisah. Exit code 0 berarti lulus. Jangan jalankan skenario outage/lifecycle bersamaan.

## Deployment default

```sh
python3 scripts/check-deployment.py
```

Memeriksa sembilan layanan healthy, port Aggregator/DB tertutup, ownership jaringan/secret, query dua sumber melalui tiga identitas, serta correlation ID/latensi pada log Client API, Auth, dan Aggregator. `--start` dapat membangun/menjalankan stack lebih dahulu. Jika sebelumnya menggunakan overlay operator manual, jalankan Compose utama kembali untuk menutup port sebelum checker deployment.

## Pemeriksaan ingestion dan freshness Anggota A

```sh
python3 scripts/check-ingestion.py
```

Memeriksa mapping PVMBG v1/v2, confidence level, snapshot kanonis di dua jurnal consumer, freshness dan data tersimpan saat PVMBG outage, independensi BMKG, lalu recovery. Container utama tidak boleh restart. Skrip menambah laporan/snapshot/outbox/jurnal, lalu mengembalikan PVMBG ke v1/outage=false. Data yang ditambahkan tetap tersimpan.

## Silang kredensial upstream (P3)

```sh
python3 scripts/check-cross-credentials.py
python3 scripts/check-member-b-auth.py --natural-expiry
```

Checker upstream memeriksa kedua endpoint BMKG dan laporan PVMBG. Kredensial sendiri harus menghasilkan 200/array JSON; kredensial sumber lain pada header native, header trust domain asing, dan request tanpa kredensial harus menghasilkan 401/403. Kedua kredensial harus berbeda; redirect tidak diikuti. Bukti hanya berisi status/correlation ID, tanpa kredensial/payload. Tidak mengubah layanan.

Checker Auth memeriksa tiga identitas, tujuh/sebelas field, metadata publik yang aman, penolakan field mentah, rotasi/replay, dan token lama. `--natural-expiry` menunggu TTL access nyata (maksimal 300 detik), lalu refresh tanpa login ulang. Tanpa opsi tersebut, pemeriksaan expiry alami dilewati. Kredensial/port dibaca dari Compose, sama seperti checker lain. [Detail kontrak Auth](../services/client-api/README.md#pemeriksaan-autentikasi).

## Storage dan lifecycle mandiri (P4)

```sh
python3 scripts/check-p4.py
```

Stop/build/start PVMBG saja; ubah schema v1/v2; cocokkan JSONB dengan pembacaan melalui tiga scope; restart Aggregator/PostgreSQL dengan volume tetap; probe storage dari tiga peran jaringan; stop/start Auth/API secara mandiri; dan cocokkan trace HTTP. Schema PVMBG awal dipulihkan. Restart Auth menghapus sesi lama; checker login ulang. Probe jaringan memakai `tests/compose-probes.yml` secara otomatis dan tidak membagikan password DB ke peran client/consumer.

## Fanout, downtime, dan idempotensi (P5)

```sh
python3 scripts/check-stage-3.py
python3 scripts/check-p5-third.py
```

Checker consumer mematikan notifikasi sementara dashboard terus menerima event, memeriksa backlog dan catch-up, replay ID yang sama sebelum/sesudah restart consumer, lalu mematikan/menjalankan broker untuk menguji reconnect. Layanan dipulihkan dan volume dipertahankan. ID pesan harus hanya muncul sekali pada jurnal tiap consumer.

Checker subscriber ketiga membangun proses terpisah lewat `tests/compose-review.yml`, menunggu binding baru, lalu mencocokkan satu event baru di ketiga subscriber. Source/commit/container producer harus tetap. Container dan queue sementara dibersihkan otomatis. Queue exclusive/non-durable/auto-delete ini tidak menerima sejarah sebelum binding dan tidak menyimpan backlog lintas restart.

## Recovery broker dengan consumer offline

```sh
python3 scripts/check-stage-2.py
```

Jalankan langsung pada stack lengkap. Checker mematikan kedua consumer, menunggu backlog, memeriksa pesan persistent tiap queue, lalu menghentikan broker. Polling harus tetap menulis outbox pending dan query Aggregator tetap 200. Sesudah broker dijalankan, outbox harus terbit tanpa restart Aggregator dan snapshot queue harus tetap ada.

Probe queue memakai requeue, tanpa ack permanen/purge. Sesudah selesai atau gagal, checker menjalankan kembali hanya consumer yang sebelumnya berjalan. Fanout dan deduplikasi diperiksa terpisah oleh `check-stage-3.py`.

## Resiliensi HTTP tambahan

```sh
python3 scripts/check-member-b-resilience.py
python3 scripts/check-member-b-resilience.py --exercise-outages --burst-connections 64
```

Pemeriksaan dasar mencakup proyeksi aman, filter kosong 200, galat JSON, refresh, trace dan redaksi log. `--exercise-outages` mengubah outage PVMBG dan stop/start PostgreSQL/Auth, lalu memulihkan flag/layanan. `--burst-connections` menerima 2–256 koneksi untuk burst singkat, mencatat 200/429; ini tidak menggantikan beban P2. [Detail resiliensi](../services/client-api/README.md#pemeriksaan-resiliensi).

## Beban Client API terautentikasi

```sh
python3 scripts/check-client-p2.py
```

Memerlukan k6 dan lsof native pada host, sembilan layanan healthy, serta access TTL 60 detik. K6 menyiapkan 50 sesi Field Team sebelum pengukuran. Tiap VU menyimpan pasangan token sendiri dan refresh sebelum expiry. Dua run masing-masing 50 VU/60 detik memakai query BMKG-only: PVMBG delay 3000 ms, lalu outage. Socket proses k6 ke Client API disampel pada detik 15/30/45.

p95 request lengkap dan p95 respons 200 harus <300 ms, error tidak terkontrol <1%, setiap run sedikitnya 50 refresh berhasil dan query 200. 429 hanya dianggap terkontrol jika kode `concurrency_limit`, correlation ID, dan `Retry-After: 1` benar; jumlah/rate 429 dan throughput tetap dicatat. Login/refresh terpisah dari metrik query.

Checker memeriksa fetch upstream lambat/503, last-known PVMBG/stale, progres BMKG dan laporan baru setelah recovery. Delay/schema/outage awal dibaca dari health PVMBG dan dipulihkan melalui `finally`, termasuk saat threshold gagal. Layanan inti selain PVMBG tidak direstart. Hasil tergantung mesin/config; baca `p2-client-check.json`, summary dan log kedua run.

## Startup dari clone

```sh
python3 scripts/check-clean-clone.py
```

Menjalankan commit HEAD pada project/volume baru dengan secret/password sementara sendiri, tanpa port operator/DB. Port 28081/28082/28084/28080/35672 harus bebas. `--revision` memilih commit. Perubahan aplikasi/config yang belum di-commit tidak ikut diuji; SHA dicatat. `--project-name` pada checker ini memilih project lama yang diamati, bukan nama project clone.

Hanya resource clone dihapus, termasuk volumenya; container project lama harus tetap. Build cache dapat dipakai, sehingga ini bukan uji download internet tanpa cache. Checker deployment workspace dipakai untuk memeriksa aplikasi pada checkout commit. Sesudah commit perubahan aplikasi/config, ulangi checker jika ingin membuktikan versi itu.

## Bukti laporan P1 dan diagram Bab 3

```sh
python3 scripts/check-p1-report.py
python3 scripts/show-p1-evidence.py runtime --input artifacts/checks/p1-report/report-check.json
```

Checker laporan menjalankan ingestion dan mencocokkan payload BMKG/warning serta PVMBG v1/v2 dengan data kanonis. Ia memeriksa migrasi, container, trace dan consumer; menambah data/jurnal lalu memulihkan PVMBG ke v1/outage=false. `show-p1-evidence.py` membaca hasil tersimpan; bagian lain adalah `bmkg` dan `pvmbg`. [Bukti P1](../docs/evidence/member-a/p1/README.md) menjelaskan rendering terminal dengan Freeze.

`render-report-diagrams.py` hanya membuat SVG/PNG arsitektur. Dependensi, lokasi dan caption ada di [panduan diagram](../docs/diagrams/README.md). Rendering tidak menjalankan ulang pengujian.

## Opsi untuk project atau hasil terpisah

Pemakaian biasa tidak membutuhkan opsi ini. Jika startup memakai file env/nama project khusus, teruskan nilai yang sama ke checker:

```sh
docker compose --env-file .env.acceptance.local --project-name tubesaat-acceptance up --build -d --wait
python3 scripts/check-ingestion.py --env-file .env.acceptance.local --project-name tubesaat-acceptance --evidence-dir /tmp/ingestion-run
```

Nama project memisahkan container/volume, tetapi port host tetap harus bebas. Tidak perlu menambah file Compose operator. `--output <file.json>` tersedia pada checker deployment, ingestion, Auth, upstream, resiliensi, laporan P1 dan consumer; `--evidence-dir` tersedia pada semua checker. Jika memilih `--output`, file JSON memakai path itu.

## Log progres dan pengujian helper

Progres dikirim ke stderr tanpa buffering; JSON/stdout tetap terpisah. Polling panjang mencetak heartbeat; P2 mencetak checkpoint dan ringkasan tiap run. Secret/token tidak dicetak. Untuk menyimpan log gabungan:

```sh
set -o pipefail
python3 scripts/check-client-p2.py 2>&1 | tee /tmp/p2-progress.log
```

Tes helper/konfigurasi/lifecycle tidak membutuhkan Docker:

```sh
python3 -B -m unittest discover -s scripts -p 'test_*.py'
```

Untuk pengujian modul Go dan integrasi storage/consumer, lihat [panduan pengujian](../tests/README.md).
