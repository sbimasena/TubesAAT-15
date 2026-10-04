# Skrip demo Anggota C

Jalankan dari root repositori dengan Python 3 stdlib/Docker dan konfigurasi development `.env.example`. Skrip memakai network/volume stack yang sudah berjalan; tidak menghapus Canonical Store atau jurnal. Kredensial dibaca untuk request tetapi tidak dicatat ke bukti. Jangan jalankan skenario outage secara bersamaan.

```sh
docker compose --env-file .env.example up --build -d bmkg pvmbg canonical-store message-broker aggregator notification-consumer dashboard-consumer
python3 scripts/check-p4.py
python3 scripts/check-stage-3.py --output docs/evidence/anggota-c/stage-4/fanout-downtime.json
python3 scripts/check-p5-third.py
```

| Skrip | Tujuan dan efek | Bukti |
|---|---|---|
| `check-p4.py` | Stop/build/start PVMBG saja; schema v1/v2 live; restart Aggregator/PostgreSQL tanpa menghapus volume; probe storage dari tiga peran jaringan. PVMBG dibiarkan schema v2. Client API HTTP masih menunggu B. | `stage-4/p4-check.json`, `pvmbg-rebuild.log` |
| `check-stage-3.py` | Stop/start notifikasi, replay ID sama sebelum/sesudah restart consumer, lalu stop/start broker. Layanan yang dihentikan dipulihkan, volume dipertahankan. `--output` menentukan lokasi bukti sehingga output historis tahap 3 tidak tertimpa. | Default `stage-3/consumer-check.json`; tahap 4 `stage-4/fanout-downtime.json` |
| `check-p5-third.py` | Menjalankan proses subscriber terpisah melalui `tests/compose-review.yml`; mencocokkan event baru dengan dua consumer dasar; memeriksa file/commit/container producer tetap; membersihkan container/queue sementara. | `stage-4/third-consumer.json`, `review-build.log` |
| `check-stage-2.py` | Khusus broker dengan **kedua consumer offline**; jangan dijalankan pada stack consumer aktif. | `stage-2/broker-recovery.json` |

Semua lokasi di bawah `docs/evidence/anggota-c/`. `demo_support.py` berisi helper operator (Compose, request management, SQL, copy journal), tidak dipakai aplikasi atau sebagai shared business logic antarlayanan. Probe SQL hanya untuk operator/demo; layanan consumer tetap tidak mengakses PostgreSQL.

Untuk melihat binding subscriber ketiga secara manual:

```sh
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-review.yml up --build -d --no-deps review-subscriber
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-review.yml logs -f review-subscriber
```

Buka UI RabbitMQ → Exchanges → hazard.events selama subscriber aktif; queue `amq.gen-*` memakai binding baru. Proses berhenti setelah 90 detik. Queue exclusive/non-durable/auto-delete ini hanya untuk demo, tidak menyimpan backlog lintas restart. Output stdout bukan jurnal idempotensi. Penerimaan baru berlaku setelah binding; tidak me-replay sejarah sebelum binding. Cleanup:

```sh
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-review.yml rm -s -f review-subscriber
```

Panduan screenshot, tabel bukti, serta bagian yang masih menunggu B ada di `docs/anggota-c-p4-p5-report.md`. File JSON menyimpan kejadian historis; angka queue pada UI runtime sesudah recovery bisa sudah nol. Screenshot tidak menggantikan output mentah.
