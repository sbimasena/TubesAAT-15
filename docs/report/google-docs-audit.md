# Audit laporan Google Docs — 9 Oktober 2026

Laporan yang diperiksa: [IF4031 Milestone 1](https://docs.google.com/document/d/1NNjBYWGHbg5uj43golkOZAjVn8enGM48l_zM3MpRTuQ/edit). Audit menggunakan ekspor teks dan PDF 59 halaman, termasuk pemeriksaan visual seluruh halaman. Nomor halaman di bawah mengikuti ekspor tersebut dan dapat bergeser setelah penyuntingan.

Pembanding adalah `context.md` lokal yang diberikan penulis, implementasi layanan, Compose, skrip penerimaan, dan bukti P1. Pohon aplikasi saat pemeriksaan sama dengan commit `962f334` di `origin/main`; commit tersebut menggabungkan `2d6280f`, yang menjadi commit dasar bukti P1. Dokumen spesifikasi tugas asli tidak tersedia sebagai sumber terpisah dalam audit ini. Ketentuan yang hanya disebut pada laporan dibedakan dari persyaratan yang tercantum dalam konteks.

Arsitektur dan mekanisme utama P1–P5 sudah sesuai. Laporan masih memerlukan koreksi pada angka hasil, metode pembuatan gambar, prosedur reproduksi, deklarasi AI, dan kelengkapan penyerahan. Catatan di bawah membedakan kesalahan yang terkonfirmasi dari penambahan untuk memperjelas bukti.

## A. Koreksi yang perlu dilakukan

| ID | Lokasi | Temuan dan tindakan |
|---|---|---|
| A1 | Sampul, hlm. 1 | `Nama sistem: [isi nama sistem]` masih berupa placeholder. Isi nama sistem yang dipakai kelompok. |
| A2 | Bab 1, hlm. 2 | Bagian “Petunjuk pengisian” masih memuat instruksi template. Hapus dari naskah akhir; tabel kontribusi anggota sudah terisi. |
| A3 | Bab 6, hlm. 12 | Paragraf `[Isi ringkasan perintah sesuai implementasi ...]` masih ada. Hapus setelah ringkasan prosedur lengkap; cantumkan URL repo dan referensi commit/tag final yang benar-benar tersedia. |
| A4 | Bab 6, P1, hlm. 14 | Perintah `render-p1-evidence.py` dan uraian screenshot HTML/Chrome tidak sesuai dengan Gambar 1–3 saat ini yang dibuat menggunakan Freeze. Ganti dengan `freeze --execute` atas `show-p1-evidence.py`. Renderer HTML akan menimpa PNG terminal jika memakai JSON di direktori bukti utama. |
| A5 | Bab 6, hlm. 13–16 | Startup memakai `.env`/project default, P1 memakai `.env.p1.local`/`tubesaat-p1-report`, P2–P3 memakai `../evidence.env`/`tubes-aat-15`, sedangkan P4–P5 kembali ke `.env`. Berkas konfigurasi dan stack masing-masing belum disiapkan dalam prosedur. Gunakan satu konfigurasi penerimaan atau jelaskan pembuatan dan startup setiap project. `../evidence.env` tidak ada pada workspace yang diperiksa dan bukan berkas yang disertakan repo. |
| A6 | Bab 6, P1, hlm. 13–14 | Pernyataan bahwa project terpisah mencegah benturan belum lengkap: nama project tidak mengubah port host. Tambahkan konfigurasi port bebas untuk BMKG, PVMBG, Auth, Client API, Aggregator operator, dan RabbitMQ management. Penjelasan serta urutan konsisten tersedia pada README utama. |
| A7 | Bab 7.2, tabel “Tidak ada resource exhaustion”, hlm. 23 | Narasi menyebut 194 HTTP 200 dan 62 HTTP 429. Gambar 8–9 pada hlm. 42–43 menunjukkan 224 HTTP 200 dan 32 HTTP 429 dari 256 request. Ganti narasi menjadi 224/32 jika gambar dipertahankan. Jangan menggabungkan angka dari run lain. |
| A8 | Bab 7.2, mekanisme galat, hlm. 20 | Kalimat “Auth Service atau Aggregator yang unavailable menghasilkan 503” terlalu umum. Auth gagal validasi akibat dependency menghasilkan 503; Aggregator yang merespons 503 dipetakan menjadi 503; timeout Aggregator menjadi 504; galat transport, termasuk koneksi ditolak, menjadi 502. Rujukan: `services/client-api/internal/httpapi/errors.go`. |
| A9 | Bab 6, 7.2–7.3, 8; Gambar 8–9 dan 14 | Gambar 8–9 memakai `check-resilience.py`, Gambar 14 memakai `check-auth.py`, sedangkan repo/laporan memakai `check-member-b-resilience.py` dan `check-member-b-auth.py`. Nama lama tidak ada di repo saat audit. Jelaskan asal checkout/skrip pengumpulan bukti jika dapat dibuktikan, atau ambil ulang bukti dengan skrip repo dan perbarui semua angka terkait. Jangan menyunting nama di gambar lama seolah perintah tersebut dijalankan pada run lama. |
| A10 | Bab 9, hlm. 34 | Deklarasi AI masih berupa petunjuk template. Isi penggunaan aktual: Codex untuk penelaahan kode dan draf bagian A, alat bukti P1, rendering terminal dengan Freeze, diagram SVG/PNG, dan audit. Nyatakan verifikasi yang benar-benar dilakukan penulis/kelompok. Penggunaan AI anggota lain tetap harus diisi berdasarkan keterangan mereka. |
| A11 | Lampiran A, hlm. 35 | Hanya ada judul “Form Penggunaan AI”; formulir belum dilampirkan pada PDF yang diperiksa. Isi dan sertakan formulir yang diwajibkan template tugas. |
| A12 | Bab 6 dan kelengkapan milestone | Belum ada tag `milestone-1` pada tag lokal maupun remote saat audit. `context.md` §19 mensyaratkan tag akhir dibuat setelah verifikasi clean clone. Sertakan hasil clean-clone pada commit final; baru buat dan rujuk tag. Menyebut nama skrip saja belum membuktikan run tersebut lulus pada commit final. Audit/push artefak ini tidak membuat tag atau menjalankan ulang clean-clone. |

Redaksi pengganti untuk A8:

> Kegagalan Auth Service saat validasi menghasilkan HTTP 503. Respons HTTP 503 dari Aggregator diteruskan sebagai 503, timeout permintaan Aggregator menghasilkan 504, sedangkan kegagalan transport atau respons Aggregator yang tidak valid menghasilkan 502. Kode galat dan correlation ID tetap disertakan tanpa meneruskan rincian internal dependency kepada klien.

Perintah pengganti A4 untuk gambar runtime dari bukti yang sudah tersimpan:

```sh
freeze --execute 'python3 -B scripts/show-p1-evidence.py runtime' \
  --config base --window --font.size 20 --margin 0 --padding 24 \
  --border.radius 0 --shadow.blur 0 --shadow.x 0 --shadow.y 0 \
  --output docs/evidence/member-a/p1/p1-03-runtime-verification.png
```

Perintah ketiga gambar dan log checker asli tersedia pada [README bukti P1](../evidence/member-a/p1/README.md). Pengujian baru sebaiknya menggunakan output terpisah; timestamp, hazard ID, message ID, dan confidence dapat berubah dan perlu disesuaikan bila run baru dipakai dalam laporan.

## B. Isi yang perlu ditambahkan atau diperjelas

| ID | Lokasi dan titik penyisipan | Isi yang disarankan dan alasannya |
|---|---|---|
| B1 | Bab 3.1, baris Auth Service, hlm. 5–6 | Tambahkan port container Auth 8084 dan URL internal `http://auth:8084`. Bedakan port host yang dapat diubah dengan port container tetap. |
| B2 | Bab 6, prasyarat, hlm. 13 | Tambahkan `lsof` native untuk bukti socket P2 dan Freeze untuk gambar terminal. Chrome hanya diperlukan untuk renderer diagram/HTML. Gunakan `docker compose up --build -d --wait` sebelum checker dan beri contoh health serta login/query yang dapat dieksekusi, bukan hanya `POST /login`. |
| B3 | Bab 6, konfigurasi P1, setelah uraian generator 10 detik | Cantumkan `BMKG_GENERATE_INTERVAL_SECONDS=10`, `PVMBG_GENERATE_INTERVAL_SECONDS=10`, `POLL_INTERVAL_SECONDS=3`, dan `PVMBG_DELAY_MS=750`. Default generator repo/Compose adalah 15 detik, sesuai uraian Bab 3.1. Karena laporan menyebut ketentuan satu event per 10 detik, pastikan konfigurasi demo memenuhi ketentuan yang ditulis; `context.md` sendiri hanya menyebut pembentukan periodik. |
| B4 | Bab 7.1 dan Lampiran B.1, hlm. 18–19 dan 35–37 | Sebutkan run P1 9 Oktober 2026, 19.41.19–19.41.39 WIB, commit dasar `2d6280f`, project/config, serta sumber JSON/log. Jelaskan bahwa gambar Freeze menampilkan hasil pengujian tersimpan. Tambahkan log checker asli sebagai bukti tambahan bila perlu menunjukkan tahap pemicu perubahan runtime secara eksplisit. Gambar 1–3 sudah cocok dengan JSON, tetapi tidak menampilkan request admin dan responsnya secara langsung. |
| B5 | Bab 7.2, mekanisme, setelah uraian dua poller dan timeout | Tambahkan batas pool PostgreSQL delapan koneksi serta timeout operasi database tiga detik dari `services/aggregator/internal/store/postgres.go`. Ini melengkapi pembatasan resource yang sudah dijelaskan melalui dua poller dan limiter 64 pada Client API; §11 konteks meminta concurrency yang dibatasi dan bukan sekadar goroutine. |
| B6 | Bab 7.2, sebelum tabel hasil, hlm. 22 | Nyatakan workload persis: query `GET /hazards?source=BMKG&hazard_type=SEISMIC&limit=100`, 50 sesi Field Team independen, 50 VU/60 detik per kondisi, TTL 60 detik, refresh otomatis, serta waktu pengambilan sampel socket pada detik 15/30/45. Latensi adalah request ke Client API untuk data tersimpan; pengukuran ini bukan waktu sumber menghasilkan event hingga klien menerimanya. |
| B7 | Bab 7.2, konfigurasi/hasil; Bab 8 | Tambahkan mesin/OS, CPU/RAM dan alokasi Docker, versi k6/Docker, tanggal/commit/config run, serta tautan JSON metrik P2 dan hasil resiliensi. Definisikan throughput sebagai jumlah query berhasil dibagi `measurement_wall_seconds` sesuai checker, bukan menganggap semua pembagi tepat 60 detik. Catat hasil refresh bila mengklaim flow refresh terjadi selama load. Artefak mentah P2/P3 yang sesuai screenshot belum tersedia pada repo/workspace yang diperiksa; perlu diperoleh dari run yang dipakai atau pengujian ulang. |
| B8 | Bab 7.3, batasan sesi, hlm. 25 | Tambahkan TTL refresh/sesi default 3600 detik. Rotasi tidak memperpanjang batas waktu sesi yang dihitung sejak login; setelah batas ini atau restart Auth diperlukan login ulang. Rujukan: `.env.example` dan `services/auth/internal/session/service.go`. |
| B9 | Bab 7.4, setelah penjelasan store dan JSONB, hlm. 26–29 | Tambahkan skema aktual: `hazard_events` (11 field kanonis + `revision`), `hazard_outbox`, dan tabel pencatat `schema_migrations`. Nyatakan PK, unique `(source, source_ref_id)`, FK outbox ke hazard, unique `(hazard_id, hazard_revision)`, indeks filter/waktu, dan indeks pending. Jelaskan bahwa `revision`/metadata outbox bukan field kanonis yang dikirim ke klien. Komentar “skema db” dalam Google Docs belum ditindaklanjuti. |
| B10 | Bab 7.5, tabel bukti, hlm. 32 | Tambahkan satu baris untuk Gambar 27: broker outage menahan outbox pending, query tetap tersedia pada sistem yang sudah berjalan, kemudian publikasi pulih tanpa restart Aggregator. Gambar 27 sudah dirujuk di Bab 8 tetapi belum dipasangkan dengan kriteria pada tabel P5. |
| B11 | Bab 3.1 atau Bab 6 setelah healthcheck; Bab 7.3/7.4 | Jelaskan cakupan healthcheck: PVMBG dapat health 200 ketika endpoint datanya outage; health Aggregator memeriksa database, bukan freshness semua sumber atau publisher drain. Jelaskan juga overlay operator localhost membuka endpoint penuh tanpa JWT. Boundary default ditopang jaringan Compose dan port tertutup; akses operator bukan API berotorisasi berdasarkan peran klien. |

Skema ringkas untuk B9, berdasarkan migrasi aktual:

```text
hazard_events                           hazard_outbox
-------------------------------         -------------------------------
hazard_id TEXT PK <--------------------- hazard_id TEXT FK
source TEXT                             message_id BIGINT IDENTITY PK
source_ref_id TEXT                       hazard_revision BIGINT
hazard_type TEXT                         payload JSONB (snapshot hazard)
severity TEXT                           correlation_id TEXT
area_name TEXT                          created_at TIMESTAMPTZ
latitude DOUBLE PRECISION               published_at TIMESTAMPTZ NULL
longitude DOUBLE PRECISION              UNIQUE(hazard_id, hazard_revision)
occurred_at TIMESTAMPTZ
ingested_at TIMESTAMPTZ                  Relasi: satu hazard, banyak revisi
attributes JSONB                        dalam outbox (1:N).
revision BIGINT
UNIQUE(source, source_ref_id)

schema_migrations: version, applied_at (pencatatan migrasi).
```

Ini ilustrasi skema yang sudah ada, bukan usulan migrasi atau perubahan database.

## C. Penyajian dan tautan

1. Bab 3, hlm. 3–4: kedua diagram belum memiliki caption/nomor. Gunakan Diagram 3.1/3.2 agar tidak menggeser Gambar 1–27 pada lampiran. Diagram utama masih menggunakan gambar lama dan tampak kecil; [PNG/SVG arsitektur](../diagrams/README.md) menyediakan versi yang lebih terbaca.
2. Lampiran P1, hlm. 35–37: perluas caption “BMKG dan warning tsunami”, “PVMBG v1 dan v2”, dan “Runtime Verification” agar menyebut pemeriksaan yang dibuktikan. Caption lengkap tersedia pada panduan tambahan Member A.
3. Hlm. 54: `B.45Problem 5` seharusnya `B.5 Problem 5`. Judul berada pada halaman tersendiri yang hampir kosong; satukan dengan gambar pertama P5 melalui pengaturan paragraf/page break.
4. Hlm. 29 dan 33: PDF memuat tautan salah ke `http://check-p4.py` dan `http://check-member-b-resilience.py`. Ganti dengan URL berkas GitHub atau teks monospace tanpa hyperlink otomatis.
5. Berbagai bab: backtick Markdown tampil literal dalam Google Docs. Terapkan format monospace pada nama berkas/field, hapus delimiter backtick, dan konsistenkan kapitalisasi teknis. Koreksi “logitude nya” menjadi “longitude” pada tabel referensi Bab 5.
6. Google Docs masih memiliki komentar tentang load testing dan skema database. Tindak lanjuti komentar dan hapus instruksi template dari versi final; komentar tidak dianggap sebagai isi pembahasan yang sudah selesai.

## D. Pemeriksaan terhadap konteks

| Kelompok persyaratan | Hasil pencocokan |
|---|---|
| Ruang lingkup dan ownership | Dua sumber, dua jenis bencana, tujuh layanan aplikasi + dua infrastruktur, tiga peran baca, kontribusi A/B/C sesuai. |
| Komunikasi dan deployment | HTTP/AMQP, modul/Dockerfile mandiri, tidak berbagi business logic antarlayanan, PostgreSQL milik Aggregator. Akses operator perlu dijelaskan (B11). |
| Sumber dan polling | Masing-masing sumber menyediakan 20 record historis; BMKG delay 50–150 ms, PVMBG delay konfigurabel 500–3000 ms; polling mandiri default 3 detik. Konfigurasi generator demo perlu eksplisit (B3). |
| Model dan mapping | Sebelas field kanonis, severity berbasis warning atau magnitudo, enam referensi gunung api, ID deterministik, overlap/cursor setelah commit, dan kebijakan field tambahan sudah dijelaskan sesuai kode. |
| P1 | PVMBG v1/v2, confidence identik, tanpa restart/migrasi, trace dan dua consumer cocok dengan JSON. Metode pembuatan gambar dan provenance perlu diperbaiki (A4/B4). |
| P2 | Angka sustained load dan p95 dilaporkan, outage/read last-known/freshness/recovery dijelaskan. Burst tidak cocok dengan screenshot (A7); error mapping perlu spesifik (A8); metode/raw metrics perlu dilengkapi (B5–B7). |
| P3 | Kredensial terpisah, silang kredensial, proyeksi Media, expiry alami 59,831 detik untuk TTL 60 detik, refresh/rotasi/token lama dijelaskan. Waktu tunggu sekarang sudah sesuai Gambar 14; tidak perlu diganti lagi. Nama skrip/provenance dan batas sesi perlu dilengkapi (A9/B8). |
| P4 | Independensi rebuild, persistence, JSONB, v1/v2 berdampingan, isolasi storage dijelaskan. Diagram/skema aktual perlu ditambahkan (B9); hasil clean-clone commit final belum ditunjukkan (A12). |
| P5 | Durable fanout, queue mandiri, manual ack, snapshot outbox, publisher confirm, at-least-once, idempotensi, backlog/reconnect, subscriber ketiga dan batasannya sesuai. Tambahkan keterlacakan Gambar 27 (B10). |
| Penyerahan akhir | File P1/diagram dan alatnya dipublikasikan melalui commit pendamping audit ini. Placeholder, deklarasi/form AI, artefak mentah P2/P3, hasil clean-clone dan tag final masih perlu dituntaskan. |

`context.md` §8.2, §18, dan §20 masih berisi pilihan belum dibekukan serta status tahap perencanaan. Pemilihan `net/http`, migrasi ringan embedded, dan implementasi yang sudah berjalan tidak otomatis bertentangan dengan persyaratan konteks; bagian status perencanaan lokal tersebut perlu diperbarui secara terpisah. Nilai confidence/message ID P1 yang berbeda dari P4/P5 juga bukan kesalahan karena bukti berasal dari run yang berbeda.

## E. Verifikasi artefak sebelum publikasi

- `go test ./internal/ingest ./internal/httpapi` pada modul Aggregator lulus saat audit.
- Empat skrip baru berhasil dikompilasi sebagai Python; bantuan CLI renderer berhasil ditampilkan. `check-p1-report.py` tidak menjalankan ulang integration test pada audit ini.
- Ketiga bagian `show-p1-evidence.py` berhasil membaca hasil P1 tersimpan.
- JSON P1 menyatakan PASS; migrasi sebelum/sesudah sama; sembilan container tidak berubah; nilai confidence sama; dua consumer tersedia.
- Teks artefak diperiksa untuk JWT, private key, dan nilai kredensial lokal sebelum staging. File environment dan ekspor laporan lama tidak dimasukkan dalam commit artefak.
- Gambar terminal P1 dan diagram sudah diperiksa pada pembuatan sebelumnya. Ekspor Google Docs 59 halaman diperiksa sebagai sumber audit, tanpa menyunting dokumen daring.

Audit ini tidak mengklaim pengujian ulang P2–P5 atau persetujuan penulis atas formulir AI. Nilai historis pengujian dipertahankan dan tidak diganti dengan data buatan.
