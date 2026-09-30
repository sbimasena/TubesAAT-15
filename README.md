# Sistem Koordinasi Bencana IF4031 — Milestone 1

Purwarupa sistem koordinasi bencana yang menggabungkan data gempa dan peringatan tsunami BMKG dengan laporan gunung api PVMBG untuk BNPB. Aggregator mengambil data dari kedua sumber, mengubahnya menjadi `HazardEvent`, lalu menyediakan API internal untuk layanan lain.

## Status dan pembagian pekerjaan

Status di bawah ini mengikuti konteks proyek dan isi cabang saat README ini diperbarui. Tanggung jawab anggota tetap mengikuti pembagian kerja tim.

### Anggota A — Data dan Aggregator

Pekerjaan yang sudah tersedia pada cabang ini:

- Mock BMKG menyediakan data gempa dan peringatan tsunami, autentikasi `X-BMKG-Key`, data historis, serta rekaman simulasi baru secara berkala.
- Mock PVMBG menyediakan laporan aktivitas gunung api, autentikasi Bearer, jeda respons yang dapat diatur, serta simulasi gangguan dan perubahan skema saat layanan berjalan.
- Aggregator melakukan polling BMKG dan PVMBG secara mandiri, memetakan data menjadi `HazardEvent`, mengorelasikan peringatan tsunami, dan menghindari duplikasi berdasarkan identitas sumber.
- Kolom PVMBG tambahan, termasuk `confidence_level`, diteruskan ke `HazardEvent.attributes`.
- API Aggregator menyediakan pemeriksaan kesehatan, daftar hazard, dan filter sumber, tipe hazard, waktu, serta jumlah hasil.
- Log dan permintaan menggunakan correlation ID.

Penanggung jawab pada konteks proyek: P1 dan bagian Aggregator/upstream dari P2.

**Batas saat ini:** penyimpanan Aggregator masih berada di memori dan penerbitan peristiwa masih berupa log. Integrasi PostgreSQL dan RabbitMQ belum tersedia pada Compose cabang ini.

### Anggota B — API Klien, Auth, dan Resiliensi

Ruang lingkup sesuai konteks proyek:

- API Klien mengambil data melalui API Aggregator dan tidak mengakses Canonical Store secara langsung.
- Layanan Auth mengelola identitas klien Media, Tim Lapangan, dan Operasi Internal BNPB, termasuk token akses, token penyegar, TTL, dan ruang lingkup akses.
- API Klien menerapkan otorisasi di sisi server serta membatasi field yang boleh diterima tiap klien.
- Ketahanan permintaan klien hilir dan perilaku degradasi ditangani bersama Anggota A dan C.

Penanggung jawab pada konteks proyek: koordinasi P2 dan P3. **Status pada cabang ini:** direktori `services/client-api` dan `services/auth` masih berupa kerangka; endpoint, token, scope, dan alur refresh belum diimplementasikan.

### Anggota C — Infrastruktur, Penyimpanan, dan Pesan

Ruang lingkup sesuai konteks proyek:

- Menyiapkan Docker Compose dan infrastruktur PostgreSQL sebagai Canonical Store milik Aggregator.
- Menyiapkan RabbitMQ dan kontrak distribusi `HazardEvent`.
- Menyiapkan layanan konsumen notifikasi dan dasbor/audit yang menerima peristiwa secara mandiri.
- Mendukung observabilitas, pengujian beban, dan pengujian kegagalan.

Penanggung jawab pada konteks proyek: P4 dan P5. Menurut koordinasi tim, infrastruktur sementara sudah disiapkan; layanan tersebut belum tercantum di `docker-compose.yml` cabang ini dan belum terhubung ke adapter Aggregator. Direktori kedua consumer juga masih berupa kerangka.

### Pekerjaan integrasi tim berikutnya

1. Anggota C membagikan konfigurasi layanan sementara serta kontrak exchange, queue, dan payload RabbitMQ.
2. Anggota A dan C menghubungkan repository Aggregator ke PostgreSQL dan publisher ke RabbitMQ.
3. Anggota B mengimplementasikan Auth dan API Klien sesuai kontrak API Aggregator serta scope tiap klien.
4. Anggota C menyelesaikan consumer notifikasi dan dashboard/audit.
5. Seluruh tim memeriksa alur ujung ke ujung, perubahan skema, outage, duplikasi, otorisasi, dan beban.

## Teknologi dan batas arsitektur

- Go 1.27.1 dan `net/http`; setiap layanan Go memiliki modul sendiri.
- Docker Compose untuk menjalankan layanan secara lokal.
- PostgreSQL dan RabbitMQ merupakan pilihan Canonical Store dan broker yang dituju, tetapi belum dicantumkan dalam Compose cabang ini.
- BMKG dan PVMBG tetap menjadi layanan mock mandiri. Aggregator melakukan polling melalui HTTP dengan kredensial terpisah.
- Hanya Aggregator yang boleh mengakses Canonical Store. Layanan lain mengambil data melalui API Aggregator.

## Menjalankan alur terbaru Anggota A

Kredensial di `.env` harus berbeda untuk BMKG dan PVMBG. Untuk menjalankan mock dan Aggregator memakai kredensial development contoh:

```sh
BMKG_API_KEY=dev-bmkg-key PVMBG_TOKEN=dev-pvmbg-token \
  docker compose up --build -d bmkg pvmbg aggregator
```

> Jika layanan sudah berjalan, langkah start ini tidak perlu diulang. Jangan gunakan kredensial contoh di luar pengembangan lokal. Jangan commit `.env`.

Periksa kontainer dan status kesehatan layanan:

```sh
docker compose ps
curl -sS http://localhost:8083/health | python3 -m json.tool
```

Pada respons kesehatan Aggregator, tunggu sampai `sources.BMKG.available` dan `sources.PVMBG.available` bernilai `true`. Polling bawaan berjalan setiap 3 detik.

Lihat beberapa rekaman bahaya dan status sumber tanpa `jq`:

```sh
curl -sS 'http://localhost:8083/hazards?limit=5' |
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(json.dumps({"count":d["count"],"sources":d["sources"],"events":[{k:e.get(k) for k in ("source","hazard_type","hazard_id")} for e in d["data"]]},indent=2))'
```

Filter API internal juga dapat diperiksa:

```sh
curl -sS 'http://localhost:8083/internal/v1/hazards?source=PVMBG&hazard_type=VOLCANIC&limit=3' |
  python3 -m json.tool
```

### Memeriksa autentikasi sumber

Permintaan tanpa kredensial atau dengan format kredensial yang salah seharusnya menghasilkan HTTP `401`. Kredensial yang benar seharusnya menghasilkan HTTP `200`:

```sh
# Tanpa kunci BMKG: harapkan HTTP 401
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' \
  http://localhost:8081/seismic-events

# Dengan kunci BMKG: harapkan HTTP 200
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' \
  -H 'X-BMKG-Key: dev-bmkg-key' \
  http://localhost:8081/seismic-events

# Token tanpa skema Bearer: harapkan HTTP 401
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' \
  -H 'Authorization: dev-pvmbg-token' \
  http://localhost:8082/volcanic-reports

# Token Bearer yang benar: harapkan HTTP 200
curl -sS -o /dev/null -w 'HTTP %{http_code}\n' \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  http://localhost:8082/volcanic-reports
```

### Memeriksa evolusi skema PVMBG

Aktifkan skema v2. Endpoint mengembalikan ID laporan baru; setelah polling, cari laporan itu di Aggregator dan pastikan `confidence_level` muncul di `attributes`:

```sh
V2_ID=$(curl -sS -X POST http://localhost:8082/admin/schema-version \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  -H 'Content-Type: application/json' \
  -d '{"version":2}' |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["report_id"])')

sleep 5

curl -sS 'http://localhost:8083/hazards?source=PVMBG&limit=1000' |
  V2_ID="$V2_ID" python3 -c 'import json,os,sys; d=json.load(sys.stdin); rid=os.environ["V2_ID"]; rows=[{"source_ref_id":e.get("source_ref_id"),"confidence_level":e.get("attributes",{}).get("confidence_level")} for e in d["data"] if e.get("source_ref_id")==rid]; print(json.dumps(rows,indent=2))'
```

Hasil yang diharapkan adalah satu laporan dengan nilai `confidence_level` numerik. Untuk mengembalikan laporan baru ke skema v1:

```sh
curl -sS -X POST http://localhost:8082/admin/schema-version \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  -H 'Content-Type: application/json' \
  -d '{"version":1}'
```

Laporan yang sudah dibuat dengan skema v2 tetap menyimpan kolom tersebut.

### Memeriksa simulasi gangguan PVMBG

Aktifkan simulasi gangguan, tunggu beberapa siklus polling, lalu lihat status sumber. PVMBG seharusnya menjadi tidak tersedia sedangkan BMKG tetap tersedia:

```sh
curl -sS -X POST http://localhost:8082/admin/outage \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  -H 'Content-Type: application/json' \
  -d '{"enabled":true}'

sleep 5
curl -sS http://localhost:8083/health |
  python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["sources"],indent=2))'
```

Matikan simulasi gangguan, tunggu polling berikutnya, lalu periksa bahwa PVMBG kembali tersedia:

```sh
curl -sS -X POST http://localhost:8082/admin/outage \
  -H 'Authorization: Bearer dev-pvmbg-token' \
  -H 'Content-Type: application/json' \
  -d '{"enabled":false}'

sleep 5
curl -sS http://localhost:8083/health |
  python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["sources"],indent=2))'
```

## API Aggregator

- `GET /health` — status Aggregator dan status polling kedua sumber.
- `GET /hazards` — daftar kanonis dengan bentuk `{ "data": [...], "count": n, "sources": {...} }`.
- `GET /hazards?source=BMKG&hazard_type=SEISMIC&since=<RFC3339>&limit=100` — filter hasil; batas `limit` adalah 1.000.
- `GET /internal/v1/hazards` — alias untuk daftar hazard bagi klien internal.

ID hazard dibuat deterministik dari sumber dan ID referensi sumber. Ini membuat polling berulang tidak menambahkan event yang sama sebagai ID berbeda selama Aggregator hidup.

## Batas implementasi saat ini

- Repositori Aggregator saat ini hanya menyimpan sampai 10.000 peristiwa di memori proses. Data hilang saat kontainer dimulai ulang.
- Adapter penerbit saat ini menulis log bahwa peristiwa akan dikirim; belum ada pengiriman ke RabbitMQ.
- PostgreSQL dan RabbitMQ belum didefinisikan pada Compose cabang ini. Layanan Auth, API Klien, dan kedua layanan konsumen masih berupa kerangka dengan fungsi utama kosong, sehingga alur fungsionalnya belum tersedia.
- Karena itu pemeriksaan di atas memverifikasi alur mock → Aggregator, bukan persistensi tahan restart atau distribusi pesan broker.
