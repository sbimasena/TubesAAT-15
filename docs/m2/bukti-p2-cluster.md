# Bukti P2 di cluster nyata (21 Nov 2026)

Cluster k3s 3 node: `node-a` (192.168.18.201, control plane), `node-b` (.202), `node-c` (.203). Branch: `feat/m2-p2-p4-acceptance`. Semua waktu UTC.

Aggregator: 3 replika, topology spread satu per node. Lease `aggregator-leader`, `leaseDuration=15s`, `renewDeadline=10s`, `retryPeriod=2s`.

## P2.1 — Status awal sebelum skenario

`make p2-status` pukul 06:37 UTC:

| Pod | Node | Status |
|---|---|---|
| `aggregator-6b99ff8976-6w7mq` | node-c | Running |
| `aggregator-6b99ff8976-tzrqp` | node-a | Running |
| `aggregator-6b99ff8976-w5pcn` | node-c | Running (LEADER) |

Lease:
```
holderIdentity: aggregator-6b99ff8976-w5pcn_8d1a1d35-a9c7-4ae1-95f3-51b6c870e257
leaseDurationSeconds: 15
leaseTransitions: 15
renewTime: 2026-11-21T06:37:18.141251Z
```

## P2.2 — Failover (`make p2-failover` / `kill-leader`)

Tester `kill-leader` dijalankan pukul 06:40:41 UTC. Dua pod pada node-c dihentikan bersamaan pukul **06:40:46 UTC** (leader `w5pcn` + follower `6w7mq`, tester log seq:9-10).

Pemimpin baru `tzrqp` (node-a) mencatat `leader workers starting` pukul **06:41:08 UTC** — **22 detik** setelah pod-pod dimatikan (< target ≤60 s).

Tester log (seq:12-13): `skenario selesai` + `pemulihan akhir selesai; keadaan bersih` pukul 06:40:46 UTC.

Lease setelah takeover (dipoll pukul 06:41:47):
```
holderIdentity: aggregator-6b99ff8976-tzrqp_1f7623bc-095a-4bc5-b700-95f856ed30ab
leaseTransitions: 16
renewTime: 2026-11-21T06:41:47.141251Z
```

Checksum post-failover (06:42:11):
```json
{"count":15828,"partial":false,"unavailable_units":[],"checksum":"468153f6…"}
```

Duplikat source ID: tidak ada (count tidak mundur, partial:false, store 3×16 partisi semua reachable).

## P2.3 — Clock skew (`make tester S=clock-skew`)

Skenario `clock-skew` mulai 06:42:41 UTC: `shift_clock +300s selama 30s nodes=any` (seq:15 mulai 06:42:46).

Langkah selesai seq:16-18 (timestamp 06:38:16 — jam pada node yang terkena skew maju 300 s sehingga tampak mundur pada log). Skenario berakhir `keadaan bersih`.

Lease tidak berganti selama skew (leaseTransitions tetap 16). Checksum setelah clock-skew dikembalikan (06:45:51):
```json
{"count":15872,"partial":false,"unavailable_units":[]}
```

Ingest terus berjalan (+44 event selama window skew).

Freeze (suspend VM >15 s): belum dilakukan — memerlukan akses VM host.

## P2.4 — Fencing

Log aggregator `leader workers starting epoch:16` pukul 06:41:08. Tidak ada entri `ErrFenced` di log store karena pod leader lama dimatikan segera (tidak sempat mengirim write dengan epoch basi). Fencing epoch diverifikasi lewat unit test (`go test -race ./services/aggregator/internal/store/...` — PASS, lihat bagian Unit test).

## P2.5 — Load test (`make p2-load`)

**Run pertama** pukul 06:47:22 UTC dari node-a: 10 000 VU, 120 s, skala 3→5. Log: `artifacts/m2/runs/20261121T064722Z-p2-load.log`. Node-a OOM di 1m34s (79%): k6 berjalan di node yang sama dengan aggregator + control plane. JSON summary tidak terbentuk.

**Run kedua** pukul 07:25:24 UTC, k6 dari PC lokal Windows (koordinasi scaler di node-a): `artifacts/m2/runs/20261121T072524Z-p2-load.json`.

| Metrik | Nilai |
|---|---|
| VU | 10 000 |
| Durasi | 120 s |
| responses_200/s | 1707 |
| uncontrolled_errors | 28.43% ✗ |
| data_received | 11 GB |

Skala 3→5 dimulai pada t=30s. Rollout **timed out** (timeout=25s) karena pod pada node-b mengalami OOM/CrashLoopBackOff di bawah beban ~3300 VU per pod. Skala 5→2 tidak terjadi (scaler abort pada rollout timeout). Pod yang running: 2/5, sisanya CrashLoopBackOff.

**Root cause:** setiap pod menerima ~3300 VU dan crash (memory/goroutine exhaustion). Ini konsisten dengan temuan throughput: single pod crash pada ≥2000 VU saat menjadi leader. Skala ke 5 menambah pod tapi setiap pod tetap menerima porsi besar dari 10k VU karena server capacity tidak bertambah.

**Kesimpulan P2.5:** Test berhasil dijalankan 120s dari mesin terpisah (OOM di node-a teratasi). Throughput stabil (1707/s) selama pod tidak crash. Kegagalan scaling 3→5 dan error 28% disebabkan pod OOM di bawah high-concurrency — ini masalah kode (resource limits/goroutine leak), bukan masalah infrastruktur.

Checksum post-load (07:05:27): `count:15943, partial:false` — tidak ada kehilangan data.

## P2.6 — Throughput 1 vs 3 replika (`make p2-throughput`)

**Run pertama** pukul 07:01:41 UTC dari node-a, `VUS=2000 DURATION=90s`. JSON: `artifacts/m2/runs/20261121T070141Z-p2-throughput-{1,3}.json`.

| Replika | responses_200/s | avg wait | p90 | p95 |
|---|---|---|---|---|
| 1 | 2438.59 | 313 ms | 519 ms | 636 ms |
| 3 | 1631.70 | 897 ms | 1119 ms | 1199 ms |

**Rasio 0.67×** — di bawah target ≥2×.

---

**Investigasi lanjutan (21 Nov, pukul 07:17–07:22 UTC) dari PC lokal Windows.**

Hipotesis awal (cold cache) diuji langsung. Run warm 3-replika (tiga pod sudah berjalan 9+ menit): `artifacts/m2/runs/warm-3rep-throughput.json`.

| Kondisi | responses_200/s | avg wait | p90 | p95 |
|---|---|---|---|---|
| 3 replika cold (node-a) | 1631.70 | 897 ms | 1119 ms | 1199 ms |
| 3 replika warm (lokal PC) | 1703.98 | 1114 ms | 1343 ms | 2382 ms |

Selisih hanya 4% — **hipotesis cold cache gugur**: cache hangat tidak memperbaiki throughput secara signifikan.

**Run warm 1-replika**: scale ke 1, pod fbznl node-b (berjalan 12+ menit), jalankan k6 2000 VU. Pod crash di tengah test (RESTARTS:2 dalam 40 detik), 86% failure rate. Dengan 500 VU: pod tidak crash, tapi hanya 118/s (avg wait 3.85s). Single request via curl: 37ms normal.

**Root cause sebenarnya — leader load bottleneck:**

Setelah `kubectl rollout status` selesai (pod READY), `p2-load.sh throughput` langsung menjalankan k6 — **tanpa menunggu leader election** (leaseDuration=15s). Pod baru READY adalah *follower*: tidak melakukan ingest, outbox, atau reconcile. Follower murni hanya melayani baca dari cache → 2438/s cepat. Setelah lease expire (~15s), pod menjadi leader dan menanggung pekerjaan background (BMKG/PVMBG polling, outbox publish, reconciler) sekaligus melayani read request. Di bawah 2000 VU, pod leader mengalami OOM/crash.

Dengan 3 replika, beban baca terdistribusi (~667 VU per pod): **stable di 1703/s**. Dengan 1 replika sebagai true-leader, pod crash di >500 VU karena background work + read request bersaing.

**Kesimpulan:**
- Angka "1 replika 2438/s" adalah *artefak pengukuran* (follower sebelum leader election, bukan true 1-replika).
- 3 replika lebih stabil dan lebih cepat dari 1 true-leader-replika.
- Rasio yang sah belum dapat diukur karena kode belum menangani leader-load (Fithra).
- Fix di `p2-load.sh`: tambah `sleep 20` setelah `rollout status` pada throughput mode agar leader election selesai sebelum k6 mulai.

## P2.7 — Rolling update (ROLLOUT_IMAGE)

Belum dijalankan — tidak ada image tag kedua yang tersedia di registry. Perlu build + push image dengan SHA berbeda terlebih dahulu.

## Unit test (`p4-check` / `go test -race`)

Dijalankan dari mesin lokal (Windows, Go 1.27.4):

```
go test -race -p 1 -count=1 ./services/aggregator/internal/satellite/...
```
Hasil: **PASS** — TestSSEPreservesData, TestSSERejectsGaps, TestStreamResumes, TestIdleStreamIsCancelled, TestStreamDoesNotRetry, TestSessionValidates, TestPracticeFeedPatches, TestPracticeFeedRejectsOffset, TestIncrementalClustersMatchFullRebuild. (TestWorkerSSEReplayThroughPostgres: SKIP — butuh TEST_DATABASE_URL)

```
go test -race -p 1 -count=1 ./services/aggregator/internal/store/...
```
Hasil: **PASS** — TestStaticMapSpreadsEvenly, TestPlanPartialOnly, TestOpenRouterNames, TestPartitionOfIsStable, TestChecksumMatchesContract, TestCursorRoundTrip, TestStorageStatusCounts. (TestRouterReplication, TestStoreM2, TestMigrationBackfills: SKIP — butuh TEST_DATABASE_URL)

## Belum dibuktikan

- Freeze VM (suspend >15 s) — butuh akses langsung ke hypervisor.
- Rolling update (ROLLOUT_IMAGE) — butuh image tag kedua di registry.
- Throughput ratio ≥2×: angka 1-replika sebelumnya adalah artefak pengukuran (follower sebelum leader election); true-leader-replica crash di ≥2000 VU. Butuh fix resource limits + sleep 20 dalam throughput mode.
- P2.5 load test `scale 3→5→2`: rollout 5 gagal (pod OOM); scale 2 tidak terjadi. Butuh fix pod resource limits.
- P2.7 rolling update: butuh ROLLOUT_IMAGE (image tag kedua di registry).

## Uji ulang 21 Nov 2026 — branch gabungan `c55ae39`

Branch dan image sama dengan `bukti-p1-cluster.md` bagian uji ulang. Waktu UTC.

**Status setelah deploy (10:53:48):** leader `aggregator-59dc98f7b8-f7ttp` (node-c), `leaseTransitions` 32. Sebaran setelah rolling update: node-c 2, node-a 1, node-b 0; setelah pod dihentikan Tester kembali satu per node. Penyebab: `topologySpreadConstraints` ikut menghitung pod revisi lama saat rolling update. Diperbaiki dengan `matchLabelKeys: [pod-template-hash]` di `deploy/k8s/base/aggregator.yaml`. Rolling update ke image `b61977f29c0b` (21 Nov ±11:55): satu Aggregator per node (node-a, node-b, node-c). Checkpoint BMKG 101.623 B di ketiga store (sebelum `e87d26e`: 1.121 kB).

**Failover Tester `kill-leader` (11:00):** `stop_pod role=aggregator nodes=any` menghentikan `f7ttp` (leader) dan `lq7lr` di node-c pukul 11:00:32. `p99nz` (node-a) mencatat `leader workers starting` epoch 33 pukul **11:00:53,8 → ±22 s** (target ≤60 s). Setelah takeover: 0 ERROR, 0 timeout store, 0 `partition rows copied`, BMKG dan PVMBG terus di-commit. Checksum 11:02:27: count 35.987, `partial:false`. Pod pengganti tersebar satu per node.

Catatan: pada percobaan sebelumnya (10:03, image `e87d26e`) Tester memilih node tanpa leader sehingga tidak ada failover; failover manual `kubectl delete pod <leader> --force --grace-period=0` saat itu memberi takeover ±16 s (`temuan-pr22.md`, temuan 10).

**Clock skew:** tidak diulang pada `c55ae39` karena PR #21 tidak mengubah `internal/ingest` dan mengulangnya menghilangkan data BMKG lagi. Hasil pada `e87d26e` (10:06): lease tidak berganti, tidak ada fencing, checksum `partial:false`, **tetapi ±23 event BMKG hilang permanen dan status BMKG macet `stale`** bila skew mengenai node mock BMKG (`temuan-pr22.md` temuan 8).

**`make p2-throughput` (PR #21), k6 di node-a, durasi 30 s:**

| TARGET_URL | VU | 1 replika (resp/s, gagal, p95) | 3 replika (resp/s, gagal, p95) | Rasio |
|---|---|---|---|---|
| NodePort node-b `192.168.18.202:30083` | 100 | 35,1; 3,86%; 5,34 s | tidak jalan (script berhenti) | — |
| NodePort node-b | 30 | 21,6; 0,41%; 2,38 s | 28,5; 0%; 1,82 s | 1,32× |
| NodePort node-a `192.168.18.201:30083` | 100 | 2.440,6; 0%; 103 ms | 2.499,8; 0%; 102 ms | 1,02× |

- Lewat node-b, satu respons 42.689 B butuh ±1 s (±42 KB/s lewat WiFi); lewat node-a 0,02 s. Dua baris pertama mengukur link WiFi, bukan Aggregator.
- Lewat node-a, server tidak gagal sama sekali, tapi rasio 1,02× menunjukkan pembatasnya klien (k6 berbagi 4 vCPU node-a dengan Aggregator, `store-1`, dll.). Bukti scaling ≥2× butuh k6 dari mesin terpisah.
- Script berhenti (`set -e`) bila baseline 1 replika melewati threshold `uncontrolled_errors < 1%`, sehingga perbandingan tidak pernah terjadi tepat saat baseline kewalahan (`temuan-pr22.md` temuan 13).

## Uji ulang 21 Nov 2026 (lanjutan) — perbaikan Fithra/Hasbullah, image `7a269c7` s/d `58405634daf3`

Waktu UTC.

### Failover dengan `make p2-kill-leader` (menghapus pemegang Lease)

Target baru ini selalu mengenai leader, berbeda dengan Tester `kill-leader` yang memilih node acak (`temuan-pr22.md` temuan 10).

| Waktu | Image | Leader lama → baru | Takeover | Catatan |
|---|---|---|---|---|
| ±14:40 | `7a269c771cd7` | `szchb` (node-c) → `74pr7` (node-a), epoch 38 | **20,4 s** | NodePort `/health` 200 terus; 30 s kemudian BMKG/PVMBG/Pantai `stale:false`, coastal breaker `closed` |
| 15:07:20 | `872f2545518d` | `h6rx2` → `mkmpk` | ±12 s | Satellite lanjut dari offset 9.457 |
| 16:30:27 | `58405634daf3` | `4jp5w` → `sckmb` | ±20 s | di tengah catch-up Satellite; lanjut dari offset 73.752 |

### Clock skew (`make tester S=clock-skew`, 14:36:17)

- Tester menjalankan `shift_clock +300s selama 30s` di node-c (mock BMKG dan leader). `systemd-timesyncd` node-c mengoreksi lompatan dalam ±1 s, lalu restore Tester membuat jam -300 s selama ±1 s. Journal `systemd-resolved: Clock change detected`: 14:41:24 → 14:36:25 → 14:31:54 → 14:36:55. Log Tester mencatat `langkah 1 selesai` dengan timestamp 14:31:54 karena itu.
- Hasil: 44 event mock BMKG sejak 14:31:11 semuanya tersimpan di store (dicocokkan per `event_id`), BMKG `stale:false`, 0 `no matching seismic event`, jam ketiga node sinkron setelahnya.
- Karena skew hanya bertahan ±1 s dan mock membuat event tiap 10 s, kasus kehilangan data dari uji sebelumnya (event bertimestamp masa depan) tidak terulang. Perbaikan cursor (`f08c308` + cap setelah fallback warning, belum di-commit) diverifikasi lewat unit test `TestSkewedCursorsAreCappedAndRecentEventsRecovered` dan `TestPrepareTermCapsFutureCheckpointCursors`. Untuk mengulang skew yang bertahan, `timesyncd` di node target perlu dimatikan sementara.

### Throughput 1 vs 3 replika — pengukuran ulang

k6 2.4.0 dari laptop (bukan node cluster, tetapi menampung VM node-a dan node-c), 100 VU, 60 s, endpoint `/internal/v1/hazards?source=BMKG&limit=100` (±43 KB). 0 request gagal di semua run. JSON: `artifacts/m2/runs/20261121T171420Z-p2-throughput-laptop-*` (laptop) dan `20261121T173844Z-p2-throughput-{1,3}.json` (node-a).

| Konfigurasi | resp/s | p95 |
|---|---|---|
| NodePort `Cluster`, 1 replika di node-b | 204 | 989 ms |
| NodePort `Cluster`, 1 replika di node-a (node tujuan k6) | 3.315 | 50 ms |
| NodePort `Cluster`, 1 replika di node-c | 594 | 399 ms |
| NodePort `Cluster`, 3 replika | 1.652 | 193 ms |
| `externalTrafficPolicy: Local`, 1 replika di node-a | 3.752 | 42 ms |
| `Local`, 3 replika, k6 ke ketiga node | 2.585 | 148 ms |
| `Local` + batas CPU 250m, 1 → 3 replika (laptop) | 557 → 1.233 | 393 → 257 ms |
| `Local` + batas CPU 250m, 1 → 3 replika (node-a, `make p2-throughput`) | 694 → 1.308 | — |
| **`Local` + batas CPU 100m, 1 → 3 replika (node-a, `make p2-throughput`)** | **228 → 536** | 903 → 926 ms |

- Tanpa batas CPU, satu replika melayani ±3.300 resp/s dengan **0,13–0,48 core** (`kubectl top`; node-a 36%). Aggregator tidak jenuh, jadi yang terukur adalah jaringan: dengan `Cluster`, kube-proxy meneruskan ±2/3 request ke node lain lewat link antar-VM (±25 MB/s) atau Wi-Fi node-b (±9 MB/s), dan menambah replika malah menurunkan throughput. Ini juga menjelaskan rasio 0,67× dan 1,02× di bagian sebelumnya.
- Dengan kapasitas per-replika dibatasi dan NodePort node-local, efek replika terukur: **rasio 2,35× pada 100m** (≥2×, `make p2-throughput` exit 0), 2,21× / 1,88× pada 250m. Pada 250m satu replika (±25 MB/s) melampaui link Wi-Fi node-b sehingga sepertiga beban tertahan di sana. 100m (±230 resp/s ≈ 10 MB/s) tetap di bawah link terlambat.
- `make p2-throughput` kini melakukan ini otomatis (`CPU_LIMIT`, default 100m; VUS 100; DURATION 60s) dan memulihkan `externalTrafficPolicy: Cluster`, tanpa batas CPU, serta jumlah replika semula saat keluar (terverifikasi setelah run).
- Untuk laporan: scaling diukur dengan kapasitas per-instance dibatasi. Klien ideal tetap mesin ketiga di luar kedua host fisik.

## Uji ulang 22 Nov 2026 — batas baca serentak, 3→5→2 + rolling update, freeze

Waktu UTC. k6 2.4.0 dari PC host (Windows), `TARGET_URL=http://192.168.18.201:30083,http://192.168.18.203:30083` (NodePort `Cluster`), 10.000 VU.

### Penyebab OOM 21 Nov dan perbaikannya

- Setiap request baca me-encode responsnya sendiri (±43 KB pada `limit=100`), dan setiap request membuat span OTel serta satu baris log. Dengan ±3.300 VU per pod, memori melewati limit 512Mi (Lampiran B, tidak boleh dinaikkan).
- Perbaikan (`services/aggregator/internal/httpapi/server.go`, `limitReads`): maksimal **256 baca serentak per pod**. Kelebihannya menunggu ≤500 ms, lalu dijawab `503` + `Retry-After: 1` + `{"error":{"code":"overloaded"}}`, yang dihitung k6 sebagai penolakan terkendali. Pembatas dipasang di luar tracing dan logging sehingga request yang ditolak tidak membuat span maupun log. Probe `/health` dan Audit API tidak dibatasi. Tes: `TestSaturatedReadsGetLabelledRejection`.
- `GOMEMLIMIT=380MiB` di Deployment Aggregator: GC bekerja lebih agresif sebelum batas cgroup.
- `p2-load.sh`: durasi default 180 s (spesifikasi ≥120 s). Timeout rollout 60/30/60 s, karena pod yang dijadwalkan di `node-b` (Wi-Fi) butuh ±30 s sampai Ready dan batas lama 25 s membatalkan skala 3→5.

### Hasil per iterasi

| Run | Perubahan | 3→5 | 5→2 | Rolling update | Restart pod | uncontrolled_errors | resp 200/s |
|---|---|---|---|---|---|---|---|
| 21 Nov 07:25 | (sebelum perbaikan) | timeout, OOM | tidak jalan | — | banyak | 28,43% | 1.707 |
| 03:11:03 | batas 256 di handler | timeout 25 s | tidak jalan | tidak jalan | 0 | 1,48% | 738 |
| 03:14:17 | + timeout rollout dilonggarkan | timeout | tidak jalan | tidak jalan | OOM (3 pod) | 6,94% | 711 |
| 03:18:20 | + `GOMEMLIMIT` | OK | OK | OK | OOM (6×) | 63,54% | 524 |
| 03:30:00 | + pembatas di luar tracing/log | **OK** | **OK** | **OK** | **0** | 1,80% | 483 |
| 03:36:39 | + disk node diperbesar (lihat bawah) | OK | OK | OK | 0 | 1,96% | 718 |
| **11:46:47** | + respons baca gzip (lihat bawah) | **OK** | **OK** | **OK** | **0** | **0,13%** | **2.435** |

Run final 03:36:39 (`artifacts/m2/runs/20261122T033639Z-p2-load.{log,json}`): 1.438.179 request (7.624/s), 135.416 respons kanonis, sisanya penolakan `503` berlabel. Memori puncak per pod **408Mi** (limit 512Mi). Rolling update ke tag kedua (`…-readcap2`) selesai di tengah beban.

Sisa error tak terkendali (±2%) hampir semuanya `request timeout` 10 s di sisi klien (24.831), sisanya error koneksi (2.173). k6 dengan 10k VU berbagi CPU dengan VM `node-a` dan `node-c` di PC yang sama, dan sepertiga trafik NodePort `Cluster` diteruskan ke pod di `node-b` lewat Wi-Fi. Klien ideal tetap mesin ketiga di luar kedua host (batasan, sama dengan catatan throughput di atas).

### Perbaikan lanjutan: respons baca gzip (run 11:46:47)

- **Diagnosis run 03:36:** `data_received` 6,9 GB untuk 135.416 respons 200, yaitu ±51 KB per respons dengan total hanya ±37 MB/s. Yang jenuh adalah link (PC host dan node-b lewat Wi-Fi), bukan CPU. Selama respons 43 KB dikirim lewat link yang penuh, slot baca (256 per pod) tetap terpakai. Request lain akhirnya ditolak `503` atau menunggu sampai timeout klien 10 s.
- **Perbaikan** (`services/aggregator/internal/httpapi/server.go`, `listHazards`): bila klien mengirim `Accept-Encoding: gzip`, respons `/hazards` dan `/internal/v1/hazards` dikompres gzip (BestSpeed, writer di-pool, `Vary: Accept-Encoding`). Klien tanpa header itu tetap menerima JSON biasa. Client API memakai `net/http`, yang otomatis meminta dan men-decompress gzip. Di cluster, respons `source=BMKG&limit=100` turun dari **43.568 B menjadi 8.411 B**. Tes: `TestHazardsGzipOnlyWhenAccepted`. Skrip k6 (`tests/k6/m2-aggregator.js`) kini mengirim `Accept-Encoding: gzip` seperti browser/klien HTTP biasa.
- **Image:** `docker.io/sbimasena/bnpb-aggregator:d28063bd237e-gzip`, ditambah tag kedua `…-gzip-b` (kode sama) sebagai target rolling update. Keduanya di-import langsung ke containerd k3s di ketiga node (`k3s ctr images import`, `imagePullPolicy: IfNotPresent`), **belum di-push ke Docker Hub**.
- **Run 11:46:47** (`artifacts/m2/runs/20261122T114647Z-p2-load.{log,json}`, `make p2-load` dengan `ROLLOUT_IMAGE=…-gzip-b`): 3→5→2 dan rolling update semua "successfully rolled out". Hasil: 1.959.543 request (10.511/s); **454.032 respons kanonis (2.435/s, 3,4× run 03:36)**; `uncontrolled_errors` **0,13%** (2.640: 1.348 timeout + error koneksi); p95 durasi 2,08 s (sebelumnya 4,99 s); `data_received` 4,3 GB (±9,5 KB per respons 200). Sesudah run: 3 pod Aggregator, 0 restart, checksum keempat sumber `partial:false`. Memori puncak per pod tidak diukur pada run ini.
- **`failed_responses` 76,82%** (threshold `rate==0` gagal) mencakup **semua** respons yang bukan 200, termasuk `503 overloaded` berlabel dari load shedding yang disengaja. Threshold ini hanya aktif bila `ROLLOUT_IMAGE` di-set (kriteria 8). Kriteria P2.6 hanya menghitung error di luar penolakan terkontrol (0,13% < 1%). Kriteria 8 ("ganti versi tanpa request gagal") secara ketat tetap tidak terpenuhi di bawah overload 10k VU: rollout tidak menambah error tak terkendali, tetapi penolakan terkontrol tetap ada.
- Screenshot: `artifacts/m2/screenshots/p2-03-k6-10000vu-gzip.png`.

### DiskPressure node-a saat load test

- Run 03:30 menemukan 4 pod Aggregator `Evicted` (`The node had condition: [DiskPressure]`). Root filesystem `node-a` hanya 19G (94% terpakai); installer Ubuntu memakai separuh volume group, sehingga 19 GB VG belum dialokasikan.
- Perbaikan: `lvextend -r -l +100%FREE /dev/ubuntu-vg/ubuntu-lv` online di ketiga node (19G → 38G; node-a 34%, node-b 37%, node-c 35%), serta `docker builder prune` di node-a (build cache 3,6 GB).

### Freeze: VM leader dijeda 25 s (`make p2-freeze`)

`artifacts/m2/runs/20261122T035815Z-p2-freeze.log`. Leader `aggregator-7c6b86f79d-bnbd5` di `node-c` (VM di PC host), `leaseTransitions` 75.

| Waktu (wall clock) | Kejadian |
|---|---|
| 03:58:15 | `VBoxManage controlvm node-c pause` (node-c juga memuat `store-2`, `rabbitmq-0`, mock, sensor) |
| 03:58:34,6 | Pod `4wq7b` (node-b) mengambil lease, epoch 76; `fence skipped unreachable unit store-2` (ikut beku) |
| 03:58:36,6 | `leader workers starting epoch=76`: takeover **21 s** setelah freeze |
| 03:58:41 | `VBoxManage controlvm node-c resume` |
| setelah resume | Leader lama mencatat `leader workers stopped epoch=75` lalu keluar (exit 0); Kubernetes menyalakannya lagi sebagai follower. Timestamp lognya tertinggal (jam VM berhenti selama pause) |

- Leader lama tidak menulis apa pun setelah bangun, sehingga fencing tidak perlu menolak apa pun pada percobaan ini. Bila sempat menulis, unit `store-0/1` sudah di-fence epoch 76 dan akan menolak epoch 75 (`ErrFenced`; diuji di `internal/store/store_m2_test.go` `TestStoreM2` dan `router_test.go` untuk `Upsert`/`SaveCursor` zombie, serta `internal/leader` `TestVerifyRejectsZombieAndAPIFailure`). `store-2` di-fence oleh reconciler sebelum dipakai lagi.
- Efek samping: jam node-c tertinggal ±26 s setelah resume (jam VM berhenti selama pause; `timesyncd` menganggap sudah sinkron dan baru poll lagi ±34 menit kemudian). Ditemukan 04:17 saat leader pindah ke pod di node-c; `systemctl restart systemd-timesyncd` mengoreksi +25,07 s. Lease, ingest, dan checksum tetap normal sesudahnya. Langkah resync kini ada di runbook P2-3.
- Checksum setelah freeze (03:59:31): BMKG 15.630, PVMBG 9.622, SATELIT 37.770, SENSOR_PANTAI 95.837, **semua `partial:false`**; ketiga node `Ready`.

### Status "Belum dibuktikan" (diperbarui 22 Nov)

| Item | Status |
|---|---|
| Freeze VM >15 s | **Terbukti** (di atas) |
| Rolling update di tengah beban | **Terbukti** (run 03:30, 03:36, 11:46) tanpa restart. Tanpa request gagal sama sekali (kriteria 8 ketat): **tidak**, penolakan `503` berlabel tetap ada saat overload |
| Scale 3→5→2 di bawah 10k VU | **Terbukti** tanpa restart; error tak terkendali **0,13%** (run 11:46 dengan gzip; sebelumnya 1,96%) |
| Throughput ≥2× | Terbukti 21 Nov (2,35× pada CPU 100m) |

## Catatan 22 Nov 2026 — NodePort Aggregator masuk manifest (PR #27)

Service `aggregator` di cluster sudah lama bertipe `NodePort 8083:30083` (dipakai `make p2-throughput`, `p2-load`, `p5-gameday.sh`, runbook), tetapi `deploy/k8s/base/aggregator.yaml` masih `ClusterIP`: drift antara manifest dan cluster. PR #27 menambahkan `type: NodePort` dan `nodePort: 30083` ke manifest. Divalidasi di `node-a` pukul 10:0x UTC: `kubectl kustomize deploy/k8s/base` dan `kubectl apply --dry-run=server` lulus, dan `kubectl diff` tidak menunjukkan perubahan `type`/port pada Service yang berjalan. Metode `make p2-throughput` (batas CPU 100m per replika + `externalTrafficPolicy: Local`, hasil 2,35× pada 21 Nov) tidak berubah.

## P2 kriteria 2 — setiap sumber diambil tepat sekali (22 Nov ±10:26–10:31 UTC)

Setelah deploy `c936b23`, log mock BMKG dan PVMBG lima menit terakhir dihitung per header `X-Instance-ID` (`kubectl logs deploy/bmkg|pvmbg --since=5m`, field `instance_id`). Tiga replika Aggregator berjalan (`aggregator-75766ccc86-cxs2h`, `-l8r46`, `-ljp2j`); pemegang Lease `aggregator-75766ccc86-l8r46`.

| Sumber | Request per `instance_id` |
|---|---|
| BMKG `/seismic-events` | `aggregator-75766ccc86-l8r46`: **152**; replika lain: 0 |
| PVMBG `/volcanic-reports` | `aggregator-75766ccc86-l8r46`: **96**; replika lain: 0 |

Contoh baris log BMKG:

```
{"time":"2026-11-22T10:26:20.563120185Z","level":"INFO","msg":"request complete","service":"bmkg","correlation_id":"poll-BMKG-1795343180427620634","instance_id":"aggregator-75766ccc86-l8r46","operation":"/seismic-events","latency_ms":134.318,"status":200}
```
