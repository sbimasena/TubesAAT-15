# Skrip Pengujian dan Demo

Checkpoint fondasi B memakai konfigurasi lokal dan overlay khusus: lihat [README API Klien](../services/client-api/README.md#checkpoint-stage-23). `check-member-b.py` memeriksa Auth/API Klien dan alur HTTP ke Aggregator; opsi `--check-lifecycle` menghentikan/menjalankan kembali hanya kedua service B. Ini bukan tes penerimaan P2/P3.

Checkpoint Stage 4 memakai `check-member-b-auth.py`: login tiga identitas, scope, penolakan field mentah, rotasi/replay, serta penolakan access token lama. Opsi `--natural-expiry` menunggu TTL nyata sebelum refresh tanpa login ulang. Skrip membutuhkan Auth/API Klien/Aggregator dengan data kedua sumber; tidak mengubah lifecycle atau status sumber. Baca [panduan Stage 4](../services/client-api/README.md#checkpoint-stage-4). Uji ini belum mencakup silang kredensial upstream, beban P2, atau fanout.

Checkpoint Stage 5 memakai `check-member-b-resilience.py` pada project uji yang dipilih lewat `--project-name`. Pemeriksaan dasar mencakup proyeksi sumber aman, filter kosong 200, galat JSON, serta trace/redaksi log. `--exercise-outages` mengubah flag outage PVMBG dan menghentikan/menjalankan kembali PostgreSQL/Auth pada project tersebut, lalu memulihkan flag/layanan tanpa menghapus volume. `--burst-connections` menguji burst singkat dan mencatat 200/429 yang teramati; ini bukan beban P2 60 detik. Baca [panduan Stage 5](../services/client-api/README.md#checkpoint-stage-5). Jangan jalankan bersamaan dengan demo lain yang mengubah sumber atau lifecycle.

Jalankan dari root repositori dengan Python 3 stdlib/Docker dan konfigurasi development `.env.example`. Skrip memakai network/volume stack yang sudah berjalan; tidak menghapus Canonical Store atau jurnal. Kredensial dibaca untuk request tetapi tidak dicatat ke bukti. Jangan jalankan skenario outage secara bersamaan.

```sh
docker compose --env-file .env.example up --build -d bmkg pvmbg canonical-store message-broker aggregator notification-consumer dashboard-consumer
python3 scripts/check-p4.py
python3 scripts/check-stage-3.py --output /tmp/fanout-downtime.json
python3 scripts/check-p5-third.py
```

| Skrip | Tujuan dan efek |
|---|---|
| `check-p4.py` | Stop/build/start PVMBG saja; schema v1/v2 live; restart Aggregator/PostgreSQL tanpa menghapus volume; probe storage dari tiga peran jaringan. PVMBG dibiarkan schema v2. Client API HTTP masih menunggu B. |
| `check-stage-3.py` | Uji fanout, downtime, dan idempotensi: stop/start notifikasi, replay ID sama sebelum/sesudah restart consumer, lalu stop/start broker. Layanan yang dihentikan dipulihkan, volume dipertahankan. `--output` menentukan lokasi hasil JSON sehingga rekaman sebelumnya tidak tertimpa. |
| `check-p5-third.py` | Menjalankan proses subscriber terpisah melalui `tests/compose-review.yml`; mencocokkan event baru dengan dua consumer dasar; memeriksa file/commit/container producer tetap; membersihkan container/queue sementara. |
| `check-stage-2.py` | Khusus broker dengan **kedua consumer offline**; jangan dijalankan pada stack consumer aktif. |

`demo_support.py` berisi helper operator (Compose, request management, SQL, copy journal), tidak dipakai aplikasi atau sebagai shared business logic antarlayanan. Probe SQL hanya untuk operator/demo; layanan consumer tetap tidak mengakses PostgreSQL.

Untuk melihat binding subscriber ketiga secara manual:

```sh
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-review.yml up --build -d --no-deps review-subscriber
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-review.yml logs -f review-subscriber
```

Buka UI RabbitMQ → Exchanges → hazard.events selama subscriber aktif; queue `amq.gen-*` memakai binding baru. Proses berhenti setelah 90 detik. Queue exclusive/non-durable/auto-delete ini hanya untuk demo, tidak menyimpan backlog lintas restart. Output stdout bukan jurnal idempotensi. Penerimaan baru berlaku setelah binding; tidak me-replay sejarah sebelum binding. Cleanup:

```sh
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-review.yml rm -s -f review-subscriber
```

Hasil JSON mencatat ID pesan, correlation ID, dan timestamp selama pengujian. Cocokkan ID producer dengan kedua consumer untuk memeriksa fanout. Angka queue pada UI sesudah recovery bisa sudah nol karena backlog telah dikonsumsi.

## Uji beban, outage, dan startup dari clone

```sh
python3 scripts/check-p2.py
python3 scripts/check-clean-clone.py
```

- P2 memerlukan native k6/lsof. Merecreate PVMBG dengan delay 3 detik, menjalankan 50 VU/60 detik serta mengambil tiga sampel 50 socket TCP, lalu melakukan outage 503/recovery. Aggregator, storage, dan consumer tetap berjalan. Delay kembali ke `.env.example`, schema v2/outage=false pada `finally`. Jangan menjalankan demo lainnya bersamaan.
- Clone menggunakan commit HEAD dan project/volume baru pada port 18081/18082/18083/25672. Perbaikan bootstrap broker sudah berada pada main; perintah default memeriksa HEAD tanpa patch kerja. Hanya project uji yang dihapus; volume utama tetap.
- Untuk menguji bootstrap yang diperbaiki pada volume broker lama dan reconnect consumer, pakai skrip yang sudah ada: `python3 scripts/check-stage-3.py --output /tmp/broker-recovery.json`.

Parameter beban, definisi latensi, dan threshold dijelaskan di [panduan pengujian](../tests/README.md). Autentikasi, outage/recovery, dan burst singkat API Klien sudah memiliki checker pada cabang B; pengukuran P2 berkelanjutan melalui API publik masih perlu dijalankan.

## Pemeriksaan ingestion dan freshness Anggota A

```sh
python3 scripts/check-ingestion.py --output /tmp/tubesaat-ingestion-check.json
```

Memerlukan tujuh layanan A/C yang berjalan dengan `.env.example`. Skrip membandingkan rekaman PVMBG v1/v2 dengan API kanonis dan kedua jurnal consumer, lalu memeriksa data tersimpan, status stale, independensi BMKG, dan recovery saat outage PVMBG. Identitas/waktu startup kontainer harus tetap sama. Kontainer one-off untuk pengujian dikecualikan dari pemeriksaan identitas layanan utama.

Skrip tidak menghentikan kontainer atau menghapus volume/queue. Ia menambah laporan v2 dan snapshot/outbox/jurnal hasilnya, lalu memulihkan PVMBG ke skema v1 serta outage=false. Jangan jalankan bersamaan dengan demo yang mengubah status sumber. Hasil dan timestamp ditulis ke berkas output; tidak memerlukan `jq`. Freshness menunjukkan keberhasilan pipeline polling/commit, bukan umur tiap rekaman atau publisher confirm.
