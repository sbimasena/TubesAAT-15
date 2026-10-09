# Bukti P1 Member A

Bukti berasal dari pengujian nyata pada workspace lokal, 9 Oktober 2026 pukul 19.41.19–19.41.39 WIB (12.41.19–12.41.39 UTC). Commit dasar aplikasi: `2d6280fcd8732a3d8f45992a9e9c32017b3a82ac`. Skrip dokumentasi baru masih merupakan perubahan workspace saat pengujian, sebagaimana metadata `tested_source: WORKSPACE` pada JSON.

Project uji `tubesaat-p1-report` menjalankan sembilan container. Polling 3 detik, generator kedua mock 10 detik, dan delay PVMBG 750 ms. Generator default kode/Compose adalah 15 detik; konfigurasi pengujian berbeda secara sengaja dan tercatat pada JSON. Tidak ada perubahan kode layanan untuk menghasilkan bukti ini.

| Berkas | Isi |
|---|---|
| `report-check.json` | Payload BMKG, pasangan warning, hasil kanonis; payload PVMBG v1/v2; migrasi sebelum/sesudah; identitas dan waktu startup container; trace v2 dan snapshot kedua consumer. Hasil keseluruhan `PASS`. |
| `ingestion-check.json` | Hasil checker evolusi skema yang sudah ada, termasuk outage, stale state, recovery, dan fanout. |
| `ingestion-run.log` | Keluaran checker ingestion pada pengujian yang sama. |
| `p1-01-bmkg-tsunami.png` | Gambar 1: pemetaan kejadian BMKG dan korelasi warning tsunami. |
| `p1-02-pvmbg-schema-evolution.png` | Gambar 2: pemetaan PVMBG v1/v2 dan nilai confidence yang dipertahankan. |
| `p1-03-runtime-verification.png` | Gambar 3: container/migrasi tetap, trace fetch–commit–publish, serta dua consumer. |
| `p1-ingestion-run-terminal.png` | Keluaran checker asli pada `ingestion-run.log`, ditangkap melalui Freeze. Bukti tambahan, belum diberi nomor gambar dalam laporan. |
| `p1-*.html` dan `html-archive/` | Tampilan HTML dan gambar versi awal, disimpan sebagai arsip. |

PNG Gambar 1–3 saat ini dibuat menggunakan `freeze --execute` dari keluaran `scripts/show-p1-evidence.py`. Perintah tersebut membaca hasil pengujian yang sudah tersimpan; tidak menjalankan ulang ingestion. Payload, timestamp, ID container, catatan migrasi, dan log berasal dari hasil checker pada run yang sama. `p1-ingestion-run-terminal.png` menampilkan keluaran checker asli yang disimpan pada `ingestion-run.log`. Bukti ini tidak mengukur throughput, p95, atau kapasitas beban P2.

## Membuat ulang gambar terminal

Jalankan dari root repo dengan Freeze yang telah terpasang:

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

freeze --execute 'cat docs/evidence/member-a/p1/ingestion-run.log' \
  --config base --window --font.size 20 --margin 0 --padding 24 \
  --border.radius 0 --shadow.blur 0 --shadow.x 0 --shadow.y 0 \
  --output docs/evidence/member-a/p1/p1-ingestion-run-terminal.png
```

`show-p1-evidence.py` juga dapat dijalankan langsung di terminal. Gunakan `--input` untuk membaca hasil run lain. Gambar runtime menampilkan 12 karakter awal ID container; identitas lengkap tersedia pada JSON.

## Reproduksi

Isi file env lokal menurut README proyek, gunakan port host yang tidak berbenturan, dan tetapkan `BMKG_GENERATE_INTERVAL_SECONDS=10`, `PVMBG_GENERATE_INTERVAL_SECONDS=10`, `POLL_INTERVAL_SECONDS=3`, serta `PVMBG_DELAY_MS=750`. Jangan commit kredensial aktual.

```sh
docker compose --env-file .env.p1.local --project-name tubesaat-p1-report \
  -f docker-compose.yml -f tests/compose-operator.yml up --build -d --wait

python3 -B scripts/check-p1-report.py \
  --env-file .env.p1.local --project-name tubesaat-p1-report \
  --output /tmp/tubesaat-p1-new-evidence/report-check.json

freeze --execute 'python3 -B scripts/show-p1-evidence.py runtime --input /tmp/tubesaat-p1-new-evidence/report-check.json' \
  --config base --window --font.size 20 \
  --output /tmp/tubesaat-p1-new-evidence/runtime.png
```

Checker memerlukan Python 3 dan Docker Compose; gambar terminal memerlukan Freeze. Jalankan dari root repo. Output baru ditempatkan di direktori terpisah agar bukti run ini tetap tersedia. `render-p1-evidence.py` tetap tersedia untuk membuat tampilan HTML versi awal dengan Google Chrome; jangan menjalankannya pada direktori bukti utama jika ingin mempertahankan PNG terminal.

Checker menambah data dan jurnal serta menguji skema v1/v2 dan outage, lalu mengembalikan PVMBG ke versi 1 dengan outage nonaktif. Checker tidak me-restart container atau menghapus volume. ID, timestamp, dan nilai confidence dapat berbeda pada run berikutnya.

Unit test relevan yang dijalankan bersama pengumpulan bukti:

```sh
cd services/aggregator
go test ./internal/ingest ./internal/httpapi
```

Kedua paket lulus. Rujukan paragraf, tabel, diagram ASCII, dan caption tersedia pada `docs/report/member-a-additions.md`.
