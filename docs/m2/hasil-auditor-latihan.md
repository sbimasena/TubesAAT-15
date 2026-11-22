# Hasil auditor — skenario latihan (22 Nov 2026)

Skenario **ujian** baru bisa dijalankan setelah kunci dari asisten dibagikan (`spesifikasi-m2.md` §Tester Agent). Dokumen ini adalah versi latihan dari "hasil auditor": setiap skenario latihan Tester Agent dijalankan di antara dua snapshot Audit API, lalu dinilai otomatis.

Perintah: `make audit-run S=<skenario>` (`scripts/m2/audit-run.py`; juga tersedia di Control Console).

## Kriteria putusan

1. **Tidak ada data hilang:** `count` checksum per sumber sesudah skenario ≥ sebelum.
2. **Tidak ada duplikat:** semua event yang di-ingest sejak skenario dimulai (`/internal/audit/events?since=`, semua halaman cursor) memiliki `hazard_id` unik.
3. **Replikasi utuh:** 5 event sampel tetap punya primary dan replica di node berbeda (`/internal/audit/location`).
4. **Pulih:** ketiga store `Ready` dan checksum semua sumber `partial:false`. Pemeriksaan dimulai 90 s setelah skenario dikirim, lalu diulang tiap 10 s sampai 600 s.

## Hasil

| Skenario | Aksi Tester Agent | BMKG | PVMBG | SATELIT | SENSOR_PANTAI | Event baru / duplikat | Pulih | Putusan |
|---|---|---|---|---|---|---|---|---|
| `kill-store` | pod `store-2` (node-c) dihentikan | 19.250 → 19.259 | 13.264 → 13.273 | 42.386 → 42.390 | 95.837 → 95.837 | 25 / 0 | ≤ 90 s | **LOLOS** |
| `kill-leader` | pod aggregator di node-c dihentikan | 19.260 → 19.270 | 13.274 → 13.284 | 42.390 → 42.396 | 95.837 → 95.837 | 29 / 0 | ≤ 95 s | **LOLOS** |
| `schema` | `POST /admin/schema-version` ke mock PVMBG (HTTP 200) | 19.270 → 19.280 | 13.284 → 13.294 | 42.397 → 42.419 | 95.837 → 95.837 | 45 / 0 | ≤ 90 s | **LOLOS** |
| `clock-skew` | jam satu node digeser +300 s selama 30 s | 19.280 → 19.289 | 13.295 → 13.297 | 42.419 → 42.442 | 95.837 → 95.837 | 37 / 0 | ≤ 90 s | **LOLOS** |
| `partition` | `block_internode` semua node selama 30 s | 19.292 → 19.301 | 13.297 → 13.297 | 42.444 → 42.446 | 95.837 → 95.837 | 15 / 0 | ≤ 90 s | **LOLOS** |

Kelima event sampel tetap berada di dua node berbeda pada semua run, misalnya `haz_4a03…`: `node-b/store-0/primary` + `node-a/store-1/replica`. Log mentah, JSON snapshot, dan log Tester Agent (berantai hash `prev`/`mac`) tersimpan di `artifacts/m2/runs/20261122T1405..1412*-audit-*.{json,log}`.

## Catatan

- **Pulih "≤ 90 s"**: pemeriksaan pertama dilakukan setelah jeda 90 s dan langsung lolos. Waktu pulih sebenarnya lebih pendek, tetapi tidak diukur di sini. Untuk `kill-store`, lihat `bukti-p1-cluster.md`.
- **SENSOR_PANTAI tetap 95.837**: feed latihan sudah habis diputar ulang sebelum audit, jadi tidak ada event pantai baru. Count yang tidak turun tetap membuktikan tidak ada data yang hilang.
- **PVMBG tidak bertambah saat `partition`**: selama sekat 30 s ingestion PVMBG tertahan, lalu tertangkap lagi pada polling berikutnya. Tidak ada event yang hilang atau ganda.
- **`clock-skew`**: setelah pemulihan, timestamp log Tester Agent mundur sekitar 300 s dari langkah mulai (14:10:39 → 14:06:09). Jam ketiga node sinkron kembali (dicek dengan `date -u`). Script kini mengambil log dengan `--tail`, bukan `--since-time`.
- **`wipe-node` tidak diulang**: skenario ini mematikan node berulang kali (`temuan-tester-agent.md`). Kehilangan data satu unit dibuktikan dengan `make p1-amnesia S=store-2` (`bukti-p1-cluster.md`, Lampiran B Gambar B.4–B.7), dan ditolak oleh `make audit-run`.
