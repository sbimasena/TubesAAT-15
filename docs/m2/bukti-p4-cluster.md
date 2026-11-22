# Bukti P4 di cluster nyata (21 Nov 2026)

Branch: `feat/m2-p2-p4-acceptance`. Semua waktu UTC.

## Unit test (`p4-check` / `go test -race`)

Dijalankan dari mesin lokal (Windows, Go 1.27.4) tanpa TEST_DATABASE_URL:

```
go test -race -p 1 -count=1 -v ./services/aggregator/internal/satellite/...
```

**PASS** — semua tes berikut lulus:
- `TestIncrementalClustersMatchFullRebuild` (0.13 s)
- `TestSSEPreservesDataAndSkipsCommittedOffsets`
- `TestSSERejectsGapsAndStopsAtFailedCommit`
- `TestStreamResumesFromDurableOffset` (0.56 s)
- `TestIdleStreamIsCancelled` (0.02 s)
- `TestStreamDoesNotRetryCommitFailureOrUnauthorized` (200, 401)
- `TestSessionValidatesRequiredProgressFields`
- `TestPracticeFeedPatches`
- `TestPracticeFeedRejectsOffsetMismatch`

SKIP (butuh `TEST_DATABASE_URL`):
- `TestWorkerSSEReplayThroughPostgres`
- `TestWorkerSyntheticBurstCatchUp`

Total waktu: 1.787 s.

## Status satellite di cluster

Deployment aggregator mempunyai env `SATELLITE_TOKEN` (dari secret `satellite-token`, ada sejak 45 jam). Namun pada `GET /internal/v1/status` pukul 06:41:47 UTC, `satellite.state = "no_checkpoint"` — leader baru `tzrqp` (epoch 16) belum menginisialisasi worker.

Kemungkinan penyebab: token kosong di secret, atau worker gagal saat startup setelah failover (perlu log lanjutan). Investigasi lanjutan diperlukan sebelum bukti live feed dapat dikumpulkan.

## `make p4-export`

Dijalankan dari node-a (ops.sh p4-export, python3 + kubectl port-forward ke svc/aggregator:8083):
```
[]
```
Hasil kosong — konsisten dengan `satellite.state = "no_checkpoint"` (belum ada event SATELIT di Audit API).

## Belum dibuktikan

| Item | Keterangan |
|---|---|
| Live feed 60× | Worker satellite belum aktif di cluster |
| Peak lag, p95 latency, catch-up ≤60 s | Memerlukan worker aktif + feed aktif |
| Disconnect + recovery checksum | Memerlukan worker aktif |
| `p4-compare` dengan referensi | Referensi asisten belum dirilis |
| Database recovery test (`TestWorkerSSEReplayThroughPostgres`) | Butuh `TEST_DATABASE_URL` disposable PostgreSQL |

## Investigasi `no_checkpoint`

Diperiksa 21 Nov ~07:00 UTC. Secret `satellite-token` ada (45 jam) tapi worker tidak aktif. Env var `SATELLITE_TOKEN` terpasang di deployment aggregator. Status GET `/internal/v1/status`: `satellite.state = "no_checkpoint"`.

Dari `p4-satellite.md`: "The Aggregator starts the worker only for the current leader when `SATELLITE_TOKEN` is present." Juga: "Docker Compose reads the ignored `.env`. Kubernetes mounts the optional `satellite-token` Secret, which `scripts/k8s/deploy.sh` creates from `.env`."

Kemungkinan penyebab (belum dikonfirmasi):
1. Secret `satellite-token` berisi token kosong atau tidak valid — perlu `kubectl get secret satellite-token -o jsonpath='{.data.SATELLITE_TOKEN}' | base64 -d`.
2. Worker gagal saat startup setelah failover (epoch 16) — cek log dengan `kubectl logs ... | grep satellite`.

## Langkah selanjutnya

1. `kubectl -n sistem-informasi-kebencanaan-bnpb get secret satellite-token -o jsonpath='{.data.SATELLITE_TOKEN}' | base64 -d` — verifikasi isi token (jangan tampilkan ke log).
2. Kalau kosong: isi `.env` → `make deploy OVERLAY=cluster`.
3. Setelah worker aktif: jalankan `make p4-export` dan `make p4-compare` ketika referensi asisten tersedia.

## Uji ulang 21 Nov 2026 — branch gabungan `c55ae39`

- `SATELLITE_TOKEN` diisi di `~/TubesAAT-15/.env` node-a (43 karakter, tanpa kutip/spasi/CR). Setelah deploy, Secret `satellite-token` key `token` = 43 byte. Perintah cek yang benar: `kubectl -n sistem-informasi-kebencanaan-bnpb get secret satellite-token -o jsonpath='{.data.token}' | base64 -d | wc -c` (key-nya `token`, bukan `SATELLITE_TOKEN` seperti di "Langkah selanjutnya" di atas).
- Status leader 10:53:48: `satellite.state = "checkpoint_present"`, `processed_offset = -1`, `durable_offset = -1`. Worker mulai tetapi tidak pernah mendapat sesi: log leader `Satellite session unavailable ... session request failed` tiap ±5 s (19 kali dalam ±1,5 menit setelah failover 11:00).
- `GET https://satellite.naufarrel.dev/v1/session` dengan token itu:
  - dari host node-c: `401` dalam 0,36 s (IPv4); IPv6 tidak punya rute.
  - dari pod busybox di node-c: percobaan pertama tidak dijawab hingga timeout 8 s, percobaan kedua `401 Unauthorized` dalam 1 s.
- Kesimpulan: (1) feed menolak token (`401`); perlu dicek ke asisten apakah token benar/aktif. (2) Koneksi keluar dari pod di node-c kadang macet melewati batas 5 s `Session` (`internal/satellite/stream.go:242`), dan error transport disembunyikan menjadi `session request failed` (`stream.go:251`).
- `make p4-sample` tidak dijalankan: memakai token yang sama dan feed menolak koneksi ganda, jadi akan berebut dengan worker leader.
- `make p4-export` belum bermakna sampai worker mendapat sesi.

## Uji ulang 21 Nov 2026 (lanjutan) — Satellite berjalan penuh, image `58405634daf3`

Waktu UTC. Rincian sebab-akibat ada di `temuan-pr22.md` temuan 9 dan 15.

### Penyebab worker tidak pernah mendapat sesi

Kesimpulan di bagian sebelumnya hanya separuh benar. Setelah log error transport tidak lagi disembunyikan (`bf746f4`), leader mencatat `tls: failed to verify certificate: x509: certificate signed by unknown authority`. Image aggregator `FROM scratch` tidak membawa CA bundle, sedangkan feed memakai sertifikat Let's Encrypt, jadi worker **tidak pernah bisa** tersambung dari cluster apa pun token-nya. Diperbaiki di `1fe072d` (salin `/etc/ssl/certs/ca-certificates.crt` dari stage builder); setelah itu leader mendapat `401`. Token di `.env` ternyata salah ketik; setelah diganti (15:05) `GET /v1/session` → `200`.

### Run pertama (image `872f2545518d`) dan kegagalannya

- 15:05 worker mulai dari offset 0 (checkpoint -1). `GET /v1/session`: `profile practice`, `speed 60`, `latest_offset` 2.032.416, `virtual_now` 2027-05-05. Feed bertambah ±5 offset/s.
- Laju turun seiring offset: ±41–90/s (offset 8–14 rb), ±32/s (40 rb). Macet di offset 49.009 sejak 15:21:35: `store: checkpoint acknowledged by 1 units; need 2` tiap 10 s.
- Penyebab: checkpoint utuh 15,5 MB (`Detections` 7,9 MB / 42.316, `Owners` 3,8 MB, `Seen` 2,0 MB, `Events` 1,8 MB) ditulis ulang 2× per batch ke 3 store. Dampaknya ikut ke store lain (lihat `bukti-p1-cluster.md`). Worker dimatikan 15:27 dengan mengosongkan Secret di cluster.
- Failover selama run ini (`make p2-kill-leader` 15:07:20): leader baru ±12 s, lanjut dari offset 9.457.

### Pengukuran perilaku feed

Rekaman 178.336 offset pertama (60 s stream dari offset 0, worker mati agar tidak ada koneksi ganda): 165.788 deteksi, 12.340 update/retract, rentang waktu deteksi 2026-11-11 20:49 sampai 2026-11-27 03:36.

| | p50 | p90 | p99 | p99,9 | max |
|---|---|---|---|---|---|
| Keterlambatan deteksi terhadap waktu event maksimum (jam) | 0,08 | 0,7 | 3,35 | 3,9 | 4,48 |
| Umur key update/retract (jam) | 0,15 | 0,83 | 3,45 | 4,1 | 5,13 |

`SATELLITE_LATENESS_SECONDS` = 86.400 (24 jam) menutup semua keterlambatan yang teramati. Feed juga mengirim ±15 koordinat dekat khatulistiwa dalam notasi eksponen (`"latitude":-2.1e-4`) yang di key update ditulis `-0.00021`; kode lama berhenti permanen di offset 162.263 karenanya (diperbaiki `ded37c0`).

### Perbaikan dan verifikasi offline (`4db80ff`, `ded37c0`, `5840563`)

Pemangkasan kebakaran yang sudah final (berdasarkan watermark, deterministik), checkpoint base + delta (`SATELIT` + `SATELIT_DELTA`), dan batch hingga 4.096 frame dengan jendela 1 s.

- Replay 178.336 offset rekaman asli lewat processor baru: selesai 20 s, base maksimum 5,98 MB (akhir 5,46 MB, 15.511 deteksi aktif), `late_detections` 0.
- Dibandingkan state machine tanpa pemangkasan (lateness 10.000 jam, 151.760 deteksi): **2.797 vs 2.797 kebakaran, 0 hilang, 0 lebih, 0 berbeda**.
- Simulasi live (8.000 offset terakhir dalam batch 5 frame): rata-rata 226 KB ditulis per batch (sebelumnya ±11 MB), ±32 ms proses per batch.
- `go test -race` + PostgreSQL untuk seluruh paket aggregator lulus di pod node-b. `TestRecordedPracticeReplay` dengan 3.000 frame pertama rekaman asli: hasil dengan restart di tengah identik dengan tanpa gangguan (47 kebakaran, checksum `7fc04863…`).

### Run di cluster (image `58405634daf3`, deploy 16:24)

Checkpoint lama (±14,6 MB per store) dihapus dari `ingest_cursor` sebelum deploy, jadi worker memutar ulang seluruh sesi practice dari offset 0.

| Waktu | Offset | Base / delta | Catatan |
|---|---|---|---|
| 16:25:20 | 4.990 | 1,45 / 0,04 MB | |
| 16:26:21 | 18.487 | 5,40 MB | ±225 offset/s |
| 16:28:24 | 42.096 | 5,17 / 0,12 MB | base mulai turun (pemangkasan aktif) |
| 16:29:25 | 57.464 | 5,38 MB | melewati titik macet run pertama |
| 16:30:27 | 72.924 | | `make p2-kill-leader` |
| 16:30:49 | 73.752 | | leader baru melanjutkan dari base + delta (±20 s) |
| 16:36:04 | 361.567 | 5,81 MB | ±930 offset/s |
| 17:01:15 | 1.737.407 | 5,67 MB | |
| **17:07:44** | **2.087.227 / 2.087.235** | | **catch-up selesai** (±43 menit) |
| 17:35:40 | 2.100.942 / 2.101.041 | 5,37 / 0,09 MB | mengikuti feed live setelah beberapa rollout |

Sepanjang catch-up: `late_detections` 0, 0 warning non-Satellite per menit, BMKG/PVMBG/Pantai `stale:false` (Pantai sempat tertangkap `stale` 2× sesaat; 20 sampel tiap 2 s menunjukkan umur data ≤2,3 s).

### `make p4-export` (17:09)

- **32.763 kebakaran**: AWAS 32.616, SIAGA 20, WASPADA 127. Total 1.344.979 deteksi (p50 41, p90 49, max 98 per kebakaran), `occurred_at` 2026-11-11T20:49 sampai 2027-05-09T15:56. Dominasi AWAS sesuai aturan spesifikasi (AWAS bila ≥20 deteksi).
- File: `~/p4-evidence/satellite-export-20261121T170901Z.json` di node-a (87 MB, tidak di-commit).
- Struktur lolos validator `make p4-compare` (export terhadap dirinya sendiri: 0 missing/extra/mismatch, tidak ada keanggotaan ganda).
- Konsisten dengan replay offline: 2.797 kebakaran per 178 rb offset × 11,7 ≈ 32,7 rb.
- Percobaan pertama gagal `Audit storage is degraded` (satu halaman `limit=1000` ±2,1 MB sesaat `partial` walau ketiga unit sehat). Penelusuran ulang 33 halaman bersih, percobaan berikutnya lolos.

### Status "Belum dibuktikan" (diperbarui)

| Item | Status |
|---|---|
| Worker aktif di cluster | **Terbukti** (di atas) |
| Catch-up dan "kembali mutakhir" | Catch-up 2,09 jt offset dalam ±43 menit; kriteria ≤60 s setelah lonjakan belum diukur terpisah |
| Disconnect/failover + recovery | **Terbukti** dua kali (15:07 dan 16:30): lanjut dari offset durable, tidak dari 0 |
| p95 latensi data → HazardEvent | Belum diukur di cluster |
| `p4-compare` dengan referensi | Menunggu referensi asisten |

## Uji ulang 22 Nov 2026 — PR #27 (`c936b23`)

Waktu UTC. PR #27 mengubah dua hal P4: `detection_keys` di Audit menyimpan koordinat **apa adanya dari feed** (`-2.1e-4`, bukan `-0.00021`; sesuai `kontrak-m2.md` "diambil apa adanya dari feed tanpa pembulatan"), sementara update/retract dari feed tetap dicocokkan lewat bentuk desimal; dan total singleton (`single_detections`) kini menghitung singleton aktif **dan** yang sudah difinalisasi/di-prune, serta bertahan setelah restart.

### `p4-check` dengan PostgreSQL asli (10:11)

Clone `main` `c936b23` di `node-a`, `go test -race -p 1 -count=1 -v ./internal/satellite ./internal/store` dalam `golang:1.27.1` dengan `TEST_DATABASE_URL` ke `postgres:17-alpine` sekali pakai, ditambah `python3 -m unittest discover -s tests -p 'test_satellite_*.py'`. Log: `artifacts/m2/runs/20261122T101125Z-p4-check.log` di `node-a`.

| Bagian | Hasil |
|---|---|
| Go `satellite` + `store` (`-race`, PostgreSQL) | **ok**, 62 PASS, 2 SKIP |
| Python `test_satellite_*` | **OK**, 11 tes |
| Seluruh paket Aggregator (`go test -race ./...`, laptop) | 13/13 ok |

Tes yang relevan dengan PR #27:
- `TestExponentCoordinateMatchesFeedUpdateKey`: detection `-2.1e-4` membentuk fire; key Audit tetap `N20|2026-11-25|1704|-2.1e-4|110.82748`; update dan retract dengan key feed `-0.00021` tetap merevisi lalu menarik fire yang sama, juga setelah checkpoint di-serialisasi ulang.
- `TestSingletonTotalSurvivesPruningAndRestore`: setelah detection lama di-prune dan processor di-restore, `single_detections` = 2 (1 aktif + 1 final), bukan 1.
- `TestWorkerSyntheticBurstCatchUp`: dua lonjakan total 2.000 detection membentuk 100 fire; fire lonjakan pertama tetap bisa di-query setelah detection-nya di-prune dari checkpoint.
- `TestWorkerSSEReplayThroughPostgres`, `TestProcessorRecoversPendingEffectsFromPostgres`: replay dan pemulihan efek tertunda lewat PostgreSQL tetap lulus.

SKIP: `TestCoursePracticeFeed` (butuh `SATELLITE_LIVE_TEST=1`) dan `TestRecordedPracticeReplay` (butuh rekaman `stream.sse`). Rekaman tidak tersedia, dan merekam ulang lewat `make p4-sample` akan berebut satu-satunya koneksi feed kelompok dengan leader di cluster, jadi sengaja tidak dilakukan.

### Deploy `c936b23` ke cluster (22 Nov ±10:13–10:25 UTC)

`make deploy OVERLAY=cluster REGISTRY=docker.io/sbimasena` dari clone bersih `main` `c936b23` di `node-a` (log `~/deploy-c936b23.log`, `deployed c936b23171d7`, exit 0). Semua image layanan kelompok kini `docker.io/sbimasena/bnpb-*:c936b23171d7`; Sensor Pantai tetap `sustuna/sensor-pantai@sha256:51fbd90d…`. Tidak ada replay feed.

| Pemeriksaan | Sebelum deploy | Sesudah deploy |
|---|---|---|
| Pod | — | semua `1/1 Running`, leader baru `aggregator-75766ccc86-l8r46` |
| Satelit `durable_offset` | 2.590.592 | 2.593.066 → 2.593.274 dalam 40 s (lanjut dari checkpoint, **bukan dari 0**), `pending_effects` 2 → 0 |
| `single_detections` | 1.850 (hanya singleton aktif) | 1.879 → 1.906 (aktif + yang difinalisasi sejak deploy) |
| Freshness BMKG / PVMBG / SENSOR_PANTAI | `stale:false` | `stale:false` |
| Checksum BMKG | 17.891 | 17.926, `partial:false` |
| Checksum PVMBG | 11.884 | 11.919, `partial:false` |
| Checksum SATELIT | 27.060, `partial:true` (baca melewati timeout) | 40.697, `partial:false` |
| Checksum SENSOR_PANTAI | 95.837 | 95.837, `partial:false` |

### p95 latensi data masuk → HazardEvent tersimpan (kriteria 4), Prometheus 10:27 UTC

Metrik `bnpb_satellite_ingest_duration_seconds`: per efek hazard, dari frame pertama batch diterima sampai commit replikasi dan checkpoint selesai.

| Jendela | Jumlah efek | p50 | p95 |
|---|---|---|---|
| 1 jam terakhir (mencakup deploy) | 6.693 | 1,55 s | **3,63 s** |
| 6 jam terakhir | 40.359 | — | **3,82 s** |

Offset lag (`bnpb_satellite_offset_lag`): maksimum 15 offset dalam 1 jam terakhir (termasuk saat pergantian leader akibat deploy), 0 saat diukur. Pengukuran ini pada laju feed latihan yang sedang berjalan, bukan lonjakan 60× terpisah; "kembali mutakhir ≤60 s setelah lonjakan" tetap perlu diukur saat sesi ujian.

### Batasan

- Checkpoint sebelum deploy tidak memuat singleton yang sudah di-prune sebelumnya, jadi `single_detections` hanya lengkap untuk data sejak deploy; total historis butuh **replay penuh** feed latihan. Replay sengaja tidak dilakukan: sesi ujian (`POST /v1/exam/start`) membawa data baru, dan perilakunya sudah dibuktikan tes di atas.
- ±15 key fire lama berkoordinat eksponen tetap dalam bentuk desimal sampai di-replay; detection baru sejak deploy disimpan apa adanya.
