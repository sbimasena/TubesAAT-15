# Audit laporan dan kurasi repository — 10 Oktober 2026

Sumber laporan: [Google Docs FGMKDistributed](https://docs.google.com/document/d/1NNjBYWGHbg5uj43golkOZAjVn8enGM48l_zM3MpRTuQ/edit?tab=t.0), diambil ulang pada 10 Oktober 2026 pukul 15.27 WIB. Ekspor terbaru berisi 59 halaman. [Formulir AI yang ditautkan](https://drive.google.com/file/d/1oaCXK7Of5Qs6OwD_hJuSkhWOe9-LBAO-/view?usp=sharing) juga diperiksa, termasuk tampilan isian dan tanda tangan.

Pembanding: `context.md` lokal, 151 berkas tracked pada commit `78d1e85`, kode/Compose, import dan pemanggilan antarskrip, README, serta bukti pengujian. Dokumen spesifikasi tugas asli belum tersedia sebagai sumber terpisah. File konteks, pedoman penulisan, dan snapshot laporan lama masih untracked; keberadaannya di workspace tidak berarti sudah disertakan di GitHub.

Hasil penelaahan ini diikuti pembersihan 12 berkas pada bagian 2.1, sesuai permintaan pengguna. Rujukan README dan dependensi checker resiliensi ikut diperbarui. Nama berkas dan direktori tidak diubah; perubahan API legacy serta usulan rename tetap ditunda. Perintah pengganti laporan memakai nama yang tersedia sekarang. Google Docs belum disunting; koreksi Bab 6 pada R2 tetap perlu diterapkan pada laporan.

## 1. Hasil pemeriksaan ulang

Nama sistem sudah menjadi **FGMKDistributed**. Petunjuk pengisian Bab 1 dan placeholder Bab 6 sudah dihapus. Bab “Penggunaan AI” yang sebelumnya berupa template sudah dihapus; lampiran sekarang menjadi **Bab 9**, dengan tautan formulir AI. Formulir sudah diisi dan ditandatangani, sehingga temuan lama “formulir belum tersedia” tidak berlaku lagi.

Mekanisme utama P1–P5 tetap sesuai konteks dan kode: dua sumber mandiri, tujuh layanan aplikasi ditambah PostgreSQL/RabbitMQ, polling terpisah, model kanonis 11 field, storage milik Aggregator, JSONB, autentikasi/scope/rotasi token, outbox, dua subscription durable, idempotensi dan subscriber ketiga.

Masalah utama yang tersisa adalah ketidaksesuaian bukti dengan narasi/prosedur serta campuran artefak pengembangan dengan alur penerimaan final. Menghapus seluruh file yang tidak disebut dalam laporan akan ikut membuang tes regresi dan alat reproduksi yang masih diperlukan.

## 2. Kurasi berkas

### 2.1 Berkas yang dihapus pada pembersihan

| Berkas | Alasan penghapusan | Rujukan/dependensi yang diperiksa |
|---|---|---|
| `scripts/render-p1-evidence.py` | Hapus renderer P1 HTML yang sudah digantikan Freeze. | Ganti perintah Bab 6 dan hapus rujukan renderer ini pada README/bukti. |
| `docs/evidence/member-a/p1/p1-01-bmkg-tsunami.html` | Hapus tampilan HTML lama; Gambar 1 memakai PNG terminal. | Raw JSON dan PNG terminal tetap disimpan. |
| `docs/evidence/member-a/p1/p1-02-pvmbg-schema-evolution.html` | Hapus tampilan HTML lama; Gambar 2 memakai PNG terminal. | Sama. |
| `docs/evidence/member-a/p1/p1-03-runtime-verification.html` | Hapus tampilan HTML lama; Gambar 3 memakai PNG terminal. | Sama. |
| `docs/evidence/member-a/p1/html-archive/p1-01-bmkg-tsunami.png` | Hapus gambar versi HTML yang tidak dipakai laporan terbaru. | Versi terdahulu tetap dapat ditemukan melalui riwayat Git. |
| `docs/evidence/member-a/p1/html-archive/p1-02-pvmbg-schema-evolution.png` | Hapus gambar versi HTML yang tidak dipakai. | Sama. |
| `docs/evidence/member-a/p1/html-archive/p1-03-runtime-verification.png` | Hapus gambar versi HTML yang tidak dipakai. | Sama. |
| `docs/evidence/member-a/p1/p1-ingestion-run-terminal.png` | PNG sekitar 3,10 MB ini tidak dipakai dalam laporan terbaru, sehingga dihapus. | Pertahankan `ingestion-run.log`; gambar dapat dibuat ulang dengan Freeze. |
| `scripts/check-member-b.py` | Hapus checker fondasi/checkpoint yang membutuhkan alias provisional. Health, ownership, protected reads dan lifecycle sudah tercakup pada deployment/P4 serta checker Auth. | Hapus alur checkpoint terkait dari README; pastikan acceptance checker tetap meliputi perilaku final. |
| `scripts/check-p2.py` lama | Hapus baseline langsung Aggregator dari paket penerimaan final. Skrip ini sendiri menandai `AUTH_CLIENT_API_NOT_CHECKED` dan `PENDING B`. | `check-client-p2.py` tetap menjadi pengujian P2 utama. Versi sebelumnya tersedia pada riwayat Git jika diperlukan untuk diagnosis. |
| `tests/load-p2.js` | Hapus workload baseline yang hanya dipanggil `check-p2.py` lama. | Pertahankan workload API publik `load-client-p2.js` dan tes redaksi summary. |
| `tests/compose-member-b.yml` | Hapus overlay kompatibilitas alias checkpoint. | Dependensi pada `check-member-b-resilience.py` telah dilepas. Compose efektif sebelum/sesudah penghapusan identik pada environment lokal yang diperiksa. |

Delapan berkas pertama adalah artefak rendering yang tergantikan/tidak dipakai; empat terakhir berkaitan dengan alur checkpoint atau baseline yang sudah terpisah dari penerimaan final. Seluruh 12 berkas di tabel telah dihapus dari workspace. Rujukan aktif README ikut dihapus dan checker resiliensi kini memakai Compose utama. Riwayat Git tetap menyimpan versi sebelumnya; JSON/log dan tiga PNG terminal P1 tetap dipertahankan.

### 2.2 Dua keputusan yang bergantung pada versi laporan final

| Berkas | Status aktual | Rekomendasi |
|---|---|---|
| `docs/diagrams/bab-3-arsitektur-sistem.png` dan `.svg` | Diagram overview yang dibuat kemudian belum dipakai di hlm. 3. Laporan masih memakai diagram berwarna versi sebelumnya, yang juga sesuai arsitektur. | Pilih satu versi: pakai versi repo di laporan, atau simpan diagram yang benar-benar dipakai dan keluarkan alternatif ini. Renderer perlu disesuaikan jika satu diagram dikeluarkan. |
| `docs/report/member-a-additions.md` | Panduan penyuntingan berdasarkan snapshot lama; sebagian isi sudah masuk laporan dan nomor halamannya lama. | Pertahankan selama penulisan. Sesudah semua isi dipindahkan dan naskah final tersimpan, keluarkan panduan draf dari paket final atau ringkas menjadi dokumentasi teknis yang masih berlaku. |

`docs/diagrams/bab-3-alur-internal-aggregator.png` **sudah dipakai** pada hlm. 4; SVG-nya adalah sumber yang dapat diedit. Keduanya layak dipertahankan. `render-report-diagrams.py` juga masih berguna untuk mereproduksi gambar yang dipakai dan dapat dipindahkan ke alat dokumentasi. SVG bukan file sampah hanya karena laporan menyisipkan PNG.

### 2.3 Nama dan lokasi yang perlu disatukan

Gunakan fungsi/skema penerimaan sebagai dasar nama. Nama anggota tetap cocok pada tabel kontribusi, bukan pada entry point demo final. Usulan berikut belum diterapkan:

| Nama sekarang | Nama yang disarankan | Fungsi |
|---|---|---|
| `check-member-b-auth.py` | `check-auth.py` | P3: tiga identitas, scope, expiry alami, refresh dan replay. |
| `check-member-b-resilience.py` | `check-resilience.py` | P2/P3: galat, burst 429, outage dependency dan recovery. |
| `check-client-p2.py` | `check-load.py` | P2: sustained load API publik terautentikasi; menghindari benturan dengan baseline lama. |
| `check-p1-report.py` | `check-data-mapping.py` | P1: pencocokan payload, confidence, migrasi/container dan fanout; memanggil checker ingestion. |
| `check-stage-2.py` | `check-broker-recovery.py` | P5: pending outbox dan backlog durable ketika broker offline. |
| `check-stage-3.py` | `check-fanout.py` | P5: dua consumer, downtime independen, dedup dan reconnect. |
| `check-p5-third.py` | `check-third-subscriber.py` | P5: subscription baru tanpa perubahan producer. |
| `check-p4.py` | `check-service-independence.py` | P4: rebuild/restart, persistence, ownership dan akses tiga peran. |
| `docs/evidence/member-a/p1/` | `docs/evidence/p1/run-20261009-124119/` | Bukti disusun menurut problem dan run; pembagian kerja dicatat pada kontribusi. |
| Default output `docs/evidence/anggota-c/stage-*` | Output lokal `artifacts/<skenario>/<run>/` | Output otomatis dipisahkan dari bukti terpilih yang di-commit. Tambahkan `artifacts/` pada `.gitignore` saat perubahan ini diterapkan. |
| `scripts/render-report-diagrams.py`, `scripts/show-p1-evidence.py` | `scripts/report/` atau direktori alat dokumentasi lain | Alat gambar/penyajian tidak bercampur dengan checker penerimaan. Perbarui path input default dan petunjuk pemanggilan. |

Tidak perlu wrapper dengan nama anggota lama setelah rename: wrapper akan menambah jumlah file dan mempertahankan dua cara menjalankan hal yang sama. Ubah rujukan README, skrip pemanggil, tests, Bab 6/8, dan manifest bukti dalam perubahan yang sama. Teks pada screenshot historis tetap mencerminkan perintah run lama; jangan menyunting nama di screenshot supaya terlihat berasal dari run baru.

### 2.4 Berkas yang tetap diperlukan

| Kelompok | Berkas | Alasan |
|---|---|---|
| Aplikasi | Tujuh direktori `services/`, Dockerfile, `go.mod`, dan `go.sum` yang diperlukan | Build dan deployment tiap layanan harus mandiri. Modul mock yang hanya memakai stdlib memang tidak memerlukan `go.sum`. |
| Workspace lokal | `go.work`, `go.work.sum` | Mendukung pengerjaan monorepo; tetap dilarang memakai business logic layanan lain. |
| Infrastruktur | `docker-compose.yml`, `.env.example`, topology/bootstrap RabbitMQ, migrasi PostgreSQL | Konfigurasi startup, ownership, kredensial eksternal dan persistence. |
| Unit/regression test | Seluruh 25 file `*_test.go`, `scripts/test_demo_support.py`, `scripts/test_cross_credentials.py`, `tests/check-load-summary.js` | Menguji retry/cursor, freshness, scope, rotasi, rollback/JSONB, confirm, persist-before-ack, dedup, dan pencegahan kebocoran token. Tidak disebut dalam PDF bukan alasan menghapusnya. Kasus yang khusus alias provisional dapat dihapus/disesuaikan bersama penghapusan alias; pertahankan tes perilaku endpoint final. |
| Alat checker bersama | `demo_support.py`, `checker_progress.py` | Banyak checker mengimpor keduanya; helper operator ini bukan business logic bersama antarlayanan. Pencarian nama file saja dapat melewatkan import Python tanpa `.py`. |
| P1 | `check-ingestion.py`, checker mapping, `report-check.json`, `ingestion-check.json`, `ingestion-run.log`, PNG Gambar 1–3 | Checker mapping memanggil checker ingestion. JSON/log adalah sumber angka dan trace; PNG menyajikannya pada lampiran. |
| P2/P3 | Checker load, Auth, resiliensi, silang kredensial; `load-client-p2.js` | Beban, burst, autentikasi dan trust domain menguji kriteria berbeda. |
| P4/P5/final | Checker independence, fanout, broker recovery, subscriber ketiga, deployment dan clean-clone | Dibutuhkan untuk acceptance dan reproduksi bukti. |
| Overlay khusus | `compose-operator.yml`, `compose-load.yml`, `compose-storage.yml`, `compose-consumers.yml`, `compose-probes.yml`, `compose-review.yml` | Masing-masing memiliki fungsi berbeda: akses operator, delay P2, tes DB/broker, tes jurnal, probe isolasi, subscriber ketiga. Jangan gabungkan overlay operator ke deployment default. |
| Subscriber demo | `services/dashboard-consumer/cmd/review/main.go` | Dipakai overlay review untuk Gambar 26. Lokasinya dalam modul dashboard tidak berarti proses ini consumer dasar yang sama; dijalankan pada container/queue tersendiri. |
| Dokumen | README utama dan README layanan/infrastruktur | Berisi kontrak dan cara menjalankan layanan. Kurangi duplikasi prosedur: README utama menjadi pintu masuk penerimaan, `tests/README.md` untuk tes unit/integrasi, `scripts/README.md` untuk opsi/efek checker. |

`check-stage-2.py` bukan duplikasi penuh `check-stage-3.py`. Stage 2 memeriksa pending outbox, query tetap 200, dan publikasi setelah broker pulih, dengan kedua consumer offline serta backlog awal tersedia. Stage 3 memeriksa consumer yang aktif, replay/dedup, downtime satu consumer dan reconnect. Kriteria tambahan stage 2 sesuai Gambar 27; penamaannya yang tertinggal, bukan kegunaannya.

### 2.5 Kode kompatibilitas yang dapat dikeluarkan

Endpoint `GET /internal/provisional/hazards` nonaktif secara default dan hanya digunakan checkpoint lama. Jika alur final cukup memakai `/hazards`, hapus secara terkoordinasi:

- Flag `ENABLE_PROVISIONAL_HAZARD_ENDPOINT` pada `.env.example` dan parsing `EnableProvisionalHazards` pada config Client API.
- Parameter flag pada `NewHandler`, wiring pada `cmd/server/main.go`, dan route/header provisional pada `internal/httpapi/server.go`.
- Kasus uji yang hanya melindungi alias; uji Auth, proyeksi, error mapping dan limiter pada `/hazards` tetap dipertahankan.
- Overlay/checker checkpoint dan bagian README yang hanya menjelaskan checkpoint.

Ini perubahan kode, sehingga perlu menjalankan tes Client API/Auth dan checker yang terpengaruh. Jangan menghapus file test HTTP/resilience seluruhnya untuk membuang beberapa kasus legacy.

### 2.6 File lokal yang bukan bagian repository Git saat ini

`IF4031_M1_FithraGaMasukKelas.md`, `.pdf`, `context.md`, `system-architecture.md`, `writing.md`, dan `.agents/` masih untracked. Snapshot laporan lokal sudah berbeda dari Google Docs terbaru. Jangan memublikasikan snapshot lama sebagai laporan final. Pedoman agent/penulisan pribadi dapat tetap lokal. Bila konteks atau arsitektur dijadikan dokumentasi kelompok, mutakhirkan isinya terlebih dahulu; `context.md` §8.2/18/20 masih memuat keputusan/status perencanaan yang sudah terlewati.

## 3. Susunan paket final yang disarankan

```text
README.md                         startup dan tabel demo P1–P5
.env.example
.gitignore
docker-compose.yml
go.work / go.work.sum
services/                         tujuh modul/build mandiri dan tesnya
infrastructure/                   store, broker, topology/bootstrap
scripts/
  check-deployment.py
  check-clean-clone.py
  check-ingestion.py
  check-data-mapping.py
  check-load.py
  check-auth.py
  check-cross-credentials.py
  check-resilience.py
  check-service-independence.py
  check-fanout.py
  check-broker-recovery.py
  check-third-subscriber.py
  demo_support.py / checker_progress.py
  test_*.py
  report/                         alat diagram dan tampilan bukti tersimpan
tests/
  README.md                       unit, integrasi dan efek overlay
  compose-{operator,load,storage,consumers,probes,review}.yml
  load-client-p2.js
  check-load-summary.js
docs/
  diagrams/                       hanya diagram final + sumber yang dipakai
  evidence/
    README.md                     Gambar → berkas → run → commit → perintah
    p1/ ... p5/                   JSON/log + gambar terpilih, per run
  report/                         naskah final bila disimpan; draf dapat dikeluarkan
artifacts/                        hasil sementara, diabaikan Git
```

Nama dan susunan ini adalah keputusan untuk memperjelas proyek, bukan format wajib universal. Organisasi `cmd/`, `internal/`, dan tes dalam modul sesuai [panduan Go](https://go.dev/doc/modules/layout). Overlay kecil tetap wajar: [Docker Compose](https://docs.docker.com/compose/how-tos/multiple-compose-files/merge/) mendukung konfigurasi gabungan; urutan overlay dan resolusi path relatif terhadap file dasar harus dipertahankan.

Manifest bukti baru cukup satu indeks bersama. Untuk setiap Gambar 1–27, catat problem, path, raw JSON/log, tanggal/run, commit aplikasi, konfigurasi, dan alat rendering. Bukti mentah P2–P5 yang sesuai screenshot belum ditemukan pada repo/workspace; dapat diperoleh dari anggota pemilik run. Jangan menyusun JSON baru dari screenshot lalu menyebutnya hasil run asli. Jika raw evidence tidak tersedia, ambil ulang pengujian dan sesuaikan seluruh angka/ID/gambar terkait.

## 4. Koreksi laporan: lokasi dan isi pengganti

Nomor halaman mengikuti PDF terbaru. Bab lampiran sekarang **Bab 9**. Perintah di bawah memakai nama file saat ini, sebelum usulan rename diterapkan.

### R1. Bab 7.2, tabel “Tidak ada resource exhaustion”, hlm. 23

Ganti isian yang menyebut 194 HTTP 200 dan 62 HTTP 429 dengan:

> Pada sustained load tidak ditemukan respons HTTP 429 maupun error tidak terkontrol. Pada pengujian burst 256 request, sistem menghasilkan 224 respons HTTP 200 dan 32 respons HTTP 429. Respons 429 merupakan penolakan terkontrol oleh concurrency limiter dan menyertakan header Retry-After sesuai kontrak API.

Angka 224/32 terbaca pada Gambar 8–9, hlm. 42–43. Hasil ini menunjukkan perilaku yang diamati pada beban tersebut; jangan memperluasnya menjadi klaim kapasitas tak terbatas atau pengukuran CPU/memori yang tidak dilakukan.

### R2. Bab 6, P1, hlm. 14: ganti renderer dan penjelasannya

Hapus perintah `python3 -B scripts/render-p1-evidence.py`. Ganti dengan perintah Freeze untuk berkas bukti tersimpan:

```sh
freeze --execute 'python3 -B scripts/show-p1-evidence.py bmkg' \
  --config base --window --font.size 20 --margin 0 --padding 24 \
  --border.radius 0 --shadow.blur 0 --shadow.x 0 --shadow.y 0 \
  --output docs/evidence/member-a/p1/p1-01-bmkg-tsunami.png
freeze --execute 'python3 -B scripts/show-p1-evidence.py pvmbg' \
  --config base --window --font.size 20 --margin 0 --padding 24 \
  --border.radius 0 --shadow.blur 0 --shadow.x 0 --shadow.y 0 \
  --output docs/evidence/member-a/p1/p1-02-pvmbg-schema-evolution.png
freeze --execute 'python3 -B scripts/show-p1-evidence.py runtime' \
  --config base --window --font.size 20 --margin 0 --padding 24 \
  --border.radius 0 --shadow.blur 0 --shadow.x 0 --shadow.y 0 \
  --output docs/evidence/member-a/p1/p1-03-runtime-verification.png
```

Ganti kalimat tentang kebutuhan Chrome/HTML dengan:

> Gambar 1–3 dibuat menggunakan Freeze dari keluaran terminal scripts/show-p1-evidence.py. Skrip tersebut membaca hasil pengujian yang tersimpan pada report-check.json; perintah rendering tidak menjalankan ulang ingestion. JSON dan log checker disimpan bersama gambar agar payload, timestamp, state container, dan trace dapat ditelusuri.

Output checker untuk run baru sebaiknya disimpan di `/tmp/tubesaat-p1-new-evidence/report-check.json` atau lokasi run tersendiri, lalu renderer diberi `--input` yang sama. Jangan menimpa raw evidence run lama sementara narasi masih memakai confidence/message ID run lama.

### R3. Bab 6, hlm. 13–16: samakan konfigurasi

Ganti paragraf P1 yang berakhir “agar tidak berbenturan dengan demo lain” dengan:

> Pengujian dilakukan secara berurutan pada project Compose tersendiri dengan satu berkas konfigurasi lokal yang telah dilengkapi kredensial. Nama project memisahkan container dan volume, sedangkan port host dipilih secara eksplisit agar tidak berbenturan dengan stack lain. Overlay operator membuka API Aggregator pada localhost untuk checker yang membutuhkannya. Semua perintah pada satu rangkaian menggunakan berkas environment dan nama project yang sama.

Contoh persiapan untuk ditambahkan setelah pengisian `.env`:

```sh
cp .env .env.acceptance.local
# Pilih port host yang bebas sebelum startup.
# BMKG_PORT, PVMBG_PORT, AUTH_PORT, CLIENT_API_PORT,
# AGGREGATOR_PORT, RABBITMQ_MANAGEMENT_PORT.
# Konfigurasi pengujian P1:
# BMKG_GENERATE_INTERVAL_SECONDS=10
# PVMBG_GENERATE_INTERVAL_SECONDS=10
# POLL_INTERVAL_SECONDS=3
# PVMBG_DELAY_MS=750
export ACCEPTANCE_ENV=.env.acceptance.local
export ACCEPTANCE_PROJECT=tubesaat-acceptance
export ACCEPTANCE_RESULTS="$(mktemp -d /tmp/tubesaat-acceptance.XXXXXX)"
docker compose --env-file "$ACCEPTANCE_ENV" --project-name "$ACCEPTANCE_PROJECT" \
  up --build -d --wait
```

Pada semua perintah checker, ganti path env dengan `"$ACCEPTANCE_ENV"`; pada checker yang mendukung nama project, pakai `--project-name "$ACCEPTANCE_PROJECT"`. Checker Auth memakai port pada env dan tidak memiliki opsi project. Ganti output/evidence-dir menjadi subdirektori `"$ACCEPTANCE_RESULTS"`. Aktifkan overlay operator pada project yang sama sebelum checker yang membutuhkannya, mengikuti urutan README. Jalankan checker satu per satu karena beberapa mengubah schema/outage atau lifecycle.

Tambahkan `lsof` native sebagai prasyarat bukti koneksi P2 dan Freeze sebagai prasyarat gambar terminal. Chrome diperlukan untuk rendering diagram, bukan untuk screenshot P1 terminal. Lengkapi contoh `POST /login` dengan URL Auth, metode, JSON client_id/password lokal; jangan sertakan kredensial nyata dalam laporan.

### R4. Bab 7.2, mekanisme galat, hlm. 20

Ganti bagian yang menggeneralisasi “Auth Service atau Aggregator yang unavailable menghasilkan 503” dengan:

> Kegagalan Auth Service saat validasi menghasilkan HTTP 503. Respons HTTP 503 dari Aggregator diteruskan sebagai 503, timeout permintaan Aggregator menghasilkan 504, sedangkan kegagalan transport atau respons Aggregator yang tidak valid menghasilkan 502. Respons galat menyertakan kode yang stabil dan correlation ID tanpa meneruskan rincian internal dependency kepada klien.

Rujukan implementasi: `services/client-api/internal/httpapi/errors.go` dan middleware Auth.

### R5. Bab 7.2, setelah uraian dua poller/timeout pada hlm. 19

Tambahkan:

> Selain dua poller dengan siklus terpisah, akses PostgreSQL dibatasi melalui pool berkapasitas maksimum delapan koneksi. Koneksi dan operasi repository memiliki timeout tiga detik. Pembatasan ini melengkapi concurrency limiter pada Client-Facing API dan mencegah penambahan koneksi database tanpa batas ketika permintaan meningkat. Kedua poller dan jalur query tetap berbagi storage sehingga keterbatasan database dapat memengaruhi keduanya.

### R6. Bab 7.2, sebelum tabel hasil pada hlm. 22

Tambahkan:

> Workload mengirim GET /hazards?source=BMKG&hazard_type=SEISMIC&limit=100 melalui Client-Facing API. Sebanyak 50 virtual user menggunakan sesi Tim Lapangan yang berbeda selama 60 detik pada masing-masing kondisi PVMBG lambat dan outage. Access token tetap memiliki TTL 60 detik dan diperbarui secara otomatis. Koneksi TCP aktif diperiksa pada detik ke-15, ke-30, dan ke-45. Latensi yang dilaporkan adalah waktu permintaan query data tersimpan melalui API, termasuk jalur autentikasi dan pembacaan Aggregator.

Setelah tabel hasil, tambahkan:

> Throughput dihitung dari jumlah query berhasil dibagi durasi pengukuran aktual yang dicatat checker. Hasil pengukuran berlaku pada konfigurasi mesin dan workload saat pengujian. Metrik query dibedakan dari login serta refresh agar waktu persiapan sesi tidak ditafsirkan sebagai latensi query.

Masih perlu diisi dari run asli: OS/CPU/RAM dan alokasi Docker, versi Docker/k6, commit aplikasi/config, waktu run, jumlah refresh sukses/gagal, serta tautan `p2-client-check.json`, summary dan log. Jangan menggunakan versi alat yang terpasang sekarang sebagai versi historis tanpa bukti. README mencatat p95 203/221 ms pada 7 Oktober, sedangkan laporan mencatat 70/88 ms pada run lain; bedakan tanggal/config agar keduanya tidak dipahami sebagai hasil run yang sama.

### R7. Bab 7.3, “Batasan yang disadari”, hlm. 24

Tambahkan setelah penjelasan session in-memory:

> Sesi dan refresh token memiliki batas waktu default 3.600 detik sejak login. Rotasi token menaikkan generation dan mengganti pasangan token, tetapi tidak memperpanjang batas waktu sesi tersebut. Setelah sesi berakhir atau Auth Service restart, klien perlu melakukan login kembali.

Waktu tunggu expiry 59,831 detik pada hlm. 26 sudah cocok dengan screenshot dan TTL access 60 detik; tidak perlu dikoreksi lagi.

### R8. Bab 7.4, setelah paragraf migrasi/JSONB, hlm. 27

Tambahkan paragraf berikut dan diagram skema:

> Canonical Store memuat hazard_events untuk sebelas field kanonis beserta nomor revision internal, hazard_outbox untuk snapshot event yang menunggu publikasi, dan schema_migrations untuk pencatatan migrasi. Constraint unik pasangan source dan source_ref_id menjaga satu identitas hazard per rekaman sumber. Setiap row outbox mengacu pada hazard dan memiliki pasangan hazard_id serta hazard_revision yang unik. Revision dan metadata outbox merupakan detail persistence, sehingga tidak menambah field kanonis yang diterima klien.

```text
hazard_events (1) ------------------------- (N) hazard_outbox
hazard_id TEXT PK <------------------------ hazard_id TEXT FK
source TEXT                                message_id BIGINT IDENTITY PK
source_ref_id TEXT                         hazard_revision BIGINT
hazard_type TEXT                           payload JSONB
severity TEXT                              correlation_id TEXT
area_name TEXT                             created_at TIMESTAMPTZ
latitude DOUBLE PRECISION                  published_at TIMESTAMPTZ NULL
longitude DOUBLE PRECISION                 UNIQUE(hazard_id, hazard_revision)
occurred_at TIMESTAMPTZ
ingested_at TIMESTAMPTZ
attributes JSONB
revision BIGINT
UNIQUE(source, source_ref_id)

schema_migrations: version, applied_at
```

Tambahkan indeks waktu/filter sumber/jenis hazard dan indeks outbox pending pada tabel skema bila dibutuhkan. Diagram ini menggambarkan migrasi yang sudah ada; tidak meminta perubahan database.

### R9. Bab 7.5, tabel bukti hlm. 32, serta Bab 6 P5

Tambahkan baris tabel:

| Kriteria | Bukti dan rujukan |
|---|---|
| Broker outage dan pemulihan publikasi | Gambar 27 menunjukkan satu event baru tersimpan sebagai outbox pending ketika broker offline, query tetap HTTP 200, kemudian published_at terisi setelah broker pulih dengan ID Aggregator tetap. Reproduksi: scripts/check-stage-2.py; kode: repository outbox dan worker publisher Aggregator. |

Tambahkan penjelasan pada Bab 6 P5:

> Pemeriksaan broker recovery dilakukan terpisah dengan kedua consumer dasar offline dan backlog awal yang sudah tersedia. Checker memeriksa ingestion serta outbox ketika broker berhenti dan publikasi setelah broker kembali, kemudian consumer dijalankan kembali untuk skenario fanout. Skenario ini melengkapi pemeriksaan downtime satu consumer dan subscriber ketiga.

Skrip stage 2 mensyaratkan kedua queue memiliki pesan awal; jangan menyisipkan perintahnya pada stack consumer aktif tanpa persiapan tersebut. Usulan nama finalnya adalah `check-broker-recovery.py`.

### R10. Bab 7.4 / Gambar 22, hlm. 53; milestone final

Gambar 22 mencatat clean-clone PASS pada commit `6beb351e6bd7b63b145e56a451a04d32564c83fc`. Commit ini benar-benar tersedia dalam riwayat, tetapi bukan HEAD `78d1e85`. Perbandingan kode menunjukkan perubahan Client API setelahnya, termasuk reuse koneksi dan decode envelope. Karena itu gambar belum membuktikan clean-clone untuk versi akhir.

Sebelum run ulang, tambahkan kalimat yang membatasi klaim:

> Bukti clean-clone pada Gambar 22 berasal dari commit 6beb351. Hasil tersebut menunjukkan startup dan alur integrasi pada checkout yang diuji; hasil untuk revisi final perlu ditautkan pada commit yang sama dengan kode penyerahan.

Setelah kurasi/config selesai dan perubahan di-commit, jalankan checker clean-clone pada revisi final dan ganti gambar serta rujukan commit berdasarkan hasil nyata. Tag `milestone-1` belum tersedia pada remote saat pemeriksaan; konteks mensyaratkan tag dibuat setelah clean-clone lulus. Jangan memakai tag atau hash baru untuk menamai bukti run lama.

### R11. Bab 9 Lampiran A, hlm. 34, dan formulir AI

Formulir sudah diisi dan ditandatangani, tetapi **kode mata kuliah tertulis IF4013**, sedangkan sampul/report/konteks memakai IF4031. Koreksi isian menjadi **IF4031** dan perbarui dokumen finalnya. Tingkat penggunaan sudah diisi pada beberapa lingkup; nama alat aktual dan bantuan coding belum dijelaskan secara khusus. Isi rincian yang benar-benar digunakan kelompok pada bagian “Lainnya, Jelaskan”.

Tambahkan sebelum tautan formulir, untuk bagian Member A:

> Pada bagian Member A, Codex digunakan untuk menelaah implementasi mock dan Aggregator serta menyusun draf uraian arsitektur, pemetaan, polling, freshness, dan autentikasi upstream. Bantuan juga mencakup skrip pengumpulan bukti P1, kode diagram SVG, dan perintah penyajian hasil terminal. Bukti berasal dari pengujian sistem lokal dan disimpan sebagai JSON serta log; Freeze digunakan untuk merender keluaran terminal menjadi gambar.

Penggunaan AI anggota lain dan verifikasi penulis perlu diisi menurut kegiatan sebenarnya. Tautan PDF dapat dibuka pada audit ini, tetapi formulir berada di luar PDF laporan. Jika ketentuan pengumpulan meminta formulir dilampirkan dalam satu PDF, sertakan halamannya secara langsung; ketersediaan link saja tidak membuktikan pemenuhan aturan bundling yang tidak tersedia dalam audit.

### R12. Bab 3.1 / Bab 6: batas health dan akses operator

Tambahkan setelah penjelasan healthcheck:

> Healthcheck memeriksa kondisi layanan sesuai kontraknya. PVMBG tetap dapat memberikan health 200 ketika endpoint data sedang outage, sedangkan health Aggregator memeriksa konektivitas database dan tidak menjamin semua sumber fresh atau seluruh outbox sudah dipublikasikan. Keberhasilan ingestion diperiksa melalui metadata sumber dan bukti commit. Overlay operator membuka endpoint internal penuh pada localhost tanpa JWT untuk inspeksi tepercaya; klien downstream tetap mengakses Client-Facing API yang memeriksa identitas serta scope.

Pada inventaris Auth Service, tambahkan port container **8084** dan URL **http://auth:8084**; port host dapat diubah melalui konfigurasi lokal.

### R13. Bab 3, Bab 8, dan Lampiran: penyajian

- Bab 3 hlm. 3–4: beri caption `Diagram 3.1. Arsitektur FGMKDistributed` dan `Diagram 3.2. Alur internal Aggregator`. Pilih diagram overview yang sama pada repo dan laporan.
- Gambar 1: ganti caption menjadi `Pemetaan SeismicEvent dan korelasi TsunamiWarning BMKG menjadi HazardEvent kanonis`.
- Gambar 2: ganti caption menjadi `Pemetaan PVMBG v1/v2 dengan confidence_level dipertahankan pada atribut kanonis`.
- Gambar 3: ganti caption menjadi `Verifikasi perubahan skema tanpa restart atau migrasi tambahan, beserta trace dan penerimaan dua consumer`.
- Bab 6/8 dan Gambar 8–9/14: nama skrip historis berbeda. Setelah rename final, rujukan repo diperbarui; asal run/commit tetap dinyatakan dengan jujur.
- Bab 8 hlm. 33 dan Bab 7.4 hlm. 29: hyperlink otomatis salah menuju `http://load-client-p2.js`, `http://check-p4.py`, dan `http://check-member-b-resilience.py`. Ganti dengan URL GitHub berkas yang benar atau teks monospace tanpa hyperlink.
- Bab 9 lampiran hlm. 54: ganti `B.45Problem 5` menjadi `B.5 Problem 5`; satukan judul dengan gambar pertama agar tidak menjadi halaman hampir kosong.
- Bab 5 hlm. 12: koreksi “logitude nya” menjadi “longitude”. Terapkan font monospace pada path/field dan hapus backtick Markdown yang tampil literal.
- Tindak lanjuti komentar tentang load testing dan skema database; komentar tidak menggantikan pembahasan yang perlu ditambahkan.

## 5. Urutan implementasi kurasi

1. Tetapkan satu inventaris entry point P1–P5 dan satu versi diagram overview.
2. Hapus artefak HTML tergantikan dan checkpoint/baseline, lalu perbarui dependensi serta README.
3. Rename checker berdasarkan fungsi, rapikan output sementara dan direktori bukti, serta keluarkan kode alias provisional bila disepakati sebagai bagian kurasi final.
4. Pertahankan unit/regression tests; jalankan tes yang terkena perubahan. Periksa Compose efektif untuk semua overlay.
5. Ambil bukti penerimaan pada run terpisah dan commit yang jelas, simpan manifest JSON/log/gambar yang dipakai laporan.
6. Ganti paragraf, angka, nama berkas dan gambar laporan berdasarkan hasil tersebut; perbaiki formulir AI.
7. Verifikasi clean-clone pada commit final, baru buat tag milestone.

Penghapusan pada langkah 2 telah diterapkan pada 12 berkas yang disebutkan di bagian 2.1. Pemilihan diagram, rename, pemindahan direktori dan perubahan API legacy masih ditunda. Modul layanan serta seluruh unit/regression test dipertahankan.

## 6. Verifikasi pada penelaahan ini

- PDF laporan 59 halaman dan formulir AI satu halaman dibaca; seluruh halaman laporan diperiksa melalui render, gambar yang relevan dibuka lebih besar.
- Referensi nama berkas, import helper, pemanggilan subprocess dan dependensi overlay ditelusuri. File yang tidak muncul sebagai nama literal pada laporan tetap diperiksa fungsinya.
- `python3 -B -m unittest discover -s scripts -p 'test_*.py'`: **4 tes lulus**. Respons kredensial asing 200 pada log salah satu kasus merupakan server uji sengaja tidak aman yang harus ditolak checker, bukan hasil sistem nyata.
- `k6 --address '' run --quiet tests/check-load-summary.js`: **lulus**, token setup tidak masuk JSON summary.
- Pada penelaahan awal, HEAD dan remote main sama-sama `78d1e85`; tidak ada tag milestone remote pada waktu pemeriksaan.
- Tidak menjalankan ulang P1–P5 atau clean-clone, tidak menghentikan container, dan tidak menyunting Google Docs/formulir. Pembersihan menghapus artefak/checker lama, tanpa mengubah kode layanan atau nama berkas.

## 7. Verifikasi pembersihan berkas

- Sebanyak 12 berkas di bagian 2.1 dihapus; tidak ada rename atau perubahan kode layanan.
- Compose utama dan Compose dengan overlay checkpoint lama menghasilkan konfigurasi efektif yang identik pada environment lokal yang diperiksa sebelum penghapusan. `docker compose --env-file .env -f docker-compose.yml config --quiet` tetap lulus.
- Empat unit test Python dan tes redaksi summary k6 dijalankan ulang setelah pembersihan; seluruhnya lulus.
- Syntax seluruh skrip Python yang tersisa dan command `--help` checker resiliensi diperiksa. Tiga bagian `show-p1-evidence.py` tetap berhasil membaca bukti JSON tersimpan.
- Rujukan aktif ke berkas yang dihapus dibersihkan. Nama berkas lama pada bagian 2.1 dan koreksi R2 sengaja dipertahankan sebagai catatan audit/perubahan laporan.
- JSON, log asli, tiga PNG terminal P1, diagram, serta seluruh unit/regression test tetap tersedia. Pengujian lifecycle P1–P5 tidak dijalankan ulang dalam pembersihan ini.
