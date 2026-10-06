# Skrip Pengujian dan Demo

Checkpoint fondasi B memakai konfigurasi lokal dan overlay khusus: lihat [README API Klien](../services/client-api/README.md#checkpoint-fondasi). `check-member-b.py` memeriksa Auth/API Klien dan alur HTTP ke Aggregator; opsi `--check-lifecycle` menghentikan/menjalankan kembali hanya kedua service B. Ini bukan tes penerimaan P2/P3.

Pemeriksaan autentikasi memakai `check-member-b-auth.py`: login tiga identitas, scope, penolakan field mentah, rotasi/replay, serta penolakan access token lama. Opsi `--natural-expiry` menunggu TTL nyata sebelum refresh tanpa login ulang. Skrip membutuhkan Auth/API Klien/Aggregator dengan data kedua sumber; tidak mengubah lifecycle atau status sumber. Baca [panduan autentikasi](../services/client-api/README.md#pemeriksaan-autentikasi). Uji ini belum mencakup silang kredensial upstream, beban P2, atau fanout.

Pemeriksaan resiliensi memakai `check-member-b-resilience.py` pada project uji yang dipilih lewat `--project-name`. Pemeriksaan dasar mencakup proyeksi sumber aman, filter kosong 200, galat JSON, serta trace/redaksi log. `--exercise-outages` mengubah flag outage PVMBG dan menghentikan/menjalankan kembali PostgreSQL/Auth pada project tersebut, lalu memulihkan flag/layanan tanpa menghapus volume. `--burst-connections` menguji burst singkat dan mencatat 200/429 yang teramati; ini bukan beban P2 60 detik. Baca [panduan resiliensi](../services/client-api/README.md#pemeriksaan-resiliensi). Jangan jalankan bersamaan dengan demo lain yang mengubah sumber atau lifecycle.

Jalankan dari root repositori dengan Python 3 stdlib/Docker dan file environment lokal lengkap seperti panduan README utama. Checker C menerima `--env-file` (awal `.env`), `--project-name`, serta `--evidence-dir` untuk menyimpan hasil baru tanpa menimpa bukti lama. Seluruh operasi Compose memakai konfigurasi tersebut dan overlay operator. Nilai efektif dibaca dari Compose, termasuk port host dan interpolasi environment. Jangan menjalankan skenario outage bersamaan.

Untuk mengaktifkan akses operator pada project yang diperiksa:

```sh
docker compose --env-file .env -f docker-compose.yml -f tests/compose-operator.yml up --build -d --wait
python3 scripts/check-p4.py
python3 scripts/check-stage-3.py --output /tmp/fanout-downtime.json
python3 scripts/check-p5-third.py
```

| Skrip | Tujuan dan efek |
|---|---|
| `check-p4.py` | Stop/build/start PVMBG saja; schema v1/v2 live; restart Aggregator/PostgreSQL tanpa menghapus volume; probe storage dari tiga peran jaringan. PVMBG dibiarkan schema v2. Pemeriksaan ini masih membaca Aggregator langsung; bukti persistence melalui Client API perlu dilengkapi. |
| `check-stage-3.py` | Uji fanout, downtime, dan idempotensi: stop/start notifikasi, replay ID sama sebelum/sesudah restart consumer, lalu stop/start broker. Layanan yang dihentikan dipulihkan, volume dipertahankan. `--output` menentukan lokasi hasil JSON sehingga rekaman sebelumnya tidak tertimpa. |
| `check-p5-third.py` | Menjalankan proses subscriber terpisah melalui `tests/compose-review.yml`; mencocokkan event baru dengan dua consumer dasar; memeriksa file/commit/container producer tetap; membersihkan container/queue sementara. |
| `check-stage-2.py` | Khusus broker dengan **kedua consumer offline**; jangan dijalankan pada stack consumer aktif. |

`demo_support.py` berisi helper operator (Compose, request management, SQL, copy journal), tidak dipakai aplikasi atau sebagai shared business logic antarlayanan. Probe SQL hanya untuk operator/demo; layanan consumer tetap tidak mengakses PostgreSQL.

Untuk melihat binding subscriber ketiga secara manual:

```sh
docker compose --env-file .env -f docker-compose.yml -f tests/compose-operator.yml -f tests/compose-review.yml up --build -d --no-deps review-subscriber
docker compose --env-file .env -f docker-compose.yml -f tests/compose-operator.yml -f tests/compose-review.yml logs -f review-subscriber
```

Buka UI RabbitMQ → Exchanges → hazard.events selama subscriber aktif; queue `amq.gen-*` memakai binding baru. Proses berhenti setelah 90 detik. Queue exclusive/non-durable/auto-delete ini hanya untuk demo, tidak menyimpan backlog lintas restart. Output stdout bukan jurnal idempotensi. Penerimaan baru berlaku setelah binding; tidak me-replay sejarah sebelum binding. Cleanup:

```sh
docker compose --env-file .env -f docker-compose.yml -f tests/compose-operator.yml -f tests/compose-review.yml rm -s -f review-subscriber
```

Hasil JSON mencatat ID pesan, correlation ID, dan timestamp selama pengujian. Cocokkan ID producer dengan kedua consumer untuk memeriksa fanout. Angka queue pada UI sesudah recovery bisa sudah nol karena backlog telah dikonsumsi.

## Uji beban, outage, dan startup dari clone

```sh
python3 scripts/check-p2.py
python3 scripts/check-clean-clone.py
```

- P2 memerlukan native k6/lsof. Merecreate PVMBG dengan delay 3 detik, menjalankan 50 VU/60 detik serta mengambil tiga sampel 50 socket TCP, lalu melakukan outage 503/recovery. Aggregator, storage, dan consumer tetap berjalan. Delay kembali ke file environment yang dipilih, schema v2/outage=false pada `finally`. Jangan menjalankan demo lainnya bersamaan.
- Clone menggunakan commit HEAD dan project/volume baru pada port 18081/18082/18083/25672. Perbaikan bootstrap broker sudah berada pada main; perintah default memeriksa HEAD tanpa patch kerja. Hanya project uji yang dihapus; volume utama tetap.
- Untuk menguji bootstrap yang diperbaiki pada volume broker lama dan reconnect consumer, pakai skrip yang sudah ada: `python3 scripts/check-stage-3.py --output /tmp/broker-recovery.json`.

Parameter beban, definisi latensi, dan threshold dijelaskan di [panduan pengujian](../tests/README.md). Autentikasi, outage/recovery, dan burst singkat API Klien sudah memiliki checker pada cabang B; pengukuran P2 berkelanjutan melalui API publik masih perlu dijalankan.

## Pemeriksaan ingestion dan freshness Anggota A

```sh
python3 scripts/check-ingestion.py --output /tmp/tubesaat-ingestion-check.json
```

Memerlukan tujuh layanan A/C yang berjalan dengan environment lokal dan overlay operator. Skrip membandingkan rekaman PVMBG v1/v2 dengan API kanonis dan kedua jurnal consumer, lalu memeriksa data tersimpan, status stale, independensi BMKG, dan recovery saat outage PVMBG. Identitas/waktu startup kontainer harus tetap sama. Kontainer one-off untuk pengujian dikecualikan dari pemeriksaan identitas layanan utama.

Skrip tidak menghentikan kontainer atau menghapus volume/queue. Ia menambah laporan v2 dan snapshot/outbox/jurnal hasilnya, lalu memulihkan PVMBG ke skema v1 serta outage=false. Jangan jalankan bersamaan dengan demo yang mengubah status sumber. Hasil dan timestamp ditulis ke berkas output; tidak memerlukan `jq`. Freshness menunjukkan keberhasilan pipeline polling/commit, bukan umur tiap rekaman atau publisher confirm.

## Deployment default

```sh
python3 scripts/check-deployment.py --env-file .env --output /tmp/deployment-check.json
```

Checker memeriksa Compose utama tanpa overlay operator: sembilan layanan healthy, tidak ada port host Aggregator/DB, ownership jaringan/secret, query kedua sumber lewat tiga peran, serta correlation ID/latensi pada log Client API, Auth, dan Aggregator. `--start` membangun/menjalankan stack default, sehingga akan menutup kembali port operator pada project tersebut. `--project-name` memilih project uji. Environment tetap lokal; bukti tidak memuat token/password.

Gunakan overlay operator hanya untuk checker yang membutuhkan HTTP Aggregator dari host. Checker clone masih menguji tujuh layanan A/C dari HEAD; file environment lokal lengkap dipakai dari luar clone, port Auth/API pada project clone dipisahkan dari stack utama, dan perubahan source workspace belum ikut pengujian.
