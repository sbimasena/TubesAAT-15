# Skrip Pengujian dan Demo

Urutan lengkap P1–P5 tersedia di [README utama](../README.md#urutan-pemeriksaan-penerimaan-p1p5). Jalankan pada project uji, satu skenario setiap kali, dan hentikan urutan jika checker gagal.

## Silang kredensial upstream (P3)

```sh
python3 scripts/check-cross-credentials.py --env-file .env --project-name project-uji --output /tmp/p3-upstream.json
python3 -B -m unittest discover -s scripts -p test_cross_credentials.py
```

Checker membaca konfigurasi Compose utama untuk mendapatkan kredensial dan port host efektif BMKG/PVMBG. Ia memeriksa kedua endpoint BMKG serta endpoint laporan PVMBG: kredensial sendiri harus menghasilkan 200/array JSON; kredensial sumber lain pada header native, header trust domain asing, dan request tanpa kredensial harus menghasilkan 401/403. Kedua kredensial harus nonkosong dan berbeda. Redirect tidak diikuti. Tidak memerlukan overlay operator dan tidak mengubah schema, outage, lifecycle, atau data layanan. Jalankan saat kedua sumber tersedia.

Exit code 0 berarti lulus, 1 berarti gagal. Output hanya memuat status/correlation ID; kredensial dan body tidak ditulis. Jika gagal sebelum request, periksa konfigurasi dan konektivitas lokal; jika gagal setelah request, status yang sudah diamati tetap disimpan. Tes lokal tidak memerlukan Docker dan memastikan server yang menerima kredensial asing tidak dapat lolos.

Checkpoint fondasi B memakai konfigurasi lokal dan overlay khusus: lihat [README API Klien](../services/client-api/README.md#checkpoint-fondasi). `check-member-b.py` memeriksa Auth/API Klien dan alur HTTP ke Aggregator; opsi `--check-lifecycle` menghentikan/menjalankan kembali hanya kedua service B. Ini bukan tes penerimaan P2/P3.

Pemeriksaan autentikasi memakai `check-member-b-auth.py`: login tiga identitas, scope, penolakan field mentah, rotasi/replay, serta penolakan access token lama. Opsi `--natural-expiry` menunggu TTL nyata sebelum refresh tanpa login ulang. Skrip membutuhkan Auth/API Klien/Aggregator dengan data kedua sumber; tidak mengubah lifecycle atau status sumber. Baca [panduan autentikasi](../services/client-api/README.md#pemeriksaan-autentikasi). Uji ini belum mencakup silang kredensial upstream, beban P2, atau fanout.

Pemeriksaan resiliensi memakai `check-member-b-resilience.py` pada project uji yang dipilih lewat `--project-name`. Pemeriksaan dasar mencakup proyeksi sumber aman, filter kosong 200, galat JSON, serta trace/redaksi log. `--exercise-outages` mengubah flag outage PVMBG dan menghentikan/menjalankan kembali PostgreSQL/Auth pada project tersebut, lalu memulihkan flag/layanan tanpa menghapus volume. `--burst-connections` menguji burst singkat dan mencatat 200/429 yang teramati; ini bukan beban P2 60 detik. Baca [panduan resiliensi](../services/client-api/README.md#pemeriksaan-resiliensi). Jangan jalankan bersamaan dengan demo lain yang mengubah sumber atau lifecycle.

Jalankan dari root repositori dengan Python 3 stdlib/Docker dan file environment lokal lengkap seperti panduan README utama. Checker yang memakai `demo_support.py` menerima `--env-file` (awal `.env`), `--project-name`, serta `--evidence-dir` untuk menyimpan hasil baru tanpa menimpa bukti lama. Seluruh operasi Compose memakai konfigurasi tersebut dan overlay operator. Nilai efektif dibaca dari Compose, termasuk port host dan interpolasi environment. Jangan menjalankan skenario outage bersamaan.

Untuk mengaktifkan akses operator pada project yang diperiksa:

```sh
docker compose --env-file .env -f docker-compose.yml -f tests/compose-operator.yml up --build -d --wait
python3 scripts/check-p4.py
python3 scripts/check-stage-3.py --output /tmp/fanout-downtime.json
python3 scripts/check-p5-third.py
```

| Skrip | Tujuan dan efek |
|---|---|
| `check-p4.py` | Stop/build/start PVMBG saja; schema v1/v2 live; restart Aggregator/PostgreSQL tanpa menghapus volume; probe storage dari tiga peran jaringan. PVMBG dibiarkan schema v2. Record kanonis dicocokkan melalui Client API sebagai Field Team/Internal Ops dan proyeksi Media, sebelum/sesudah restart Aggregator/PostgreSQL. Auth/API diuji stop/start mandiri; restart Auth menolak sesi lama dan membutuhkan login ulang. Log ketiga layanan memuat correlation ID/latensi yang sama. |
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
- Clone menjalankan sembilan layanan dari commit HEAD tanpa patch source, pada project/volume baru dan port 28081/28082/28084/28080/35672. Secret/password sementara dihasilkan sendiri di luar checkout. Akses data melalui Auth/API; Aggregator/DB tetap tanpa port host. Hanya resource project clone yang dihapus; container project yang sudah ada diperiksa tetap.
- Untuk menguji bootstrap yang diperbaiki pada volume broker lama dan reconnect consumer, pakai skrip yang sudah ada: `python3 scripts/check-stage-3.py --output /tmp/broker-recovery.json`.

Parameter beban, definisi latensi, dan threshold dijelaskan di [panduan pengujian](../tests/README.md). Baseline langsung Aggregator tetap terpisah dari pengukuran API publik terautentikasi di bawah.

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

Gunakan overlay operator hanya untuk checker yang membutuhkan HTTP Aggregator dari host. Checker clone menguji sembilan layanan dari HEAD dengan secret sendiri dan Compose utama. `--revision` memilih commit; `--evidence-dir` memilih lokasi hasil. `--project-name` membatasi project yang diamati untuk memastikan container lama tetap; project clone selalu dibuat terpisah. Ia memakai checker deployment workspace untuk memeriksa aplikasi dari commit, tanpa menyalin perubahan aplikasi ke checkout. Pengujian ulang diperlukan setelah commit perubahan aplikasi/config yang memengaruhi hasil.

## Beban Client API terautentikasi

```sh
python3 scripts/check-client-p2.py --env-file .env --project-name project-uji --evidence-dir /tmp/client-p2
```

Memerlukan sembilan layanan healthy, k6/lsof native, serta access TTL 60 detik. K6 menyiapkan 50 sesi Field Team sebelum pengukuran; setiap VU memegang pasangan token sendiri dan me-refresh sebelum expiry, tanpa login tiap query. Dua run berurutan masing-masing memakai 50 VU selama 60 detik: PVMBG delay 3000 ms, kemudian PVMBG outage sementara query BMKG berlanjut. Tiga sampel socket menghitung koneksi TCP proses k6 ke port Client API. Login/refresh terpisah dari metrik query; refresh failure tetap menggagalkan hasil.

Respons 200 harus berisi data kanonis BMKG. Respons 429 harus memiliki kode `concurrency_limit`, correlation ID, dan `Retry-After: 1`; 429 yang memenuhi kontrak dilaporkan terpisah dari error tidak terkontrol. p95 request lengkap dan p95 respons 200 harus <300 ms, error tidak terkontrol <1%, dan setiap run harus mencatat sedikitnya 50 refresh berhasil serta query 200. Rate 429 tidak diberi batas tambahan; jumlah 200/rate layanan tetap dicatat.

Checker mencatat panggilan upstream lambat/503, last-known PVMBG dan metadata stale selama outage, progres ingestion BMKG, serta report PVMBG baru setelah recovery. Schema/delay/outage asli dibaca dari health PVMBG dan dipulihkan pada `finally`, termasuk saat threshold gagal. Layanan inti tidak direstart. Jangan menjalankan pemeriksaan lifecycle bersamaan. Hasil gagal dan hasil pemulihan tetap disimpan pada lokasi bukti.

## Log progres pengujian

Semua `check-*.py` menampilkan tahap pemeriksaan dan waktu berjalan ke **stderr**, langsung tanpa buffering. Polling panjang mencetak heartbeat kira-kira setiap 10 detik; P2 mencetak checkpoint koneksi/data pada detik 15, 30, dan 45 serta ringkasan metrik tiap run. Operasi build/start yang outputnya ditangkap menampilkan tahap sebelum operasi; detailnya tetap tersedia di file log atau Docker. Token, password, kredensial request, dan payload tidak dicetak oleh log progres.

Command sebelumnya tetap berlaku. Gunakan nama project Docker yang benar, lihat `docker compose ls`; nama contoh `project-uji` harus diganti dengan project yang ingin diperiksa. Contoh untuk stack `tubesaat-15`:

```sh
python3 scripts/check-client-p2.py --env-file .env --project-name tubesaat-15 --evidence-dir /tmp/p2-check
```

Untuk menyimpan progres deployment sambil mempertahankan JSON stdout:

```sh
python3 scripts/check-deployment.py --env-file .env --project-name tubesaat-15 \
  --output /tmp/deployment-check.json 2>/tmp/deployment-progress.log
tail -f /tmp/deployment-progress.log
```

`tail -f` dijalankan di terminal lain. Untuk melihat sekaligus menyimpan output gabungan checker:

```sh
set -o pipefail
python3 scripts/check-client-p2.py --env-file .env --project-name tubesaat-15 \
  --evidence-dir /tmp/p2-check 2>&1 | tee /tmp/p2-progress.log
```

File gabungan ini adalah log teks; hasil terstruktur P2 tetap di `/tmp/p2-check/p2-client-check.json`. Jangan menjalankan checker outage/lifecycle bersamaan pada project yang sama. Progres baru berlaku untuk proses yang dimulai setelah skrip diperbarui.
