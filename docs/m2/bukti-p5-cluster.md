# Bukti P5 (22 Nov 2026)

Waktu UTC. Cluster: image Aggregator `e4fef9948e36-dirty-readcap2`, Sensor Pantai `sustuna/sensor-pantai@sha256:51fbd90d…` (profil `practice`). Uji terisolasi dijalankan di `node-a` dari checkout `main` `e4fef99`.

## P5.1 — Katalog perilaku Sensor Pantai

Sumber: sampel HTTP hitam-kotak (`make p5-sample`, 30 s, speed 60, seed 42), DLQ cluster sejak 21 Nov 09:35, dan kasus gangguan yang diuji `make p5-resilience`.

| Perilaku | Teramati di | Penanganan |
|---|---|---|
| Endpoint tanpa token → `401` (`/v1/stations`) | sampel | Token dari Secret `pantai-token`; config menolak kredensial kosong (`TestConfigRejectsInvalidCredentialsAndEndpoints`) |
| Respons cepat dan sehat (30× `200`, 0,5–2,2 ms) | sampel | — |
| Observasi `quality != "ok"` | DLQ cluster: **941** item `contract_violation:quality`, 21 Nov 09:35 s/d 22 Nov 03:48 (masih terus muncul) | Disisihkan ke DLQ, observasi lain di halaman yang sama tetap diproses |
| `generated_at` mundur (sensor restart / dataset baru) | DLQ cluster: **1.111** item `contract_violation:generated_at_regressed`, 21 Nov 10:43–14:31 (image lama) | Sejak image 21 Nov sore: regresi dikonfirmasi lewat halaman tanpa cursor, lalu sesi direset (`internal/coastal/processor.go`), bukan di-park. Tidak ada item baru sejak 14:31 |
| Sensor diam / tidak terjangkau | cluster (P5.3 di bawah), `p5-ingestion` | Timeout, breaker `open`, data lama tetap dilayani dengan `stale:true` |
| `429`/`503` dengan `Retry-After` (detik, tanggal, rusak, `-1`) | `p5-resilience` | Retry-After dihormati tanpa menahan worker; hint rusak diabaikan |
| Redirect `302` | `p5-resilience` | Ditolak, tidak diikuti |
| Respons >10 MiB / >32 MiB | `p5-resilience` | Dipotong dan dicatat, tidak dibaca penuh ke memori |
| Field wajib hilang / `null` / tipe salah (7 field × 3 kasus) | `p5-resilience` | `contract_violation:<field>` |
| Observasi terlalu besar, terlalu dalam, kosong, timestamp rusak, overflow, skalar, UTF-8 rusak, field ganda | `p5-resilience` | Disisihkan; raw disimpan; observasi valid di sampingnya tetap diproses |
| Cursor terlalu panjang / tidak maju, `observation_id` ganda dengan isi berbeda | kode (`cursor_too_large`, `cursor_not_advancing`, `conflicting_observation_id`) | Disisihkan sebagai pelanggaran kontrak |

Sampel 30 s dengan profil practice tidak memunculkan burst, 5xx, atau latensi tinggi. Penyimpangan di atas diambil dari 18 jam operasi cluster dan dari uji gangguan sintetis.

## P5.2 — Ketahanan adapter (`make p5-resilience`)

03:44:01, **PASS**, 60 tes lulus dan 0 dilewati (`go test -race`, jaringan `none`). Termasuk:
- breaker terbuka, menolak, lalu pulih dengan satu probe half-open (`TestResilienceOpensRejectsAndRecoversWithSingleProbe`);
- bulkhead: pool HTTP terpisah, dan outage Pantai tidak menahan sumber lain (`TestResilienceRealHTTPOutageDoesNotBlockOtherSource`);
- retry budget dibagi antar-endpoint dan terisi ulang; jitter dalam batas; timeout nyata dengan retry terbatas;
- tidak me-retry kegagalan permanen atau kegagalan data; pembatalan saat backoff melepas permit.

## P5.3 — Breaker terbuka di cluster: Client API tetap melayani data coastal

Skrip: scale `deploy/sensor-pantai` ke 0, lalu kembalikan ke 1. Probe lewat Client API (`GET /hazards?source=SENSOR_PANTAI`, login Tim Lapangan) dan `GET /internal/v1/status` Aggregator. Log: `artifacts/m2/runs/20261122T035228Z-p5-breaker-open.log`.

| Waktu | Client API | `sources.SENSOR_PANTAI` | Breaker Aggregator |
|---|---|---|---|
| 03:52:29 baseline | 200, 2 event | `available:true`, `stale:false` | `closed`, 0 gagal |
| 03:52:30 | — sensor di-scale ke 0 — | | |
| 03:52:50 (+20 s) | **200, 2 event (data tersimpan)** | `available:false`, `stale:true`, `stale_since 03:52:33,6` | **`open`**, 4 gagal beruntun |
| 03:53:19 (+45 s) | 200, 2 event | `stale:true` | `open`, 6 gagal, `rejected` naik (probe dibatasi) |
| 03:53:21–22 | — sensor kembali dan ready — | | |
| 03:53:43 (+20 s) | 200 | `available:true`, `stale:false`, ingest 03:53:42,8 | **`closed`**, 0 gagal |

- Sumber dinyatakan stale ±3,9 s setelah pembacaan terakhir yang berhasil (`stale_after_seconds: 10`, dihitung dari ingest terakhir 03:52:29,7).
- Pulih tanpa intervensi operator dalam ≤20 s setelah sensor ready.

## P5.4 — Ingestion coastal end-to-end (`make p5-ingestion`)

Stack Compose terisolasi dengan image Sensor Pantai resmi, 03:46:15, **PASS** (`artifacts/m2/runs/20261122T034615Z-coastal-fbe31a/pantai-ingestion.json`):
- observasi coastal resmi sampai ke API yang dilindungi;
- ingestion coastal pulih setelah Aggregator restart;
- sensor dijeda → pembacaan data tersimpan bertanda stale;
- worker coastal pulih tanpa reset operator.

## P5.5 — SLO jalur peringatan

- SLO: **99% alert kritis Pantai di-ACK oleh notification-consumer ≤5 s** sejak masuk Aggregator.
- Panel Grafana "Overview": rasio ≤5 s dan p95 (panel 13–14), serta **sisa error budget 1 jam dan burn rate 5 m (panel 15–16, baru 22 Nov)**. Ekspresi panel diuji dengan `promtool test rules deploy/k8s/observability/tests/pantai-slo.test.yaml` → `SUCCESS`.
- Panel baru tampil setelah deploy observability berikutnya (`make deploy`).

## Belum dibuktikan

- ~~Game day gabungan belum dijalankan~~ — dijalankan 22 Nov siang, lihat "Uji lanjutan 22 Nov 2026 (siang)" di bawah. Profil ujian Sensor Pantai belum termasuk (dibagikan setelah tenggat).
- Latensi alert kritis ≤5 s di bawah beban 10k VU belum diukur bersamaan.

## Uji lanjutan 22 Nov 2026 (siang) — P5.1, P5.5, P5.6, bonus HPA

Waktu UTC. Semua uji dijalankan dari `node-a` (k6 dan `.env` yang sama dengan deploy). Artefak di `artifacts/m2/runs/` pada `node-a`.

### P5.1 — Alert kritis sampai dalam 11,6–40,4 s (temuan) dan perbaikannya

- Jurnal `notification-consumer` di cluster memuat **3 alert kritis Pantai** sepanjang sejarah, semuanya di awal sesi sensor 21 Nov 09:35: umur ingest→ACK **11.588 / 28.047 / 40.381 ms**. Target p95 ≤5 s **tidak terpenuhi**.
- Penyebab: outbox mengirim berurutan (`ORDER BY message_id LIMIT 50` per unit, digabung per unit), satu pesan per publisher confirm. Saat sensor pertama menyala, ribuan observasi coastal masuk sekaligus dan alert kritis antre di belakangnya; umur bertambah untuk setiap alert berikutnya.
- Perbaikan `ab9aea4` + `172c32d` (di-deploy 22 Nov): unit mengambil alert kritis Pantai (`source=SENSOR_PANTAI`, `attributes.critical=true`) lebih dulu, publisher mengurutkan ulang hasil gabungan lintas unit; urutan event lain tetap FIFO. Tes: `TestPendingOutboxPutsCriticalCoastalFirst` (PostgreSQL), `TestPrioritizeMovesCriticalAheadAndKeepsOrder`.
- Profil practice hanya mengirim alert kritis di awal sesi, jadi angka setelah perbaikan diukur saat ujian.

### P5.5 — Media dibanjiri, Tim Lapangan tetap dilayani (`make p5-priority`)

400 VU Media + 20 VU Tim Lapangan, 60 s, `GET /hazards?limit=20` lewat Client API NodePort.

| Run | Image Client API | Tim Lapangan 200 | Media ditolak terkendali | Error tak terkendali | HPA client-api |
|---|---|---|---|---|---|
| 06:50:43 | `a735ef472009` | **39,14%** (2.581/6.594) | 81,63% | 0,05% | 2 → 4 (CPU 331%/70%) |
| 07:20:12 | `bbb3d5e1f8f3` (fix `d271af3`) | **99,88%** (2.640/2.643), p95 1,39 s | 87,80% | 0,06% | 2 → 4 (CPU 497%/70%) |

- **Temuan run pertama:** semafor global 64 diambil sebelum peran diketahui dan ditahan selama introspeksi token. Banjir Media mengisi semua slot global, sehingga Tim Lapangan sudah ditolak 429 sebelum mencapai pool perannya.
- **Perbaikan `d271af3`:** jalur admisi cadangan untuk Tim Lapangan (20) dan Internal Ops (4) dipilih dari klaim `scope` JWT (belum diverifikasi; introspeksi tetap memutuskan akses, token palsu hanya menempati slot sesaat sebelum 401). Semafor global tidak berubah untuk token lain. Tes regresi `TestFieldTeamAdmittedWhileMediaFillsSharedLane`: lulus dengan kode baru, **gagal dengan kode lama** dengan `429 concurrency_limit` yang sama seperti di cluster.

### Bonus — kapasitas yang menyesuaikan diri (HPA)

HPA `client-api` (2–4 replika, CPU 70%) naik dari **2 ke 4 replika** di setiap run `p5-priority` (CPU 331–497%) dan pod baru tersebar ke node-a/node-c; turun kembali setelah beban selesai (CPU 1%). Aggregator, Auth, store, dan consumer sengaja tidak di-autoscale (leader election, sesi di memori, jurnal satu penulis).

### P5.6 — Game day (`make p5-gameday`), tanpa intervensi operator

Gangguan terjadwal di bawah beban (300 VU Media + 20 VU Tim Lapangan, 240 s): t+20 Tester `kill-store`, t+40 Sensor Pantai di-scale 0, t+60 PVMBG outage; t+150 gangguan berakhir (sensor kembali, outage dimatikan). Status Aggregator disampel tiap 10 s.

**Run 1 (07:27:06) — gagal, ditemukan masalah baru.** Tim Lapangan **0%** (81.496 request), error tak terkendali 97,9%, hampir semuanya **401**. Penyebab: **Auth OOMKilled** (limit 256Mi) pukul 07:26:59 saat 320 VU login bersamaan. Verifikasi Argon2id (64 MiB per hash) sudah berurutan, tetapi GC membiarkan sampah menumpuk melewati limit. Restart Auth menghapus semua sesi di memori sehingga semua token menjadi tidak valid.

**Perbaikan:** `GOMEMLIMIT=160MiB` pada Deployment Auth (`deploy/k8s/base/api.yaml`); skrip k6 login ulang saat menerima 401 seperti klien sungguhan.

**Run 2 (07:33:42) — lulus.**

| Waktu | Kejadian | Status Aggregator |
|---|---|---|
| 07:33:45 | sebelum gangguan | semua sumber `ok`, 3 unit `ok`, breaker `closed` |
| 07:34:0x | `kill-store` | storage tetap `ok`, under-replicated 0 |
| 07:34:3x | sensor 0 | Pantai `stale`, breaker `open` (07:35:45) |
| 07:34:4x | PVMBG outage | PVMBG `stale` |
| 07:36:1x | gangguan berakhir | — |
| 07:36:57 | pulih bertahap | BMKG, PVMBG `ok`; breaker `closed` |
| **07:38:07** | **pulih penuh** | **semua sumber `ok`**, 3 unit `ok`, under-replicated 0 |

- **Tim Lapangan tetap dilayani 99,74%** (12.681/12.713, p95 1,11 s) sepanjang gangguan; Media ditolak terkendali 90,11%; error tak terkendali **0,06%**; HPA 2→4.
- Auth **0 restart**; tidak ada restart Aggregator/Client API selama game day (restart yang tercatat berasal dari restart VM node-c pukul 07:24:58).
- Checksum sebelum → sesudah: BMKG 16.900 → 16.930, PVMBG 10.905 → 10.935, SATELIT 39.413 → 39.462, SENSOR_PANTAI 95.837 → 95.837, semuanya `partial:false`. Pengambilan checksum SATELIT tepat setelah beban sempat `partial:true` (baca checksum ±39 rb kebakaran melewati timeout 1 s per unit saat beban puncak); diulang setelah beban berhenti: lengkap, tidak ada data hilang.
- Banyak sampel status tercatat `status unavailable` saat beban puncak: endpoint status Aggregator melewati timeout `curl` 10 s. Status tidak dibatasi pembatas baca; penyebab pastinya belum diselidiki.

### Post-mortem game day (ringkas)

- **Deteksi:** sampel `/internal/v1/status` dan k6 menunjukkan Tim Lapangan 0% dengan 401; `kubectl` mencatat Auth `OOMKilled exit 137` pukul 07:26:59; kernel node-c `Memory cgroup out of memory … (service)`.
- **Dampak:** run 1 seluruh klien kehilangan sesi selama ±4 menit (semua request 401). Data kanonis tidak terdampak (checksum tetap lengkap). Run 2 setelah perbaikan: tidak ada dampak bagi Tim Lapangan.
- **Mitigasi otomatis yang bekerja:** breaker Pantai (open → closed tanpa reset), freshness `stale` untuk sumber yang mati, storage RF=2 saat `kill-store`, prioritas Tim Lapangan di Client API, HPA.
- **Perbaikan:** `GOMEMLIMIT` untuk Auth; klien login ulang saat 401. **Tindak lanjut:** sesi Auth masih di memori (restart tetap memaksa login ulang); verifikasi login sebaiknya dibatasi per detik agar lonjakan login tidak mendorong memori; selidiki endpoint status yang lambat di bawah beban.

## Uji lanjutan 22 Nov 2026 (sore) — skrip verifikasi PR #25

Waktu UTC. Skrip dari `main` `1b61c6f` (PR #25), dijalankan dari salinan terpisah di `node-a` (`~/m2-run-1b61c6f`). Cluster: Aggregator `sbimasena/bnpb-aggregator:bbb3d5e1f8f3` (leader `aggregator-577bb46457-cslh4`, epoch 80, tidak berganti selama uji), Sensor Pantai `sustuna/sensor-pantai@sha256:51fbd90d…`. Artefak di `artifacts/m2/runs/` pada `node-a`.

### Ringkasan

| Kriteria | Status | Hasil utama | Artefak | Sisa celah |
|---|---|---|---|---|
| P5.1 latensi alert kritis + zero loss (`make p5-critical-alert`) | **BLOCKED** | Jurnal 227.455 baris, hanya **3** alert kritis Pantai sepanjang sejarah (butuh ≥20); Prometheus 0 sampel ACK dalam 7 hari | `20261122T092013Z-p5-critical-alert-45edbd` | Profil practice hanya mengirim alert kritis di awal sesi; diukur saat ujian |
| P5.3 latensi BMKG saat Pantai mati (`make p5-source-latency SCALE_OUTAGE=1`) | **PASS** | Ulangan 09:41: p95 baca BMKG 117 / 128 / 119 ms (sebelum / saat ±60 s outage / sesudah), **360/360 HTTP 200** | `20261122T094152Z-p5-source-latency-2104cd` (ulangan), `20261122T092044Z-p5-source-latency-07b6e2` (pertama) | Mengukur jalur baca, bukan latensi ingest BMKG/PVMBG |
| P5.4 breaker, laju request, pemulihan (`make p5-breaker-timeline SCALE_OUTAGE=1`) | **PASS** | Ulangan 09:46: breaker `open` setelah 3 gagal, probe `half_open` dibatasi, `closed` ≤6 s setelah sensor kembali, retry +4 dari 125 attempt, Client API **25/25 HTTP 200**, p95 258 ms | `20261122T094609Z-p5-breaker-timeline-2f66c0` (ulangan), `20261122T092205Z-p5-breaker-timeline-175cfb` (pertama, FAIL skrip) | — |
| P5.8 SLO dan error budget (`make p5-slo-fetch`) | **BLOCKED** | Panel 13–16 `NO_DATA`: tidak ada alert kritis dalam jendela 5 m/1 j | `20261122T092019Z-p5-slo-fetch-a5a45e` | Kepatuhan SLO tidak bisa dievaluasi tanpa alert kritis |
| Pemulihan otomatis setelah sensor restart (PR #25, "Deferred") | **Teramati** | 2× `coastal session restarted` di log leader, tanpa reset operator | log leader | — |

### P5.1 — Alert kritis: belum cukup sampel

- Jendela 1 jam: 0 alert kritis. Jendela 7 hari: 3 alert (21 Nov 09:35, umur pra-append jurnal 11,6 / 28,0 / 40,4 s, sama dengan temuan di atas). Prometheus `notification_pantai_critical_age_seconds_count` = 0 dalam 7 hari karena metrik ACK baru ada setelah ketiga alert itu.
- Zero loss: `NOT_PROVEN`, tidak ada daftar ID pembanding independen.
- Skrip menolak memberi PASS dengan <20 alert. Klaim p95 ≤5 s **belum bisa dibuat** untuk cluster ini.
- **Mengapa restart sensor tidak menambah alert:** tiga restart sensor hari ini (09:21, 09:23, 09:47) tidak menghasilkan satu pun alert kritis baru. Sensor berjalan dengan `PANTAI_SEED=42`, jadi setiap sesi membangkitkan dataset yang sama (observasi kritis yang sama di virtual 00:30, 01:30, 02:30). Upsert Aggregator idempoten: hazard yang sama tidak membuat revisi, event outbox, maupun notifikasi baru. Ini perilaku yang benar (tidak ada notifikasi ganda), tetapi berarti profil practice dengan seed tetap hanya pernah menyediakan 3 alert kritis.
- Keterbatasan ini berasal dari **data sumber** (profil practice), bukan dari kapasitas hardware. Jalan keluar: (a) ganti `PANTAI_SEED` beberapa kali di cluster (±3 alert per seed, image tidak diubah) lalu kembalikan ke 42; (b) ukur saat ujian dengan profil `exam`. Untuk zero loss, `p5-critical-alert` kini bisa mencocokkan dengan daftar `observation_id` kritis langsung dari API sensor (`EXPECTED_KIND=source_ref_id`), independen dari Aggregator.

### P5.3 — Latensi baca BMKG saat Sensor Pantai mati

`GET /hazards?source=BMKG&limit=50` lewat Client API (ClusterIP, login Tim Lapangan), 40 sampel per fase, target p95 <300 ms. Sensor di-scale 1 → 0 (ready 0 terkonfirmasi), lalu dikembalikan ke 1 di blok `finally` (ready 1 terkonfirmasi).

| Fase | min | p50 | p95 | max | HTTP |
|---|---|---|---|---|---|
| Sebelum | 2,6 ms | 13,3 ms | 108,2 ms | 157,7 ms | 40× 200 |
| Saat sensor 0 replika | 2,4 ms | 34,7 ms | **83,4 ms** | 114,0 ms | 40× 200 |
| Sesudah pulih | 2,7 ms | 12,4 ms | 78,9 ms | 128,5 ms | 40× 200 |

Bulkhead bekerja: outage Pantai tidak menaikkan latensi atau error baca sumber lain. Sampel BMKG ini mengukur jalur baca Client API, bukan latensi ingest BMKG/PVMBG. Status ingest kedua sumber tidak disampel selama uji; dicek sesudahnya pukul 09:26:30, keduanya `stale:false` dengan ingest terakhir <1 s sebelumnya.

### P5.4 — Breaker Pantai di cluster (dipoll langsung ke pod leader)

`/internal/v1/status` dipoll tiap ±6 s langsung ke IP pod leader (field `coastal.breaker` hanya terisi di leader), sambil membaca `GET /hazards?source=SENSOR_PANTAI` lewat Client API.

| Waktu | Kejadian | Breaker | Gagal beruntun | `rejected` | Client API |
|---|---|---|---|---|---|
| 09:22:12–09:22:36 | baseline | `closed` | 0 | 25 | 200 |
| 09:22:3x | sensor di-scale 0 | | | | |
| 09:22:42 | timeout/transport gagal | `closed` | 2 | 25 | 200 |
| 09:22:48 | **breaker terbuka** | `open` | 3 | 26 | 200 (data tersimpan) |
| 09:23:00 | outage berlanjut, probe dibatasi | `open` | 4 → 8 | 27 → 31 | 200, lalu 401 (lihat catatan) |
| 09:23:4x | sensor kembali, ready | | | | |
| 09:23:50 | log: `coastal session restarted` (generated_at 01:26:13 → 00:03:40) | | | | |
| **09:23:54** | **pulih** | `closed` | 0 | 31 | — |
| 09:24:37–39 | sensor sendiri membalas `HTTP 503` 3× | `open` | 3 | 32 | — |

- **Tidak ada retry storm:** sepanjang outage, `attempts` naik 131 (hampir semuanya poll normal sebelum/sesudah outage) dan `retries` hanya +4; saat breaker terbuka, worker mencoba ±1 probe per 12 s (log `sensor request timed out` → `circuit_open` berpasangan tiap 12 s).
- **Pemulihan:** ≤6 s setelah sensor ready, tanpa intervensi operator. Sumber `available:true`, `stale:false`.
- **Client API saat outage:** 200 dengan data coastal tersimpan, p95 194,9 ms (10 sampel sebelum token habis).
- **Mengapa skrip melaporkan FAIL:**
  1. `Client API returned a non-200 status`: semua 401 sejak 09:23:06 terjadi karena token Tim Lapangan uji kedaluwarsa (`ACCESS_TOKEN_TTL_SECONDS=60`) dan skrip tidak login ulang. Ini keterbatasan skrip, bukan perilaku sistem. Klien sungguhan memakai refresh token.
  2. `breaker did not return to closed`: poll terakhir (09:24:40) jatuh tepat saat sensor membalas 503 ±47 s setelah sesi barunya dimulai (pola yang sama terlihat 09:21:59–09:22:02 setelah uji P5.3). Membuka breaker untuk 503 adalah perilaku yang benar; breaker sudah `closed` lagi saat dicek setelahnya.
- **Perbaikan skrip** (setelah run pertama): `p5-source-latency` dan `p5-breaker-timeline` menerima `AUTH_URL` dan login ulang saat 401 (`verify_common.Session`, jumlah login ulang dicatat di `report.json`); `recovered` dinilai dari fase recovery ("pernah `closed` setelah sensor kembali"), bukan poll terakhir, dan `open` sisa uji sebelumnya di baseline tidak dihitung. Tes regresi di `tests/test_p5_breaker_timeline.py` dan `tests/test_verify_common.py`.

#### Ulangan setelah perbaikan skrip (09:41–09:48 UTC, leader `aggregator-577bb46457-792cp`, epoch 81)

P5.3 (`--samples 120 --interval 0.5`, outage ±60 s): 360/360 HTTP 200, 3× login ulang otomatis, **PASS**.

| Fase | min | p50 | p95 | max |
|---|---|---|---|---|
| Sebelum | 2,5 ms | 37,9 ms | 117,1 ms | 175,4 ms |
| Saat sensor 0 replika | 2,5 ms | 38,4 ms | **128,0 ms** | 317,5 ms |
| Sesudah pulih | 2,3 ms | 35,9 ms | 118,8 ms | 259,0 ms |

P5.4 (`make p5-breaker-timeline AUTH_URL=… SCALE_OUTAGE=1`, poll langsung ke pod leader), **PASS**:

| Waktu | Breaker | Gagal beruntun | `rejected` | Client API |
|---|---|---|---|---|
| 09:46:09–09:46:39 baseline | `closed` | 0 | 7 | 200 |
| 09:46:45 sensor 0 replika | `closed` | 1 → 2 | 7 | 200 |
| 09:46:57 | **`open`** | 3 | 8 | 200 (data tersimpan) |
| 09:47:03–09:47:15 | `half_open` ↔ `open` (satu probe, gagal) | 3 → 4 | 8 → 9 | 200 (maks 596 ms) |
| 09:47:21–09:47:48 | `open` | 5 → 7 | 10 → 12 | 200 |
| 09:47:50 | log: `coastal session restarted` (generated_at 02:33:36 → 00:10:07) | | | |
| **09:47:54** | **`closed`**, sumber `available` | 0 | 12 | 200 |
| 09:48:30 | sensor membalas `HTTP 503` 3× (log), breaker `open` lagi, lalu `closed` | 3 | 13 | 200 |

Retry selama ±75 s outage hanya +2 (5 → 7), probe dibatasi satu per jendela `half_open`: tidak ada retry storm. Client API 25/25 HTTP 200 dengan 2× login ulang. Pola 503 ±45 s setelah sesi baru muncul di ketiga restart hari ini dan berasal dari sensor (perilaku profil practice), bukan Aggregator.

- Log kegagalan sekarang menyebut alasan tanpa membocorkan token/cursor/payload, misalnya `reason: "pantai: upstream HTTP 503"`, `"sensor request timed out"`, `"sensor transport failed"`, `"pantai: circuit_open"`.

### Pemulihan setelah sensor restart (teramati di cluster)

Setiap kali sensor kembali dari 0 replika, sensor memulai dataset baru dengan `generated_at` mundur. Log leader mencatat `coastal session restarted` pada 09:21:15 (16 Nov 08:39:33 → 12 Nov 00:06:05) dan 09:23:50 (12 Nov 01:26:13 → 00:03:40); ingest coastal langsung lanjut tanpa reset checkpoint manual, dan Audit API mencatat **0 item DLQ coastal baru** sejak 09:20 (dari 2.053 item lama; dulu setiap restart menghasilkan ratusan `generated_at_regressed`). Tes: `TestCoastalRecoversFromSensorRestart`, `TestCoastalDoesNotResetWithoutValidRegressedHead`.

### P5.8 — SLO dan error budget

Query panel 13–16 dijalankan langsung ke Prometheus cluster pukul 09:20:19: keempatnya `NO_DATA` (penyebut kosong), jadi **kepatuhan SLO tidak bisa dievaluasi** pada jendela ini, sesuai aturan "tanpa alert kritis, tidak ada klaim". Cross-check jurnal: 3 alert kritis, 0 di bawah 5 s (p95 pra-append 40,4 s), semuanya sebelum perbaikan prioritas outbox (`ab9aea4`, `172c32d`).

### Tes Go

`go test -race ./...` Aggregator dengan PostgreSQL asli di `node-a`: 13/13 paket ok, 232 PASS, 4 dilewati (butuh RabbitMQ/feed live). Termasuk tes baru PR #25: `TestCoastalRecoversFromSensorRestart`, `TestCoastalDoesNotResetWithoutValidRegressedHead`, `TestCoastalFailureSummaryOmitsRawErrorDetails`, dan `TestPendingOutboxPutsCriticalCoastalFirst`.

### Catatan operasional

- `node-a` kehilangan akses internet sejak sebelum 09:13 (DNS dan ping 1.1.1.1 gagal; node-b/node-c normal), mirip insiden node-c pagi ini. Agar Sensor Pantai bisa dijadwalkan ulang ke `node-a` saat uji scale, image `sustuna/sensor-pantai@sha256:51fbd90d…` disalin dari containerd node-c ke node-a (`ctr images export | import`); image tidak diubah. Pod sensor tetap jatuh ke node-c.

### Insiden node-c tanpa internet (22 Nov 07:0x–07:25)

- Gejala: node-c tidak bisa menjangkau router, internet, maupun DNS (jaringan lokal ke node-a tetap jalan). Deploy `bbb3d5e` macet `ImagePullBackOff` untuk 4 pod di node-c, dan leader Aggregator yang kebetulan di node-c tidak bisa membuka sesi Feed Satelit (`session request failed … context deadline exceeded`).
- Penanganan sementara: image disalin dari containerd node-a ke node-c (`ctr images export | import`). Penanganan akhir: VM node-c di-restart; internet pulih, leader pindah ke node-a, reconciler mengembalikan partisi ke store-2 dan menyapu salinan lama; semua sumber `ok` dan peta 16/16/16 pada 07:26:21 (±50 s setelah leader baru).

## P5 kriteria 7 — korelasi metrik, trace, dan log (`make p57-evidence`, 22 Nov 11:14 UTC)

Read-only, `main` `0e71af8` (PR #28), jendela 20 menit. Artefak: `artifacts/m2/runs/20261122T111430Z-p57-evidence-7df3b7/` di `node-a`.

| Sinyal | Hasil |
|---|---|
| Prometheus | `up{job="bnpb"}` = 5 target; laju request 0,80/s (HTTP 200); p95 request 39 ms |
| Jaeger | 100 trace kandidat; trace `45e12466d23fb417ee6dccdeb1dd16d9` melintasi **3 service**: `aggregator` `outbox publish` 19,2 ms → `dashboard-consumer` `consume dashboard` 0,96 ms dan `notification-consumer` `consume notification` 0,90 ms (span consumer ber-parent span publish) |
| Log | `correlation_id` trace yang sama (`satellite-82-2619022`) muncul di log `notification-consumer` dan `dashboard-consumer` (`event handled`, `hazard_id`, `hazard_revision`) pukul 11:14:11, sama dengan waktu span. Log Aggregator tidak mencatat publish per event di level INFO |

Ini membuktikan ketiga sinyal dapat dikorelasikan lewat `trace_id`/`correlation_id`. Kriteria 7 meminta penjelasan **kegagalan yang sedang berlangsung**; contoh dengan kegagalan nyata yang sudah terdokumentasi adalah post-mortem game day (Auth OOMKilled: metrik, `kubectl` exit 137, dan log k6) dan timeline breaker P5.4 (log `coastal cycle deferred` dengan `reason`). Demonstrasi satu kegagalan lengkap dengan trace di Jaeger dijadwalkan di video. Status: **PARTIAL**.

## P5 kriteria 3 — BMKG dan PVMBG tetap memenuhi latensi M1 saat Sensor Pantai mati (`make p53-concurrent`, 22 Nov 11:21–11:24 UTC) — **PASS**

`main` `0e71af8` (PR #28), context `default`. Skrip memasang `PVMBG_DELAY_MS=3000` (mock PVMBG lambat 3 s, terkonfirmasi lewat `/health`), lalu tiga fase 45 s dengan dua aliran baca serentak lewat Client API (login Tim Lapangan): `GET /hazards?source=BMKG` dan `GET /hazards?source=PVMBG`. Sensor Pantai di-scale 1 → 0 (ready 0 terkonfirmasi) pada fase kedua. Artefak: `artifacts/m2/runs/20261122T112129Z-p53-concurrent-a1b07e/` di `node-a`.

| Fase | BMKG: n, p50 / p95 / p99 | PVMBG: n, p50 / p95 / p99 | HTTP |
|---|---|---|---|
| Sebelum (PVMBG 3 s) | 376; 11,5 / 44,3 / 77,9 ms | 374; 16,6 / 44,8 / 84,1 ms | semua 200 |
| Sensor 0 replika | 365; 18,0 / **54,1** / 107,3 ms | 363; 20,5 / **53,6** / 113,2 ms | semua 200 |
| Sesudah pulih | 371; 20,7 / 38,6 / 85,9 ms | 372; 20,2 / 40,5 / 67,3 ms | semua 200 |

- Target M1 p95 < 300 ms terpenuhi di semua fase untuk kedua sumber; 2.221/2.221 request HTTP 200.
- Pemulihan otomatis terverifikasi: `PVMBG_DELAY_MS` kembali ke 750 ms (override env dihapus, nilai ConfigMap berlaku lagi), sensor dan PVMBG 1/1 ready pukul 11:23:48.
- Baca dilayani dari Canonical Store, sehingga upstream PVMBG yang lambat dan Sensor Pantai yang mati tidak menambah latensi baca sumber lain. Latensi ingest BMKG/PVMBG selama gangguan tidak diukur oleh skrip ini.
