# P1 — Storage terdistribusi: panduan kerja

Panduan untuk anggota yang melanjutkan atau menguji P1. PIC: C. Keputusan desain lengkap ada di `context-m2.md` §7.1 dan §9.2 (D-P1); kontrak Audit API di `kontrak-m2.md` §7.

## Cara kerja singkat

- **Partisi:** `fnv32a(hazard_id) % 24`, RF=2, di atas 3 unit PostgreSQL (StatefulSet `store-0..2`, satu per node). Hash berdasarkan `hazard_id`, bukan wilayah, supaya gempa yang menumpuk di satu wilayah tidak membebani satu node.
- **Peta partisi:** ConfigMap `store-partition-map`, diperbarui dengan CAS `resourceVersion`. Hanya Aggregator yang menyentuh store.
- **Tulis:** ke semua replika partisi; dianggap tersimpan bila satu replika commit. Replika yang tertinggal disusulkan anti-entropy.
- **Baca:** scatter-gather (timeout 1 s per unit). Jawaban `partial: true` hanya bila ada partisi tanpa replika hidup; `unavailable_units` selalu diisi bila ada unit tak terjangkau.
- **Reconciler** (tiap 10 s): deteksi amnesia lewat `incarnation`, anti-entropy, re-replikasi setelah unit mati > `REREPLICATE_AFTER_SECONDS` (60), rebalance maksimal 4 perpindahan per putaran, lalu sweep partisi yang sudah pindah.

## Letak kode

| Bagian | File |
|---|---|
| Router, scatter-gather, Checksum, Locate, Analytics | `services/aggregator/internal/store/router.go` |
| Peta partisi, layout | `services/aggregator/internal/store/partmap.go`, `kube.go` |
| Reconciler | `services/aggregator/internal/store/reconcile.go` |
| Query per unit | `services/aggregator/internal/store/postgres.go` |
| Audit API | `services/aggregator/internal/httpapi/audit.go` |
| Loader katalog | `services/aggregator/internal/httpapi/load.go` |
| Dashboard | `services/aggregator/internal/httpapi/analytics.go`, `dashboard.html` |
| Manifest | `deploy/k8s/base/store.yaml`, `aggregator.yaml` |

## Endpoint Aggregator

| Endpoint | Auth | Isi |
|---|---|---|
| `GET /internal/audit/{events,checksum,location}` | token audit | Kontrak `kontrak-m2.md` §7 |
| `POST /internal/admin/load` | token audit | NDJSON atau JSON array event BMKG (`event_id`, `region_name`, `epicenter_lat`, `epicenter_lon`, `magnitude`, `depth_km`, `occurred_at`), maksimal 5000 per request |
| `GET /internal/analytics?from=&to=` | tidak ada | Agregat per jenis bahaya per waktu (60 bucket), per severity, per area, `partial`, `unavailable_units` |
| `GET /dashboard` | tidak ada | Halaman dashboard analitik |

Token audit: `kubectl -n sistem-informasi-kebencanaan-bnpb get secret audit-token -o jsonpath='{.data.token}' | base64 -d`.

## Target `make`

Semua juga ada di `scripts/m2/actions.json` (Control Console). Jalankan dari mesin yang punya kubeconfig cluster (biasanya `node-a`).

| Target | Fungsi |
|---|---|
| `make p1-checksum` | Checksum Audit API per sumber dan total |
| `make p1-distribution` | Rows dan jumlah partisi per unit/node, plus isi peta partisi |
| `make p1-load FILE=katalog.ndjson` | Muat katalog, cetak tulis per unit selama run, log ke `artifacts/m2/runs/` |
| `make p1-surge N=3000 REGION="Sulawesi Tengah"` | Lonjakan gempa sintetis di satu wilayah, diukur dengan cara yang sama |
| `make p1-dashboard` | Port-forward Aggregator ke `127.0.0.1:18083` |
| `make p1-wipe-node` | Checksum awal lalu Tester Agent `wipe-node` |

Muat ulang katalog aman: upsert idempoten per `event_id`. Bila format katalog asisten bukan field BMKG, ubah dulu dengan `jq` atau tambahkan pemetaan di `load.go`.

## Melihat dashboard dari laptop

Server tidak punya GUI; buka dashboard di browser laptop lewat SSH tunnel:

```sh
ssh -L 18083:127.0.0.1:18083 <user>@192.168.18.201   # dari laptop
make p1-dashboard                                     # di sesi SSH (node-a)
# browser laptop: http://127.0.0.1:18083/dashboard
```

Alternatif: salin `/etc/rancher/k3s/k3s.yaml` ke laptop, ganti `127.0.0.1:6443` dengan `192.168.18.201:6443`, lalu jalankan `make p1-dashboard` langsung di laptop. Jangan commit kubeconfig. Chart.js diambil dari CDN, jadi browser perlu internet.

Pada rentang "Semua", total di dashboard harus sama dengan `count` di `make p1-checksum` (keduanya menggabungkan replika per `hazard_id`).

## Alur uji kriteria P1

1. **Sebaran (P1.1–P1.2):** `make p1-load FILE=…`, lalu `make p1-surge`, lalu `make p1-distribution`. Tidak ada unit yang memegang semua rows; dengan RF=2, jumlah rows semua unit = 2 × `count` checksum.
2. **Wipe node (P1.4):** `make p1-wipe-node`. Saat node mati, `/internal/audit/events` dan dashboard tetap 200 dengan `unavailable_units`. Setelah node kembali: log Aggregator memuat `store unit lost its data` lalu `partition moved ... to store-N`; `make p1-checksum` sama dengan sebelum; `make p1-distribution` kembali 16/16/16 partisi.
3. **Node baru / disk kosong (P1.5–P1.6):** sama seperti langkah 2, lalu ulangi kehilangan satu node lagi.

Temuan perilaku Tester Agent dicatat di `temuan-tester-agent.md`.

## Uji lokal (Windows)

```sh
cd services/aggregator
CGO_ENABLED=0 go test ./...            # tanpa gcc tidak bisa -race
# integrasi store: PostgreSQL 18 lokal di port 55432
"/c/Program Files/PostgreSQL/18/bin/pg_ctl" -D <dir> -o "-p 55432" start
TEST_DATABASE_URL='postgres://postgres@127.0.0.1:55432/postgres?sslmode=disable' CGO_ENABLED=0 go test ./internal/store
```

`TestFailedFetchPreservesLastIngestedUntilRecovery` (ingest) gagal di Windows karena timestamp kembar; itu bukan bagian P1. Di Linux/CI jalankan `go test -race ./...`.

## Sisa pekerjaan P1

- [x] Bukti `wipe-node` dan `partition` di cluster (`bukti-p1-cluster.md`, `temuan-tester-agent.md`); `partition` tidak menyekat pod, tanyakan ke asisten.
- [ ] Jalankan `p1-load`/`p1-surge` dengan dataset asisten (dibagikan setelah 20 Nov), simpan tabel beban per unit sebagai bukti.
- [ ] Reconciler hanya di leader setelah `internal/leader` dari A masuk (sekarang epoch 0 di `cmd/server/main.go`).
- [ ] Akses Audit API dari luar cluster (menunggu jawaban asisten).
- [ ] Runbook P1 di `runbook.md` dan laporan P1.

## Risiko yang diketahui

- Event katalog masuk outbox dan dikirim ke RabbitMQ, jadi consumer notifikasi ikut menerima semuanya.
- Analytics mengirim semua rows dalam rentang ke Aggregator; dengan katalog sangat besar, timeout baca 1 s bisa membuat jawaban tampak `partial`. Upgrade: `GROUP BY` per partisi di unit.
- Potongan loader 500 event harus selesai dalam batas tulis 3 s per unit; bila tidak, pakai `CHUNK=200 make p1-load …`.
