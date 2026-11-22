# Bukti P3 (22 Nov 2026)

Waktu UTC. Cluster: image Aggregator `e4fef9948e36-dirty-readcap2` (commit `e4fef99` + batas baca serentak, lihat `bukti-p2-cluster.md`). Uji terisolasi dijalankan di `node-a` dari checkout `main` `e4fef99`.

## P3.1 — Skema PVMBG berubah di cluster (Tester Agent `schema`)

`make tester S=schema`, 03:56:12.

| Waktu | Kejadian |
|---|---|
| 03:56:18 | Tester: `call_mock_admin POST /admin/schema-version -> HTTP 200`, lalu "pemulihan akhir selesai; keadaan bersih" |
| 03:56:26 | Mock PVMBG menerbitkan laporan pertama `schema_version: 2` (`PVMBG-1795319786290708230`) |
| 03:56:26,9 | Laporan itu tersimpan di Canonical Store: `haz_b476162562e0d26770fb01e079ad184b`, `attributes.confidence_level` ikut terbawa |

- Payload practice Tester Agent = **versi 2** (field aditif). Sesuai kebijakan skema di README, perubahan ini **diterima otomatis**; tidak ada pesan PVMBG baru di DLQ sejak 03:56:12 (0 item).
- Pertanyaan terbuka #4 (payload agent) terjawab untuk profil practice: agent tidak memakai mode tidak kompatibel.

## P3.2 — Perubahan tidak kompatibel di cluster → DLQ, cursor tetap maju

Dipicu manual (port-forward `svc/pvmbg`), 03:56:52:

```
POST /admin/schema-version {"mode":"rename_field"}
→ {"report_id":"PVMBG-1795319812369110363","schema_mode":"rename_field","schema_version":3}
```

- 03:56:53,9: laporan itu di-park, **±2 s** setelah mode dipasang. Audit API `GET /internal/audit/unprocessed?source=PVMBG`: `reason: schema_incompatible`, `retryable: false`, `attempts: 1`, `status: parked`, pesan disimpan utuh dengan `message_id` sha256 dari raw.
- Mock dikembalikan ke `{"version":1}` pukul 03:56:55. Laporan v1 berikutnya `PVMBG-1795319816290710480` tersimpan 03:56:56,9, jadi **cursor tidak tertahan** oleh pesan yang di-park.
- Status sumber PVMBG di `sources` tetap `stale:true` dengan `ingestion_error` sampai laporan rusak keluar dari jendela overlap poll (1 menit), lalu kembali `stale:false` (terverifikasi 04:00:09). Perilaku ini disengaja (`internal/ingest/status_test.go`, `TestIncompletePollCannotClearStaleStatus`): klien diberi tahu ada data sumber yang tidak bisa ditafsirkan.
- Total DLQ PVMBG di cluster sebelum uji: 21 item `schema_incompatible|parked` (latihan 15–19 Nov).

## P3.3 — Semua mode rusak, replay, dan field report (`make p3-check`)

Stack Compose terisolasi dengan PostgreSQL/RabbitMQ asli, 03:53:50, **PASS** (`artifacts/m2/runs/20261122T035350Z-p3-50f44c/p3-check.json`):

| Mode / kasus | Disimpan utuh (byte-for-byte) | Replay setelah perbaikan | Pesan valid di sampingnya tetap diproses |
|---|---|---|---|
| `rename_field` | PASS | PASS | PASS |
| `change_type` | PASS | PASS | PASS |
| `drop_required` | PASS | — | PASS |
| `broken_json` | PASS | — | PASS |
| `oversize_json` | PASS | — | PASS |
| `deep_json` | PASS | — | PASS |
| `invalid_utf8` | PASS | — | PASS |

Field report (`POST /field-reports`):
- PASS: semua laporan yang sudah dijawab 202 selamat dari SIGKILL penerima.
- PASS: setiap kiriman ganda dikonsumsi lalu dilewati (dedup durable per kunci idempoten).
- PASS: retry laporan commit tepat sekali setelah broker pulih.

Percobaan pertama (03:41:55) gagal sebelum uji dimulai karena pull image `golang:1.27.1` gagal (DNS/IPv6 node-a), bukan karena kode. Ulangan setelah image ditarik ulang lulus.

## Belum dibuktikan

- Replay dari dalam cluster untuk pesan di-park milik cluster (bukan stack terisolasi): endpoint `POST /internal/audit/unprocessed/replay` sudah diuji di `p3-check`, tetapi 22 item di cluster sengaja dibiarkan sebagai contoh demo. Inventaris read-only per 09:19 ada di P3.4 di bawah.

## P3.2b — Gagal sementara vs permanen (`make p3-retryable`, 22 Nov 09:31 UTC)

Skrip PR #25, stack Compose terisolasi di `node-a` (PostgreSQL/RabbitMQ asli, satu node), commit `1b61c6f`, **PASS**, cleanup PASS. Artefak: `artifacts/m2/runs/20261122T093155Z-p3-retryable-ccbe46/p3-retryable.json` di `node-a`.

Gangguan sementara disimulasikan dengan memasang CHECK constraint di `hazard_events` yang hanya menolak `source_ref_id` satu laporan PVMBG valid, lalu mencabutnya.

| Langkah | Gagal sementara (storage) | Gagal permanen (`rename_field`) |
|---|---|---|
| Klasifikasi | `reason: store_unavailable`, `status: retrying` | `reason: schema_incompatible`, `status: parked`, publik `retryable: false` |
| Percobaan 1 | 09:34:39, `attempts: 1`, retry dijadwalkan 09:34:43 (+4 s) | `attempts: 1`, `next_retry_at: null` (tidak di-retry) |
| Percobaan 2 | `attempts: 2`, retry berikutnya 09:34:53 (backoff naik) | — |
| Checkpoint selama gagal | Tidak maju, event kanonis 0 | Laporan valid berikutnya tetap masuk (sumber tidak tertahan) |
| Setelah gangguan dicabut | `status: recovered`, `attempts: 3`, **tepat 1** event kanonis, checkpoint maju | — |
| Replay ulang pesan yang sudah pulih | 202, event kanonis tetap 1 (idempoten) | — |

Percobaan pertama 09:31:31 gagal sebelum uji dimulai karena salinan skrip di `node-a` bukan repo git (`git rev-parse HEAD` exit 128). Ulangan dari clone git lulus.

## P3.4 — Inventaris DLQ PVMBG di cluster (`make p3-replay-cluster`, 22 Nov 09:19 UTC)

Skrip dari PR #25 (checkout `main` `1b61c6f` di `node-a`), mode bawaan **read-only** (tanpa `REPLAY=1`), lewat Audit API Aggregator. Artefak: `artifacts/m2/runs/20261122T091957Z-p3-replay-86f408/` di `node-a`.

| Ukuran | Nilai |
|---|---|
| Item DLQ PVMBG | **22**, semuanya `status: parked`, `reason: schema_incompatible`, `retryable: false`, `attempts: 1` |
| Rentang `received_at` | 19 Nov 09:51:31 s/d 22 Nov 03:56:53 (item terakhir = uji P3.2 di atas) |
| Event kanonis PVMBG (Audit events) | 11.542, `partial: false` |
| Hasil | `INVENTORY_ONLY`, tidak ada mutasi |

- ~~Ke-22 item tidak bisa dipulihkan dengan replay: semuanya hasil mode `rename_field`~~ **Koreksi (10:45 UTC):** klaim ini keliru, ditulis tanpa memeriksa payload. Pemeriksaan raw payload menunjukkan 21 item bermode `change_type` dan 1 item `rename_field`, dan keduanya didukung migrasi replay. Lihat "P3.4b — Penyebab 22 pesan tetap `parked`" di bawah.
- Replay dan urutan per `source_ref_id` sudah dibuktikan di stack terisolasi (`p3-check`, P3.3) dan di tes Go `TestPVMBGReplayKeepsSourceOrder` (PostgreSQL asli, lihat bawah).

## Tes Go Aggregator dengan PostgreSQL asli (22 Nov 09:1x UTC)

`go test -race -count=1 ./...` pada `services/aggregator` (`main` `1b61c6f`), container `golang:1.27.1` di `node-a` dengan `TEST_DATABASE_URL` ke `postgres:17-alpine` sekali pakai. **13/13 paket ok, 232 tes PASS**, 4 dilewati karena butuh RabbitMQ/feed live (`TestOutboxPublisher`, `TestCoursePracticeFeed`, `TestRecordedPracticeReplay`, `TestOfficialSensorContract`). Relevan untuk P3:

- `TestPVMBGPostgresCheckpointWaitsForDeadLetter`: checkpoint PVMBG tidak maju sebelum tulisan DLQ berhasil (kegagalan storage sementara tidak menghilangkan pesan).
- `TestPVMBGReplayKeepsSourceOrder`: replay mempertahankan urutan per sumber.
- `TestParkReportPreservesBytesAndDeduplicates`: pesan di-park byte-for-byte dan tidak terduplikasi.

## P3.5 — Inspeksi DLQ hanya untuk Internal Ops (22 Nov 05:34 UTC)

Diuji lewat Control Console (`make console`, panel Klien) ke Client API cluster, login dengan kredensial dari Secret:

| Peran | `GET /internal/audit/unprocessed` | `POST /field-reports` | `GET /hazards` |
|---|---|---|---|
| Internal Ops | **200** (91 ms) | 403 | 200 |
| Tim Lapangan | **403** | **202** | 200 |
| Media | **403** | 403 | 200 |

## P3.4b — Penyebab 22 pesan tetap `parked` dan replay salinannya (22 Nov 10:30–10:55 UTC)

### Investigasi

| Pertanyaan | Temuan |
|---|---|
| Apakah image ter-deploy memuat logika replay terbaru? | Ya. Migrasi replay `DecodePVMBGReplay` (`status`→`alert_level`, `reported_at` epoch→RFC3339) masuk di `0d9ebf3` (19 Nov) dan termasuk di image `c936b23171d7` yang berjalan. |
| Apakah raw payload sesuai migrasi yang didukung? | Ya. Raw payload dibaca langsung dari tabel `unprocessed_messages` (SELECT read-only, ketiga unit, dideduplikasi per `message_id`): **21 pesan `change_type`** (`reported_at` integer epoch, `source_ref_id` NULL di DLQ karena payload tidak lolos decode awal) dan **1 pesan `rename_field`** (`status` menggantikan `alert_level`, uji P3.2 03:56). Keduanya ditangani migrasi. Klaim sebelumnya "seluruhnya `rename_field`" keliru. |
| Apakah ada masalah pada replay, status DLQ, atau penyimpanan? | Tidak ditemukan. Semua item masih `attempts: 1`: **endpoint replay belum pernah dipanggil di cluster**. `parked` adalah status permanen yang memang tidak di-retry otomatis; pemulihan hanya lewat `POST /internal/audit/unprocessed/replay` oleh Internal Ops. Jalur replay (status `retrying` → poller leader satu pesan per `source_ref_id`, NULL memakai `message_id` → `DecodePVMBGReplay` → upsert idempoten → `recovered`) utuh. |

Kesimpulan: **tidak ada cacat implementasi; tidak ada perubahan kode layanan.** Yang ditambahkan hanya alat uji: `TestPVMBGReplayParkedCopy` (`services/aggregator/internal/ingest/pvmbg_dlq_copy_test.go`, dilewati tanpa fixture) dan `make p34-replay-copy` (`scripts/m2/p34-replay-copy.sh`).

### Replay salinan di lingkungan terisolasi (`make p34-replay-copy`, 10:53 UTC)

Skrip menyalin baris `parked` dari ketiga unit store (44 baris replika = 22 pesan, SELECT read-only; DLQ cluster tidak diubah), lalu memutarnya lewat jalur replay produksi (`QueuePVMBGReplay` per 32 ID seperti endpoint HTTP, lalu `retryPVMBG` di leader) pada `postgres:17-alpine` sekali pakai, `go test -race`. Artefak: `artifacts/m2/runs/20261122T105304Z-p34-replay-copy/` di `node-a`.

| Pemeriksaan | Hasil |
|---|---|
| Pesan dipulihkan | **22/22 `recovered`**, 0 tersisa |
| HazardEvent | **22**, tepat **1 per `report_id`** (`UNIQUE(source, source_ref_id)`), revision 1 |
| Isi sesuai raw | severity = `alert_level`/`status` raw; `occurred_at` = epoch raw yang dikonversi |
| Replay ulang semua 22 ID | Tidak ada perubahan: jumlah hazard, snapshot outbox, status DLQ, dan revision identik |
| Urutan per `source_ref_id` | Ke-22 `report_id` unik (tidak ada dua revisi untuk kunci yang sama di set ini); penjagaan urutan antar-revisi dibuktikan `TestPVMBGReplayKeepsSourceOrder` (lulus di run yang sama) |

Pra-syarat keamanan replay di cluster (Audit API 10:50 UTC, read-only): 22 ID `parked` di Audit = 22 ID di salinan; **0 dari 22 `report_id` sudah ada** di 12.086 event PVMBG kanonis, jadi replay tidak menimpa data apa pun.

### Replay di cluster (disetujui, 22 Nov 11:01–11:03 UTC) — **PASS**

`python3 scripts/m2/p3-replay-cluster.py --replay --message-id-file artifacts/m2/runs/p34/parked-ids.txt` (setara `make p3-replay-cluster REPLAY=1 MESSAGE_ID_FILE=…`) ke Audit API, image `c936b23171d7`. Artefak: `artifacts/m2/runs/p34/cluster-replay-110124/` di `node-a`.

| Pemeriksaan | Hasil |
|---|---|
| DLQ PVMBG aktif | 22 `parked` → **0** (22 `recovered`, 0 tersisa), selesai dalam ±70 s |
| HazardEvent untuk 22 laporan | **22**, maksimum **1 salinan per `source_ref_id`** |
| Urutan/konsistensi (snapshot Audit sebelum vs sesudah) | **0 pelanggaran**: tidak ada event hilang, `hazard_id` tidak berubah, `occurred_at` tidak mundur |
| Event baru | 28 = 22 hasil replay + 6 ingest PVMBG normal yang berjalan bersamaan |
| Checksum PVMBG | 12.139 → 12.168, `partial:false` sebelum dan sesudah |
| Replay ulang ke-22 ID (11:03, HTTP 202) | Tidak ada efek: DLQ tetap 0; ke-22 event identik (`hazard_id`, severity, `occurred_at`, `ingested_at`), jadi tidak ditulis ulang |

Contoh: `PVMBG-1795081891089218645` → `haz_afe27519a703dc0c32b2ee2da5bcb48c`, `WASPADA`, `occurred_at 2026-11-19T09:51:31Z` (dari `reported_at` epoch 1795081891).

**Status P3 kriteria 4: PASS.** Seluruh pesan yang valid setelah penyesuaian skema dipulihkan, tanpa duplikat, urutan per `source_ref_id` terjaga, dan replay ulang tidak menambah efek. Contoh DLQ baru untuk demo dapat dibuat dengan `POST /admin/schema-version {"mode":"rename_field"}` pada mock PVMBG.
