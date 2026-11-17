# Tracker tugas Anggota B — API, Auth, dan Resilience

**Acuan:** [`member_b_codex_handover_revised.md`](../member_b_codex_handover_revised.md), B-01 sampai B-20.  
**Potret status:** 6 Oktober 2026; Stage 4 implementasi dan tes HTTP/race, setelah merge `origin/main` (`d071299`) melalui `65d6808`.
**Spek resmi:** `C:/Users/ASUS/Downloads/IF4031 - Tugas Besar - M1.pdf`, ditinjau 6 Oktober 2026. Halaman 11–12 menetapkan klasifikasi field; halaman 14–15 dan 20–21 menetapkan hak akses/TTL/P3. Persistensi sesi Auth setelah restart tidak diwajibkan secara eksplisit; pengguna memilih 1A/2A/3A: sesi memori, introspeksi tanpa cache, dan tujuh field Media.
**Pengiriman fondasi:** [PR #2](https://github.com/sbimasena/TubesAAT-15/pull/2), branch `feat/downstream-api-foundation`, empat commit. Dokumen kontrak/tracker/handover/context/arsitektur tetap untracked dan tidak masuk PR.
**Batas kepemilikan:** API Klien hanya membaca Aggregator melalui HTTP; tidak mengakses PostgreSQL atau mengimpor logika bisnis Aggregator. Wiring Compose akhir milik Anggota C; kontrak API Aggregator akhir milik Anggota A bersama tim.

## Arti status

- **Selesai:** kriteria tugas pada tahap tersebut terpenuhi oleh kode dan pengujian yang tersedia. Ini tidak berarti seluruh Milestone 1 selesai.
- **Selesai sementara:** checkpoint dengan kontrak sementara sudah terbukti; kontrak atau integrasi akhir masih menunggu pihak terkait.
- **Parsial:** sebagian perilaku sudah ada, tetapi kriteria tugas belum seluruhnya terpenuhi atau belum diverifikasi.
- **Belum mulai:** implementasi inti tugas belum ada.
- **Tertahan:** penyelesaian akhir memerlukan tugas sebelumnya atau keputusan/handoff yang disebutkan. Bagian yang mandiri tetap dapat dikerjakan.

Label prasyarat dalam handover tetap dipakai sebagaimana ditulis: `READY`, `READY WITH PROVISIONAL CONTRACT`, `BLOCKED BY MEMBER A`, `BLOCKED BY MEMBER C`, dan `TEAM DECISION REQUIRED`. Status tugas di bawah ini berbeda dari label prasyarat tersebut.

## Status per tugas

| ID / tahap | Status saat ini dan bukti | Sisa pekerjaan / syarat selesai |
| --- | --- | --- |
| **B-01 — kontrak bersama** | **Selesai untuk Stage 1.** Kontrak Aggregator M1 dan keputusan Stage 4 dicatat; 1A/2A/3A disetujui pengguna. | Port 8084 serta timeout dependency 5000 ms masih sementara. Protection internal Aggregator, kontrak stale/error publik, dan konkurensi dibahas pada tahap terkait. |
| **B-02 — kerangka Auth** | **Selesai.** Server, `GET /health`, konfigurasi lingkungan, timeout server, shutdown, log JSON, dan tes ada di [`services/auth`](../services/auth/). Kredensial lingkungan sudah digunakan untuk identitas/login pada Stage 4. | Port dan wiring Compose akhir menunggu C; fitur identitas/token dicatat pada B-07 sampai B-09. |
| **B-03 — kerangka API Klien** | **Selesai.** Server, `GET /health`, konfigurasi, middleware `X-Correlation-ID`, log JSON, dan tes ada di [`services/client-api`](../services/client-api/). | Integrasi Auth dan rute publik terlindungi adalah B-10 sampai B-12. |
| **B-04 — kesiapan Docker/Compose awal** | **Selesai untuk kedua service B.** Tes Go/build Linux, build Docker, binary/HTTP healthcheck, stop, dan start ulang lolos pada project uji terpisah. Overlay B memasok environment/healthcheck tanpa akses storage. | Compose utama tetap milik C. Stack sembilan service belum lolos karena skrip broker CRLF; ini dicatat pada B-19, bukan kegagalan image B. |
| **B-05 — adapter HTTP Aggregator** | **Selesai untuk kontrak baca M1.** Adapter memakai path/filter/envelope yang disepakati, context/correlation ID, timeout, batas ukuran, klasifikasi galat, dan latensi. Tes mencakup 400/503, pembatalan, timeout header/body, koneksi ditolak, serta pelestarian metadata freshness dan atribut tambahan. | Bentuk galat publik akhir dan pemetaan source status merupakan B-13/B-14; adapter tidak mengambil alih canonicalization atau mengakses DB. |
| **B-06 — endpoint hazard sementara** | **Selesai untuk checkpoint Stage 3.** Skrip nyata mengambil masing-masing lima hazard BMKG/PVMBG melalui API Klien → Aggregator → PostgreSQL, memeriksa field/filter/freshness serta correlation ID dan log latensi. Endpoint nonaktif secara bawaan. | Pada Stage 4 alias lama sudah memakai autentikasi/proyeksi yang sama dengan rute publik, termasuk saat opt-in aktif. Checkpoint HTTP memakai Aggregator `--no-deps` karena broker CRLF, sehingga tidak membuktikan fanout atau stack sembilan service sehat. |
| **B-07 — identitas dan Argon2id** | **Selesai implementasi.** Tiga ID berbeda, scope server, hash Argon2id bersalt, verifikasi dummy, dan wiring startup/login. Tes kredensial salah/silang/ID tak dikenal serta HTTP tiga peran lolos. | Checkpoint proses/Compose baru lolos; Auth tidak memakai database identitas kanonis. |
| **B-08 — JWT access token** | **Selesai implementasi.** `POST /login` mengembalikan access/refresh token. HS256, issuer/audience, `sub/scope/iat/exp/jti/sid/generation`, secret lingkungan, TTL default 60 detik; tes HTTP dan JWT lolos. | Checkpoint HTTP Docker membuktikan TTL alami 60 detik, refresh tanpa login ulang, dan penolakan token lama. P3 lintas tim tetap terpisah. |
| **B-09 — refresh dan sesi** | **Selesai implementasi.** Sesi memori dipilih (1A); `POST /refresh` merotasi hash dan versi atomik. Tes replay, token lama belum expired, revokasi, fixed session expiry, restart, signing failure, serta 24 refresh bersamaan lolos. | Dokumentasikan login ulang setelah Auth restart. Checkpoint Docker untuk refresh/expiry, Auth restart, serta login ulang telah lolos. |
| **B-10 — strategi validasi token** | **Selesai untuk M1.** Pengguna memilih introspeksi setiap request tanpa cache (2A). Secret internal terpisah, timeout konfigurabel, correlation ID, old-token check, dan Auth outage -> 503 sudah diterapkan/didokumentasikan. | Port/network deployment akhir menunggu C; nilai timeout final ditinjau pada P2. |
| **B-11 — middleware autentikasi API Klien** | **Selesai implementasi.** Bearer wajib pada `GET /hazards`; invalid/inactive -> 401, Auth gagal -> 503. Identitas/scope/sesi berasal dari Auth. Tes fail-closed memastikan request gagal tidak mencapai Aggregator; alias checkpoint lama juga terlindungi. | Checkpoint proses nyata lolos; deployment akhir tetap bersama C. |
| **B-12 — scope dan pembatasan field** | **Selesai implementasi.** Tujuh field Ringkasan, termasuk `ingested_at`; peran lain sebelas field. Raw eksplisit -> 403 sebelum Aggregator; field tak dikenal -> 400. Stage 5 mempertahankan pembatasan dan menambahkan proyeksi metadata sumber yang sama untuk ketiga scope. | Regression checker Docker untuk ketiga peran dan metadata aman lolos; P3 seluruh tim tetap terpisah. |
| **B-13 — timeout dan kegagalan Aggregator** | **Selesai untuk checkpoint Stage 5 (2A).** Galat JSON aman/correlation ID; Aggregator 400 -> 400, 503 -> 503, timeout -> 504, koneksi/5xx lain/respons rusak -> 502. Pembatalan diteruskan; envelope/redirect dijaga. Tes HTTP/race serta runtime galat filter, outage PostgreSQL/Auth, health, dan recovery lolos. | Timeout checkpoint tetap 5000 ms; nilai akhir dan performa di bawah beban ditinjau pada B-18/P2. Timeout/malformed/transport diperiksa lewat tes adapter/handler; tidak semuanya diinjeksi pada Docker nyata. |
| **B-14 — data stale/sumber tidak tersedia** | **Selesai untuk keputusan 1A.** `data/count/sources`, kedua sumber dengan lima field aman/timestamp null, query berhasil -> 200 termasuk stale/kosong. Detail diagnostik internal disaring; Media tetap tujuh field. Tes tiga scope serta data tersimpan dengan metadata reset lolos. Runtime PVMBG outage -> data terakhir 200/stale, BMKG tetap berjalan, recovery tanpa restart lolos. | Metadata kosong tidak membuktikan data historis tidak ada. Uji outage bersamaan dengan beban berkelanjutan tetap bagian B-18/P2. |
| **B-15 — batas konkurensi/429** | **Selesai untuk checkpoint Stage 5 (3A).** Batas bersama 64 sebelum introspeksi, konfigurabel; penuh -> JSON 429 tanpa antrean/`Retry-After: 1`. Health tersedia; tes 64 tertahan, pembatalan/penggantian, dan pelepasan permit setelah galat lolos dengan race detector. Burst Docker 128 koneksi menghasilkan 71 respons 200 dan 57 respons 429, tanpa status lain. | Ukur nilai final pada P2 bersama C; burst belum memenuhi >=50 koneksi selama >=60 detik. Pembatas ini bukan rate limit per detik. |
| **B-16 — observabilitas terstruktur** | **Selesai untuk checkpoint Stage 5.** Log login/refresh/introspeksi memuat identitas/scope terverifikasi. Kedua adapter memiliki correlation ID/latensi; galat Auth diklasifikasi, 429 dan pembatalan dicatat. Tes redaksi serta runtime trace tiga layanan, identitas login/refresh, outbound log, log 429, dan absence token/secret yang diuji lolos. | Observabilitas beban berkelanjutan dan bukti metrik P2 adalah B-18. |
| **B-17 — tes otomatis P3** | **Parsial.** Tes komponen/HTTP/race downstream serta `scripts/check-member-b-auth.py` tersedia untuk field, raw denial, rotasi/replay, dan expiry alami opsional. | Checkpoint downstream nyata, termasuk expiry alami, telah lolos; silang kredensial upstream tetap milik A. P3 penuh memerlukan evidence/secrets review dan penjelasan mekanisme di laporan. |
| **B-18 — kesiapan integrasi/beban P2** | **Parsial.** Stage 5 menyediakan timeout/context, batas konkurensi/429, proyeksi stale, observabilitas, dan checker outage/recovery/burst API publik. Burst singkat serta outage nyata lolos. | Jalankan uji nyata >=50 koneksi paralel selama >=60 detik, PVMBG delay 3 detik/BMKG p95 <300 ms, throughput, p50/p95/p99, galat non-429 <1%, dan outage/recovery di bawah beban. Infrastruktur beban bersama C; storage/ingestion oleh A/C. |
| **B-19 — integrasi Compose penuh** | **Parsial.** Overlay environment/healthcheck serta jalur Auth/API terlindungi terverifikasi pada enam layanan. Broker belum dijalankan ulang karena checkout CRLF; perbaikan LF belum disetujui. | Tuntaskan portabilitas broker serta integrasi deployment akhir bersama C; B-08 sampai B-12 sudah diimplementasikan. Auth/API Klien tetap tanpa canonical `DATABASE_URL` atau jaringan storage. |
| **B-20 — skrip demo dan README** | **Parsial.** README Auth/API/root/skrip diperbarui; checker Stage 2–3, Stage 4, serta Stage 5 resilience tersedia. Checker Stage 4 memeriksa envelope baru tiga peran; checker Stage 5 memeriksa sumber stale, galat JSON, outage/recovery/log dan burst singkat tanpa token/kredensial/payload hazard pada bukti. Keduanya lolos pada Docker. | Tambahkan demo/bukti P2 berkelanjutan dan P3 seluruh tim, lalu sinkronkan deployment akhir dengan C. |

## Peta file dan bagian untuk review

### Stage 5 - bagian mandiri pada 6 Oktober 2026

#### B-13 - validasi dependency dan redirect

- [services/client-api/internal/aggregator/client.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client.go:43) - menolak redirect, memvalidasi envelope/status sumber sebelum menerima respons; handler memakai mapping JSON 2A.
- [services/client-api/internal/aggregator/client_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client_test.go:33) - redirect tidak diikuti atau diteruskan ke target lain.

#### B-14 - representasi dan parsing internal

- [services/client-api/internal/aggregator/envelope.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/envelope.go:12) - tipe status sumber dan decoder kontrak M1; diagnostics hanya dipakai internal, proyeksi publik berada pada `httpapi/sources.go`.
- [services/client-api/internal/aggregator/envelope_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/envelope_test.go:8) - fetch versus ingestion, hasil filter kosong, outage/metadata tanpa ingestion, field tambahan, serta field kontrak rusak/hilang.

#### B-16 - identitas login/refresh dan galat Auth

- [services/auth/internal/session/service.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/session/service.go:25) - identitas/scope terverifikasi untuk logging, dikecualikan dari JSON pasangan token.
- [services/auth/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/httpapi/server.go:28) - metadata request login/refresh/introspeksi dan log penyelesaian.
- [services/auth/internal/httpapi/server_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/httpapi/server_test.go:42) - trace tiga peran, identitas terverifikasi saja, respons tetap empat field, dan redaksi token/secret nyata yang dipakai tes.
- [services/client-api/internal/authclient/client.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/authclient/client.go:46) - WARN untuk dependency gagal; klasifikasi timeout/pembatalan/transport/status/respons; status, latensi, dan correlation ID.
- [services/client-api/internal/authclient/client_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/authclient/client_test.go:51) - klasifikasi dan field log aman untuk respons tidak valid/status galat/transport.

`go test -race -timeout 90s ./...` lolos pada masing-masing modul Auth dan API Klien; tes API Klien/race lolos kembali setelah keputusan Stage 5 1A/2A/3A. Kedua image B berhasil build/healthcheck. Checkpoint Docker tiga peran serta checker Stage 5 outage/recovery/log/burst lolos. Bukti: `C:/Users/ASUS/AppData/Local/Temp/member-b-stage5-auth-regression-evidence.json` dan `member-b-stage5-evidence.json`. Tidak ada commit/push; dokumen lokal tetap untracked. P2 berkelanjutan dan fanout tidak diklaim.

#### Tambahan keputusan 2A/3A untuk review

- [services/client-api/internal/httpapi/errors.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/errors.go:13) - schema JSON aman, mapping galat Aggregator, dan galat proyeksi.
- [services/client-api/internal/httpapi/concurrency.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/concurrency.go:7) - admission tanpa antrean, 429, retry hint, serta pelepasan permit.
- [services/client-api/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server.go:24) - batas bersama sebelum Auth, propagasi pembatalan, decode/proyeksi metadata, dan log galat/canceled.
- [services/client-api/internal/httpapi/resilience_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/resilience_test.go:55) - 64 request saat Auth/Aggregator tertahan, alias/publik berbagi kapasitas, healthcheck, overflow, pembatalan, dan penggantian slot.
- [services/client-api/internal/httpapi/resilience_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/resilience_test.go:162) - galat berulang tidak membocorkan permit atau detail internal.
- [services/client-api/internal/config/config.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/config/config.go:56) dan [config_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/config/config_test.go:24) - nilai awal 64, override lingkungan, validasi nilai positif/overflow.
- [services/client-api/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/cmd/server/main.go:50), [.env.example](/C:/Users/ASUS/.vscode/project/TubesAAT-15/.env.example:27), dan [tests/compose-member-b.yml](/C:/Users/ASUS/.vscode/project/TubesAAT-15/tests/compose-member-b.yml:8) - wiring konfigurasi; Compose utama C tidak diubah.

#### Tambahan keputusan 1A dan checkpoint runtime untuk review

- [services/client-api/internal/httpapi/sources.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/sources.go:11) - tepat lima field publik untuk kedua sumber; timestamp null, tanpa diagnostik internal.
- [services/client-api/internal/httpapi/sources_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/sources_test.go:11) - tiga scope, fresh/stale, filter kosong, metadata reset dengan data tersimpan, serta tidak ada ingestion teramati.
- [services/client-api/internal/httpapi/server_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server_test.go:58) - field Media/peran penuh tetap dibatasi; metadata private/future tidak bocor, alias memakai proyeksi sama.
- [scripts/check-member-b-auth.py](/C:/Users/ASUS/.vscode/project/TubesAAT-15/scripts/check-member-b-auth.py:13) - regression tiga peran memeriksa `data/count/sources` serta galat JSON API Klien.
- [scripts/check-member-b-resilience.py](/C:/Users/ASUS/.vscode/project/TubesAAT-15/scripts/check-member-b-resilience.py:22) - project eksplisit, pemeriksaan metadata/galat, burst, outage sumber/PG/Auth dan cleanup, serta trace/redaksi log.
- [services/client-api/README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/README.md:17) - kontrak metadata sumber dan batas penerimaan checkpoint Stage 5. Panduan skrip/root juga diperbarui.

Bukti runtime lengkap: [member-b-stage5-evidence.json](/C:/Users/ASUS/AppData/Local/Temp/member-b-stage5-evidence.json). Regression scope/envelope: [member-b-stage5-auth-regression-evidence.json](/C:/Users/ASUS/AppData/Local/Temp/member-b-stage5-auth-regression-evidence.json). Project uji dihentikan setelah verifikasi, volume dipertahankan. Broker/consumer dan Compose utama tetap tidak diubah. Ini belum membuktikan P2 berkelanjutan atau stack/fanout sembilan layanan.

### Stage 4 - keputusan 1A/2A/3A pada 6 Oktober 2026

#### B-07

- [services/auth/internal/identity/store.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/identity/store.go:43)
- [services/auth/internal/identity/store_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/identity/store_test.go:1)
- [services/auth/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/cmd/server/main.go:35)

#### B-08

- [services/auth/internal/token/jwt.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/token/jwt.go:17)
- [services/auth/internal/token/jwt_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/token/jwt_test.go:1)
- [services/auth/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/httpapi/server.go:27)
- [services/auth/internal/httpapi/server_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/httpapi/server_test.go:42)

#### B-09

- [services/auth/internal/session/service.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/session/service.go:83)
- [services/auth/internal/session/service_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/session/service_test.go:71)
- [services/auth/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/httpapi/server.go:42)

#### B-10

- [services/auth/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/httpapi/server.go:56)
- [services/client-api/internal/authclient/client.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/authclient/client.go:46)
- [services/client-api/internal/authclient/client_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/authclient/client_test.go:1)
- [services/auth/internal/config/config.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/config/config.go:25)
- [services/client-api/internal/config/config.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/config/config.go:41)
- [.env.example](/C:/Users/ASUS/.vscode/project/TubesAAT-15/.env.example:30)
- [tests/compose-member-b.yml](/C:/Users/ASUS/.vscode/project/TubesAAT-15/tests/compose-member-b.yml:9)

#### B-11

- [services/client-api/internal/httpapi/auth.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/auth.go:19)
- [services/client-api/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server.go:72)
- [services/client-api/internal/httpapi/server_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server_test.go:97)

#### B-12

- [services/client-api/internal/hazard/projection.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/hazard/projection.go:16)
- [services/client-api/internal/hazard/projection_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/hazard/projection_test.go:12)
- [services/client-api/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server.go:39)
- [services/client-api/internal/httpapi/server_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server_test.go:56)

#### B-16/B-20

- [scripts/check-member-b-auth.py](/C:/Users/ASUS/.vscode/project/TubesAAT-15/scripts/check-member-b-auth.py:13)
- [services/auth/README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/README.md:1)
- [services/client-api/README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/README.md:1)
- [README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/README.md:25)
- [scripts/README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/scripts/README.md:1)

Map ini menunjuk bagian implementasi atau tes saat ini. Bukti runtime dan batas penerimaan dicatat di bagian akhir; dokumen kontrak/tracker tetap untracked.

### Tambahan Stage 1–3 pada 5 Oktober 2026

- [docs/member-b-contracts.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/docs/member-b-contracts.md:31) — kontrak Aggregator M1, metadata freshness, storage 503, keputusan port/timeout sementara, dan batas bukti checkpoint. Tetap untracked.
- [services/auth/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/cmd/server/main.go:18) dan [main_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/cmd/server/main_test.go:9) — mode healthcheck binary beserta pengujian HTTP 200/503 dan koneksi gagal.
- [services/client-api/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/cmd/server/main.go:19) dan [main_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/cmd/server/main_test.go:9) — healthcheck binary pada port konfigurabel/nilai awal 8080 beserta tes.
- [services/client-api/internal/aggregator/client_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client_test.go:15) — penerusan freshness, data tersimpan saat sumber outage, dan field tambahan tanpa perubahan payload.
- [tests/compose-member-b.yml](/C:/Users/ASUS/.vscode/project/TubesAAT-15/tests/compose-member-b.yml:1) — overlay environment/dependensi/healthcheck B; tanpa akses database kanonis.
- [scripts/check-member-b.py](/C:/Users/ASUS/.vscode/project/TubesAAT-15/scripts/check-member-b.py:12) — konfigurasi isolasi storage, lifecycle, health, kedua sumber, empat filter, perbandingan kanonis, freshness, correlation ID, dan log latensi. Bukti tidak memuat kredensial/payload hazard.
- [.env.example](/C:/Users/ASUS/.vscode/project/TubesAAT-15/.env.example:24) — timeout 5000 ms dan port Auth 8084 sementara; secret/password tetap kosong.
- [README API Klien](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/README.md:13) — perintah checkpoint dan efeknya. README Auth, skrip, dan root mengarahkan ke panduan tersebut.

Bukti runtime: `C:/Users/ASUS/AppData/Local/Temp/member-b-stage123-evidence.json`. Project uji dihentikan setelah verifikasi; volume dipertahankan. File broker dan `.gitattributes` belum diubah. Daftar review di bawah adalah potret fondasi sebelumnya; nomor barisnya belum seluruhnya diperbarui.

Daftar ini mencakup tugas yang sudah selesai, selesai sementara, atau parsial. Tautan membuka baris awal bagian yang disebutkan. Nomor baris mengikuti potret repositori pada 1 Oktober 2026 dan perlu diperbarui jika kode berubah. Satu file dapat mendukung beberapa tugas; setiap daftar menjelaskan bagian yang relevan untuk tugas tersebut.

## Peta review fondasi historis — sebelum Stage 4

Bagian berikut mempertahankan lokasi review fondasi sebelumnya. Nomor baris dan pernyataan fitur yang belum ada adalah potret lama; status terkini ada pada tabel dan peta Stage 4 di atas.

### B-01 — kontrak bersama · selesai sementara

- [docs/member-b-contracts.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/docs/member-b-contracts.md:7) — batas layanan dan alur Client → Auth / Client API → Aggregator.
- [Kontrak Aggregator](/C:/Users/ASUS/.vscode/project/TubesAAT-15/docs/member-b-contracts.md:31) — path, filter, skema `data/count/sources`, dan perilaku galat yang teramati.
- [Kebijakan field](/C:/Users/ASUS/.vscode/project/TubesAAT-15/docs/member-b-contracts.md:76) — larangan minimum field mentah untuk Media dan usulan allowlist yang belum disepakati.
- [Kontrak Auth](/C:/Users/ASUS/.vscode/project/TubesAAT-15/docs/member-b-contracts.md:82) — klaim token yang direncanakan dan keputusan validasi/refresh yang terbuka.
- [Daftar dependensi](/C:/Users/ASUS/.vscode/project/TubesAAT-15/docs/member-b-contracts.md:104) — keputusan yang perlu disepakati beserta pemiliknya.

### B-02 — kerangka Auth · selesai

- [services/auth/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/cmd/server/main.go:17) — `main()`: logger JSON, pembacaan konfigurasi, `http.Server` dengan timeout, penanganan sinyal, `Shutdown`, dan `ListenAndServe`.
- [services/auth/internal/config/config.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/config/config.go:18) — `Load()`: validasi port, keberadaan secret/kredensial, TTL akses awal 60 detik, dan TTL refresh wajib; `validatePort()` serta `positiveSeconds()` memvalidasi nilai. Kredensial diperiksa keberadaannya, belum diverifikasi untuk login.
- [services/auth/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/httpapi/server.go:13) — `NewHandler()`: `GET /health`, correlation ID, dan log request; `statusWriter` mencatat status HTTP dan `newCorrelationID()` membuat ID.
- [services/auth/internal/config/config_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/config/config_test.go:23) — tes konfigurasi valid/TTL bawaan, secret hilang, dan TTL refresh tidak valid. `validEnv()` berisi fixture khusus tes.
- [services/auth/internal/httpapi/server_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/httpapi/server_test.go:10) — `TestHealthAndCorrelationID()`: health JSON dan ID yang dibuat/dipertahankan.
- [.env.example](/C:/Users/ASUS/.vscode/project/TubesAAT-15/.env.example:28) — nama variabel Auth, TTL, dan ketiga identitas; nilai secret/password tetap kosong.

### B-03 — kerangka API Klien · selesai

- [services/client-api/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/cmd/server/main.go:18) — `main()`: logger, konfigurasi, pembuatan adapter, server dengan timeout, shutdown, dan startup.
- [services/client-api/internal/config/config.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/config/config.go:22) — `Load()`: port awal 8080, validasi origin Aggregator, timeout wajib, dan flag endpoint sementara yang awalnya nonaktif.
- [services/client-api/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server.go:24) — `NewHandler()`: health; middleware mulai pada baris 51 membuat/mempertahankan ID, menyimpannya dalam request context, dan mencatat log. `statusWriter` dan `newCorrelationID()` mendukung middleware.
- [services/client-api/internal/config/config_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/config/config_test.go:9) — konfigurasi valid serta penolakan URL atau timeout yang hilang.
- [services/client-api/internal/httpapi/server_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server_test.go:12) — `TestHealthGeneratesOrPreservesCorrelationID()`.
- [.env.example](/C:/Users/ASUS/.vscode/project/TubesAAT-15/.env.example:22) — variabel API Klien, URL Aggregator, timeout, dan flag integrasi sementara.

### B-04 — kesiapan Docker/Compose awal · parsial

- [services/auth/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/cmd/server/main.go:25) dan [services/client-api/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/cmd/server/main.go:31) — konfigurasi server untuk port dari lingkungan, health handler, timeout, dan shutdown. Ini bagian yang sudah diimplementasikan untuk kesiapan runtime.
- [services/auth/Dockerfile](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/Dockerfile:1) dan [services/client-api/Dockerfile](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/Dockerfile:1) — **file yang sudah ada, tidak diubah**: build Linux tanpa CGO, image `scratch`, dan entrypoint `/service`. Keberhasilan build/run image belum terverifikasi.
- [services/auth/README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/README.md:5) dan [services/client-api/README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/README.md:3) — konfigurasi yang diperlukan untuk menjalankan layanan.
- [.env.example](/C:/Users/ASUS/.vscode/project/TubesAAT-15/.env.example:21) — daftar masukan lingkungan untuk handoff ke C. Compose akhir masih membutuhkan wiring variabel tersebut oleh C.

### B-05 — adapter HTTP Aggregator · selesai sementara

- [services/client-api/internal/aggregator/client.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client.go:16) — `provisionalPath`, batas ukuran respons, `ErrorKind`, dan `RequestError`.
- [Konstruksi client dan request](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client.go:44) — `NewClient()` memvalidasi origin/timeout; `List()` pada baris 57 membangun request, meneruskan filter yang dikenal, context, dan correlation ID, lalu membaca respons atau mengklasifikasi galat.
- [Validasi envelope dan latensi](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client.go:114) — `validEnvelope()` memeriksa `data/count/sources`; `logCall()` pada baris 131 mencatat status dan latensi. Validasi ini belum memetakan setiap field hazard/status sumber menjadi DTO.
- [services/client-api/internal/aggregator/client_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client_test.go:15) — tes sukses/filter/correlation ID; tes timeout (baris 39), respons rusak (52), 400/503 (65), dan context dibatalkan (80).
- [services/client-api/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/cmd/server/main.go:25) — adapter dibuat dari konfigurasi dan diberikan kepada handler.

### B-06 — endpoint hazard sementara · selesai sementara

- [services/client-api/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server.go:31) — registrasi bersyarat `GET /internal/provisional/hazards`, header penanda/no-store, pemanggilan adapter, dan penerusan JSON. Endpoint belum memvalidasi token atau memproyeksikan field.
- [services/client-api/internal/config/config.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/config/config.go:39) — flag opt-in endpoint sementara.
- [services/client-api/internal/httpapi/server_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server_test.go:28) — tes endpoint nonaktif menghasilkan 404; `TestProvisionalHazardEndpointCallsAggregator()` pada baris 37 menguji alur handler → adapter → server HTTP fixture dan responsnya.
- [.env.example](/C:/Users/ASUS/.vscode/project/TubesAAT-15/.env.example:26) dan [services/client-api/README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/README.md:5) — flag awal `false` serta batas penggunaan endpoint.
- [docs/member-b-contracts.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/docs/member-b-contracts.md:102) — catatan checkpoint proses lokal dengan Aggregator nyata. Uji proses tersebut belum menjadi skrip integrasi yang dapat diulang; skrip termasuk sisa B-20.

### B-13 — timeout dan kegagalan Aggregator · parsial

- [services/client-api/internal/config/config.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/config/config.go:35) — pembacaan dan validasi `AGGREGATOR_REQUEST_TIMEOUT_MS`.
- [services/client-api/internal/aggregator/client.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client.go:44) — `http.Client.Timeout`; di `List()`, request memakai context (baris 66), galat transport/timeout/pembatalan diklasifikasi (74), status non-2xx ditolak (80), dan respons dibatasi/divalidasi (85).
- [services/client-api/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server.go:37) — pemetaan sementara menjadi 502 atau 504 tanpa mengembalikan galat internal mentah.
- [services/client-api/internal/aggregator/client_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client_test.go:39) — tes timeout, respons rusak, 400/503, dan pembatalan; tes koneksi ditolak dan timeout body sudah ada pada baris 95/109; tes pemetaan 502/504 pada handler tersedia di `server_test.go`, fungsi `TestProvisionalHazardEndpointHandlesDependencyFailures()`.
- [services/client-api/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/cmd/server/main.go:31) — timeout server; `WriteTimeout` kini berasal dari konfigurasi, minimal 15 detik dan menyediakan jeda 5 detik setelah timeout Aggregator.

### B-16 — observabilitas terstruktur · parsial

- [services/auth/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/cmd/server/main.go:18) dan [services/client-api/cmd/server/main.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/cmd/server/main.go:19) — inisialisasi `slog.NewJSONHandler` dan log startup/galat konfigurasi.
- [services/auth/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/httpapi/server.go:19) — correlation ID dan log `request complete` dengan service, operation, latency, dan status.
- [services/client-api/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server.go:51) — middleware correlation ID/context serta log request API Klien.
- [services/client-api/internal/aggregator/client.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client.go:131) — `logCall()`: target Aggregator, correlation ID, operation, latency, status, dan jenis galat.
- [Tes correlation ID Auth](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/internal/httpapi/server_test.go:10), [tes correlation ID API Klien](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server_test.go:12), dan [tes penerusan ke Aggregator](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client_test.go:15) — cakupan yang sudah ada. Belum ada tes isi log, metadata identitas/scope, atau alur login/refresh.

### B-18 — kesiapan integrasi/beban P2 · parsial

Belum ada modul atau skrip beban khusus B-18. Bagian yang sudah tersedia berasal dari tugas sebelumnya:

- [services/client-api/internal/aggregator/client.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client.go:57) — request context, timeout, penanganan galat, dan log latensi yang mendukung P2.
- [services/client-api/internal/httpapi/server.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/httpapi/server.go:31) — endpoint integrasi sementara dengan kegagalan terkontrol dan correlation ID.
- [services/client-api/internal/aggregator/client_test.go](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/internal/aggregator/client_test.go:39) — bukti tes timeout/kegagalan/pembatalan pada adapter. Ini belum membuktikan perilaku sistem di bawah beban.
- [docs/member-b-contracts.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/docs/member-b-contracts.md:100) — status timeout dan checkpoint lokal. Pengukuran ≥50 koneksi selama ≥60 detik, Auth saat beban, konkurensi/429, dan stale belum dijalankan.

### B-20 — skrip demo dan README · parsial

- [README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/README.md:24) — bagian Anggota B, terutama status implementasi pada baris 33; bagian batas implementasi pada baris 186 juga diperbarui.
- [services/auth/README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/auth/README.md:3) — endpoint health, konfigurasi wajib, cara verifikasi, dan fitur Auth yang belum tersedia.
- [services/client-api/README.md](/C:/Users/ASUS/.vscode/project/TubesAAT-15/services/client-api/README.md:3) — health, correlation ID, konfigurasi adapter, endpoint sementara, timeout/galat, serta batas penggunaan.
- [.env.example](/C:/Users/ASUS/.vscode/project/TubesAAT-15/.env.example:21) — nama variabel runtime dengan secret kosong.
- Skrip demo P2/P3 belum dibuat. Perintah tes yang disebut README belum menggantikan skrip penerimaan login, expiry, refresh, Media, dan beban.

## Urutan pekerjaan yang tersisa

1. Stage 4 (B-07 sampai B-12) telah diimplementasikan dan checkpoint HTTP Docker lolos. Review kode melalui peta Stage 4/5 di atas sebelum integrasi Stage 6.
2. Stage 5 (B-13 sampai B-16) sudah diimplementasikan dan checkpoint Docker HTTP/outage/recovery/log/burst lolos; review peta keputusan 1A/2A/3A sebelum integrasi berikutnya.
3. **B-18:** gunakan kontrol dan observabilitas Stage 5 untuk uji beban berkelanjutan; tinjau nilai timeout/batas konkurensi berdasarkan hasil P2.
4. **B-18:** uji beban >=50 koneksi selama >=60 detik, outage/recovery, latency quantile, error rate, dan 429 melalui API Klien.
5. **B-17/B-19/B-20:** verifikasi P3 penuh bersama A, finalisasi Compose/port dengan C, dan lengkapi bukti demo/laporan tim.

## Handoff yang diperlukan

| Pihak | Keputusan atau artefak yang dibutuhkan |
| --- | --- |
| **Anggota A** | Kontrak akhir endpoint Aggregator: path, filter, skema, arti `count`, error, autentikasi internal, dan perilaku stale/unavailable. Bukti silang kredensial BMKG/PVMBG untuk P3. |
| **Anggota C** | Service name, URL/port Auth, jaringan dan environment Compose, pilihan penyimpanan sesi bila eksternal, akses Docker dan infrastruktur k6. C tetap pemilik wiring Compose akhir. |
| **Tim / Anggota B** | Keputusan Stage 5 1A/2A/3A sudah dipilih dan diterapkan; nilai timeout/batas akhir ditinjau pada P2. Memori/introspeksi/tujuh field Media juga sudah diputuskan; jangan meminta persetujuan ulang. |

## Bukti dan batas verifikasi

- `go test -race -timeout 90s ./services/auth/... ./services/client-api/...` lolos pada 6 Oktober 2026, termasuk tes HTTP Auth dan middleware/adapter API Klien.
- Build Linux tanpa CGO, `go vet`, build Docker baru, dan binary/HTTP healthcheck kedua modul lolos pada Stage 4. Bukti fondasi Stage 2–3 tetap historis.
- Checker Stage 4 dengan `--natural-expiry` menghasilkan PASS: data BMKG/PVMBG nyata, tiga peran, tujuh/sebelas field, raw 403, token invalid 401, rotasi/replay, dan TTL alami default 60 detik (sisa waktu tunggu 59,828 detik). Evidence: [member-b-stage4-evidence.json](/C:/Users/ASUS/AppData/Local/Temp/member-b-stage4-evidence.json).
- Auth outage menghasilkan 503 dalam sekitar 3.968 ms; health API Klien/Aggregator tetap 200. Auth restart menolak token lama dengan 401 dan login baru bekerja. Correlation ID/latensi outbound terbukti pada tiga layanan; secret/token tidak ditemukan dalam log B. Evidence: [member-b-stage4-resilience-evidence.json](/C:/Users/ASUS/AppData/Local/Temp/member-b-stage4-resilience-evidence.json).
- Kedua image baru dan healthcheck Docker lolos. Checkpoint memakai enam layanan dengan Aggregator `--no-deps`; broker/consumer tidak diuji. Project `member-b-stage123` dihentikan tanpa menghapus volume. Tidak ada bukti P2 load, fanout baru, atau P3 lengkap lintas tim.
- Tidak ada commit/push baru. Dokumen lokal tetap untracked; Compose utama serta source A/C tidak diubah.

- Setelah audit bypass, alias checkpoint lama juga dilindungi. Tes race API Klien dan checker terbaru kembali lolos; alias aktif mengembalikan 401 tanpa token, 403 untuk raw Media, dan tujuh field untuk Media. Evidence: [member-b-protected-legacy-evidence.json](/C:/Users/ASUS/AppData/Local/Temp/member-b-protected-legacy-evidence.json) dan [member-b-stage4-final-http-evidence.json](/C:/Users/ASUS/AppData/Local/Temp/member-b-stage4-final-http-evidence.json). Bukti expiry alami tetap berasal dari checkpoint sebelumnya; perubahan alias tidak mengubah mekanisme token/expiry.
