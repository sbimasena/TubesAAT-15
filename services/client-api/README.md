# API untuk Klien

Layanan ini sekarang menyediakan `GET /health`, log JSON terstruktur, dan propagasi `X-Correlation-ID`. URL Aggregator dan timeout diatur melalui `AGGREGATOR_BASE_URL` dan `AGGREGATOR_REQUEST_TIMEOUT_MS`; port layanan memakai `CLIENT_API_PORT` (awal 8080). Layanan memanggil Aggregator lewat HTTP dan tidak mengakses Canonical Store secara langsung.

## API hazard terlindungi

`GET /hazards` membutuhkan header `Authorization: Bearer <access_token>`. API Klien memanggil Auth melalui `POST /internal/v1/introspect` pada setiap request, tanpa cache. Token hilang, rusak, kedaluwarsa, direvokasi, atau memiliki versi sesi lama menghasilkan `401`; kegagalan Auth, timeout, respons tidak valid, atau secret internal tidak cocok menghasilkan `503`. Healthcheck tetap tidak bergantung pada Auth.

Scope berasal dari hasil validasi Auth, bukan parameter klien. Media hanya menerima tujuh field Ringkasan: `hazard_id`, `source`, `hazard_type`, `severity`, `area_name`, `occurred_at`, `ingested_at`. Tim Lapangan/Operasi Internal menerima seluruh sebelas field kanonis, termasuk `attributes` tambahan. Field top-level yang belum ada dalam allowlist tidak diteruskan otomatis.

Filter `source`, `hazard_type`, `since`, dan `limit` diteruskan ke Aggregator. `fields` dapat memilih field kanonis yang diizinkan. Permintaan Media dengan `include_raw=true` atau `fields` yang menyebut `source_ref_id`, `latitude`, `longitude`, atau `attributes` menghasilkan `403` sebelum memanggil Aggregator. Field tidak dikenal, sintaks `include_raw` tidak valid, atau parameter pemilihan field berulang menghasilkan `400`.

Respons berisi `data` yang diproyeksikan, `count` dari Aggregator, dan metadata aman `sources` untuk BMKG/PVMBG. Correlation ID diteruskan ke Auth/Aggregator; log request memuat identitas/scope terverifikasi, dan kedua adapter mencatat latensi outbound tanpa token/secret. Respons memakai `Cache-Control: no-store`.

Adapter mendekode dan memvalidasi envelope Aggregator satu kali, beserta status BMKG/PVMBG, jumlah data, tipe field freshness, dan timestamp. Handler menerima hasil terverifikasi untuk proyeksi scope tanpa mengulang decode JSON seluruh respons. `available` menyatakan keterjangkauan upstream; `last_ingested_at` menyatakan ingestion yang sudah selesai pada manager Aggregator saat ini. Timestamp kosong setelah restart tidak membuktikan bahwa database tidak memiliki data, dan hasil filter kosong tidak otomatis berarti outage. Kedua adapter menolak redirect HTTP. Log kegagalan Auth memuat klasifikasi aman (`timeout`, `canceled`, `transport`, `upstream_status`, atau `invalid_response`) dan level WARN; token tidak aktif dicatat sebagai `invalid_token`, tanpa token atau detail galat internal.

## Metadata sumber dan respons stale

Keputusan 1A mempertahankan `200` pada query penyimpanan yang berhasil, termasuk data terakhir saat upstream mati atau hasil filter kosong (`data=[]`, `count=0`). Status gagal baca penyimpanan dari Aggregator tetap dipetakan menjadi `503`. Kesegaran pipeline tidak diubah berdasarkan jumlah atau umur rekaman yang ditemukan.

`sources` selalu memuat BMKG dan PVMBG, termasuk ketika query hanya memilih salah satu sumber. Setiap sumber memiliki tepat lima field: `available`, `last_ingested_at`, `stale`, `stale_since`, `stale_after_seconds`. Timestamp yang belum tersedia bernilai `null`. Proyeksi ini sama untuk ketiga scope; `last_error`, `ingestion_error`, `last_success_at`, dan field tambahan tidak diteruskan. Data hazard Media tetap dibatasi tujuh field Ringkasan.

## Galat dan batas konkurensi

Galat yang dihasilkan handler hazard memakai JSON `{"error":{"code":"...","message":"..."},"correlation_id":"..."}`. Pesan tetap aman tanpa body dependency, URL internal, stack trace, atau token.

| Kondisi | Status / kode |
| --- | --- |
| Bearer hilang atau token tidak valid | `401` / `invalid_access_token` |
| Field mentah ditolak | `403` / `forbidden` |
| Parameter query atau pilihan field salah | `400` / `invalid_query` atau `invalid_fields` |
| Auth tidak tersedia | `503` / `auth_unavailable` |
| Aggregator mengembalikan 400 | `400` / `invalid_query` |
| Aggregator mengembalikan 503 | `503` / `aggregator_unavailable` |
| Timeout Aggregator | `504` / `aggregator_timeout` |
| Respons Aggregator rusak | `502` / `invalid_aggregator_response` |
| Koneksi Aggregator gagal atau status galat lainnya | `502` / `aggregator_error` |
| Kapasitas request hazard penuh | `429` / `concurrency_limit`, header `Retry-After: 1` |

`CLIENT_API_MAX_CONCURRENT_REQUESTS` memiliki nilai awal 64 dan harus berupa bilangan bulat positif. Satu batas berlaku bersama untuk `/hazards` dan alias checkpoint, sejak sebelum introspeksi Auth sampai respons selesai. Request saat penuh langsung ditolak tanpa antrean internal. Permit dilepas setelah selesai, gagal, atau dibatalkan; healthcheck tidak memakai batas ini. Nilai akhir ditinjau lewat pengukuran P2. Ini pembatas konkurensi, bukan pembatas jumlah request per detik. Pembatalan request diteruskan ke dependency; handler tidak mencoba mengirim galat setelah konteks dibatalkan, dan log memakai `error_kind=canceled` dengan status 0 jika respons belum dikirim.

Konfigurasi tambahan: `AUTH_BASE_URL`, `AUTH_REQUEST_TIMEOUT_MS`, dan `AUTH_INTERNAL_SECRET` (minimal 32 byte). Signing key JWT tidak diberikan kepada API Klien. Timeout penulisan server memberi jeda setelah jumlah timeout Auth dan Aggregator.

Setiap adapter HTTP memiliki transport sendiri yang mempertahankan maksimal 64 koneksi idle ke dependency, dengan idle timeout bawaan 90 detik. Pool ini menghindari pembukaan ulang koneksi pada beban konkurensi awal 64; jumlah request aktif tetap dibatasi oleh handler. Timeout request dan penolakan redirect tetap berlaku. Introspeksi Auth tetap dilakukan pada setiap request sehingga refresh/revokasi tidak tertunda oleh cache token.

Untuk kompatibilitas checkpoint fondasi, `ENABLE_PROVISIONAL_HAZARD_ENDPOINT=true` mengaktifkan alias `GET /internal/provisional/hazards`, ditandai `X-Provisional-Endpoint: true`. Alias memakai Auth, proyeksi hazard/sumber, galat JSON, dan batas konkurensi yang sama dengan `/hazards`. Alias tetap nonaktif secara bawaan.

Timeout penulisan respons server minimal 15 detik dan selalu menyediakan jeda 5 detik setelah jumlah timeout Auth dan Aggregator, sehingga respons 504 masih dapat dikirim. Timeout saat membaca body Aggregator juga menghasilkan 504. Timeout dependency awal 5000 ms; tuning dilakukan berdasarkan pengukuran P2.

Setelah variabel lingkungan wajib diisi, jalankan `go run ./cmd/server` dari direktori `services/client-api`, lalu periksa `GET /health` pada port `CLIENT_API_PORT`. Pengujian unit modul: `go test ./...` dari direktori yang sama.

Binary mendukung `/service --healthcheck`, dengan timeout 3 detik, menggunakan `CLIENT_API_PORT` atau nilai awal 8080. Compose utama menyediakan environment, dependency, dan healthcheck kedua service tanpa memberi akses Canonical Store.

## Pemeriksaan autentikasi

Jalankan Auth, API Klien, dan Aggregator yang sudah memiliki data BMKG/PVMBG. Untuk Compose, gunakan file lingkungan lokal lengkap. Endpoint sementara sebaiknya dinonaktifkan (`ENABLE_PROVISIONAL_HAZARD_ENDPOINT=false`) selama pemeriksaan API publik.

```sh
docker compose --env-file .env.member-b.local up --build -d --wait
python scripts/check-member-b-auth.py --env-file .env.member-b.local --natural-expiry --output member-b-auth-checkpoint.json
```

Skrip memeriksa login tiga peran, tujuh/sebelas field, penolakan permintaan mentah, rotasi, replay refresh token, dan penolakan access token lama sebelum expiry. `--natural-expiry` menunggu TTL nyata Tim Lapangan (default 60 detik, maksimal 300 detik untuk checkpoint), memeriksa `401`, lalu refresh tanpa login ulang dan memakai token baru. Jam sistem tidak diubah. Skrip tidak me-restart layanan, mengubah status sumber, atau menghapus volume. Output tidak berisi token, kredensial, atau payload hazard.

Jika URL publik berbeda, gunakan `--auth-url` dan `--client-url`. Hasil ini adalah bukti bagian downstream P3 milik B; tidak menggantikan uji silang kredensial upstream milik A, uji beban/outage P2, atau verifikasi stack/fanout seluruh tim. Kontrak stale/freshness publik dan batas konkurensi/429 sudah diimplementasikan; pengukuran beban P2 tetap terpisah.

## Pemeriksaan resiliensi

Jalankan hanya pada project uji yang dipilih secara eksplisit dan memiliki enam layanan HTTP/storage yang sehat. Broker/consumer tidak diuji oleh checker ini. Jangan menjalankan demo yang mengubah sumber atau lifecycle secara bersamaan.

```sh
python scripts/check-member-b-resilience.py --env-file .env.member-b.local --project-name member-b-checkpoint --exercise-outages --burst-connections 128 --output member-b-resilience-checkpoint.json
```

Pemeriksaan dasar memvalidasi envelope/proyeksi Media, filter kosong 200, galat JSON 400/403, refresh, correlation ID lintas proses, identitas login/refresh, latensi outbound, dan redaksi token/secret pada log B. `--exercise-outages` mengaktifkan outage PVMBG untuk memeriksa data terakhir 200/stale, independensi BMKG, dan recovery; kemudian menghentikan/menjalankan kembali hanya PostgreSQL dan Auth pada project tersebut untuk memeriksa 503/health/recovery. Flag outage asal dipulihkan, layanan yang dihentikan dipulihkan, dan volume tidak dihapus. Auth restart tetap menghapus sesi memori.

Burst bersifat opsional dan singkat: hanya 200/429 yang diterima, serta jumlah 429 yang teramati dicatat. Burst tidak menjamin kapasitas selalu penuh; tes Go menahan tepat 64 request untuk memverifikasi batas secara deterministik. Bukti ini belum memenuhi uji P2 >=50 koneksi selama >=60 detik atau penerimaan stack/fanout seluruh tim. Output tidak memuat token, kredensial, atau payload hazard; stack tetap berjalan setelah checker selesai.
