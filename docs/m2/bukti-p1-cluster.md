# Bukti P1 di cluster nyata (15 Nov 2026)

Cluster k3s 3 node: `node-a` (192.168.18.201, control plane), `node-b` (.202), `node-c` (.203). Deploy dilakukan dari `node-a` dengan `make deploy OVERLAY=cluster REGISTRY=docker.io/sbimasena`. Partisi P=24, RF=2, store-0 di node-b, store-1 di node-a, store-2 di node-c.

Cara membaca:
- `make p1-distribution` menampilkan `node | rows | partisi` per unit.
- `make p1-checksum` menampilkan `count` total.
- Placement dianggap sehat bila setiap unit memegang 16 partisi dan **jumlah rows ketiga unit = 2 × count**: setiap event punya tepat dua salinan, dan tidak ada salinan lama yang tertinggal.

Semua waktu dalam UTC.

| Waktu | Kejadian | Checksum | Distribusi (b / a / c) | Arti |
|---|---|---|---|---|
| 12:57 | Baseline setelah deploy | 292, `partial:false` | 187 / 95 / 87 | Sebelum sweep ada; map 16/16/16 |
| 13:02 | `make tester S=kill-store` (store-2) | 348, `partial:false`, unit lengkap | n/a | Pod dibuat ulang < 60 s, tanpa re-replikasi |
| 13:11 | `node-c` di-poweroff (aggregator ikut mati, di-force-delete) | 462, `partial:false`, `unavailable_units:["store-2"]` | 460 / 460 / n/a, 24 partisi masing-masing | P1.6: 16 slot store-2 pindah ke store-0/1 dalam satu pass |
| 13:15 | `node-c` dinyalakan | 510, `partial:false` | map 16/16/16 | Rebalance 4 move per pass; data lama store-2 disusulkan dengan aturan "ingested_at terbaru menang" |
| 13:26 | Setelah sweep dideploy | 633 | 438 / 404 / 424, 16 partisi masing-masing | **438+404+424 = 2×633** (P1.1: setiap unit ±2/3, tidak ada yang memegang semua) |
| 13:35 | `make tester S=wipe-node` (node-c) | 670, `partial:false`, `unavailable_units:["store-2"]` | 670 / 670 / n/a, 24 partisi masing-masing | Re-replikasi ke dua unit yang tersisa |
| 14:45 | `node-c` kembali (setelah loop tester diputus) | 1511, `partial:false` | 1218 / 1171 / 1001 | Salinan lama belum di-sweep: outbox pending karena broker mati |
| 15:02 | Broker hidup lagi, outbox terkirim | 1715, `partial:false` | 1166 / 1126 / 1138, 16 partisi masing-masing | **1166+1126+1138 = 2×1715**; count tidak pernah turun sepanjang uji |
| 15:06 | `make tester S=partition` (dua kali) | 1763, `partial:false` | 1201 / 1156 / 1169, 16 partisi masing-masing | 2×1763; sekat tidak teramati (lihat `temuan-tester-agent.md`) |
| 15:13 | `make p1-amnesia S=store-1` (simulasi disk kosong) | 1855, `partial:false` | 1263 / 1217 / 1230, 16 partisi masing-masing | **P1.5**: 15:13:50.76 `store unit lost its data` (incarnation `50cfcb90…` → `435afe7e…`); 16 partisi disalin dari store-0/2; 15:13:51.09 `refilled; serving reads again` (±0,3 s); anti-entropy lalu menyamakan 1–2 rows yang masuk selama pengisian. 1263+1217+1230 = 2×1855 |
| 15:17 | Kehilangan node berikutnya: `node-c` di-poweroff setelah amnesia store-1 pulih (Aggregator tidak di `node-c`) | 1894, `partial:false`, `unavailable_units:["store-2"]` | 1895 / 1895 / n/a, 24 partisi masing-masing | **P1.6**: map hanya berisi store-0/1; rows = count (selisih 1 karena ingest di antara perintah) |
| 15:18 | `node-c` dinyalakan lagi | 1910, `partial:false`, unit lengkap | 1306 / 1252 / 1264, 16 partisi masing-masing | Rebalance 16/16/16; 1306+1252+1264 = 2×1911 |

**P1.2, lonjakan satu wilayah** (`make p1-surge N=3000 REGION="Sulawesi Tengah"`, gempa susulan sintetis di sekitar Palu; log di `artifacts/m2/runs/`):

| Run | Potongan | Waktu per potongan | Tulis selama run (b / a / c) | Rows sesudah (b / a / c) | Checksum |
|---|---|---|---|---|---|
| 15:26 | 500 | tepat 3,00 s (timeout unit) | 3651 / 4369 / 4316 | 1769 / 3336 / 3326 | 5023; 30 s kemudian 3363 / 3348 / 3335 = 2×5023 |
| 15:29 | 100 | 0,55–1,25 s | 4294 / 4416 / 4427 | 5338 / 5371 / 5379 | 8044, `partial:false`; 5338+5371+5379 = 2×8044 |

- **Lonjakan tetap tersebar.** Pada run 15:29, tulisan ke tiap unit selisihnya hanya ±1,5%, dan tiap unit memegang 66,4–66,9% rows. Unit dipilih dari hash `hazard_id`, bukan dari wilayah, jadi 3000 gempa di satu wilayah tidak menumpuk di satu node.
- **Node lambat tidak menghambat ingest.** Pada run 15:26, `node-b` tidak sanggup menulis 500 event dalam `operationTimeout` 3 s. Write ke store-0 dibatalkan di setiap potongan, dan log mencatat 53 baris `lags`/`rows copied` untuk store-0. Ingest tetap sukses karena replika lain sudah commit, lalu anti-entropy menyusulkan store-0 dalam < 30 s. Selama jeda itu event tersebut hanya punya 1 salinan.
- Penyebab `node-b` lebih lambat belum diketahui (disk atau CPU VM). Untuk dataset asisten, gunakan `CHUNK=100`.

**Kepemilikan storage ditegakkan platform** (`make p1-netpol`, NetworkPolicy `store-ingress`, 15:4x). Dua pod uji dibuat dengan image yang sama dan hanya berbeda label. Pod `role=api` (meniru client-api) mendapat `store-0.store:5432 - no response`. Pod `role=aggregator` mendapat `accepting connections`. Secret `store-credentials` hanya di-mount ke Aggregator dan store. Setelah policy dipasang, Aggregator tetap utuh: 5436+5471+5467 = 2×8187. Uji ulang Tester Agent dengan policy aktif: `kill-store` (15:44:46, store-2) berakhir "keadaan bersih", dan checksum 20 s kemudian `count:8223`, `partial:false`. `schema` gagal karena Service mock belum berlabel `role=mock`; penyebabnya bukan policy (lihat `temuan-tester-agent.md`).

**P1.7, dashboard = Audit API:** pada 15:24:48 UTC, dashboard (`make p1-dashboard` lewat SSH tunnel, rentang "Semua") menunjukkan **1.981 kejadian**, dan `make p1-checksum` pada saat yang sama menunjukkan `count:1981`, `partial:false`, `unavailable_units:[]`.

`make p1-audit` (15:1x): halaman `events?source=BMKG&limit=2` mengembalikan `next_cursor` dan `partial:false`. `location` untuk `haz_07844eed…` menunjukkan `primary` store-2 (node-c) dan `replica` store-1 (node-a), yaitu dua node berbeda.

**Wipe-node sungguhan juga memicu amnesia.** Log Aggregator saat itu hilang karena pod-nya di-force-delete, tetapi map membuktikannya: incarnation store-2 berubah dari `7713a089…` (sebelum 13:35) menjadi `8d3198f9…`. Incarnation baru hanya muncul bila PostgreSQL di-init ulang dari disk kosong. Unit itu kemudian tercatat dengan incarnation barunya dan kembali memegang 16 partisi dengan rows = 2 × count. Simulasi 15:13 melengkapi bukti ini dengan log lengkap.

## Bug yang ditemukan dan diperbaiki

| Gejala | Penyebab | Perbaikan |
|---|---|---|
| bmkg/pvmbg `CrashLoopBackOff`: `address :tcp://10.43.x.x:8081: too many colons` | Kubernetes menyuntik `BMKG_PORT=tcp://…` dari Service `bmkg`, sehingga port listen mock tertimpa | `enableServiceLinks: false` di `deploy/k8s/base/mocks.yaml` |
| `kubectl apply -k`: `cannot unmarshal number into ... newTag` | Git SHA pendek `588683745877` berisi angka semua, sehingga dibaca YAML sebagai number | Tag diberi kutip di `scripts/k8s/deploy.sh`; pola sed di `ops.sh` (`make reset`) disesuaikan |
| Move `adding: store-0` macet dan incarnation hanya berisi store-0 | Map dibuat saat hanya store-0 hidup (pull image postgres 22 menit). store-1 dan store-2 berbagi 8 partisi dan saling menunggu sumber yang tercatat | `recoverAmnesia` melewati partisi yang tidak punya replika tercatat; anti-entropy yang menyamakan isinya |
| Setiap unit tetap memegang 24 partisi setelah move | Salinan lama di unit asal tidak dihapus | Sweep di akhir pass, setelah map tersimpan. Event dengan outbox yang belum terkirim tetap disimpan |

## Masalah operasional (runbook)

- **Pull image lambat.** `postgres:17-alpine` (117 MB) butuh 22 menit di VM. Untuk percepat: `k3s ctr images export`, salin ke node lain, lalu `import`.
- **Node mati juga mematikan Aggregator.** Aggregator masih 1 replika dan belum ada leader election (P2), sehingga ingest berhenti sampai pod dipindah. Pemindahan otomatis menunggu toleration 300 s; percepat dengan `kubectl -n $NS delete pod -l app=aggregator --force --grace-period=0`. Timer re-replikasi 60 s dihitung ulang sejak Aggregator baru start.
- **Broker `Pending` setelah node-c kembali.** `broker.yaml` memaku RabbitMQ ke control plane (`node-a`), tetapi PVC `data-rabbitmq-0` lama terikat ke `node-c` (local-path). Akibatnya pod tidak bisa dijadwalkan di node mana pun:
  ```bash
  kubectl -n $NS delete pvc data-rabbitmq-0 --wait=false
  kubectl -n $NS delete pod rabbitmq-0
  ```
  Pesan yang masih di antrean broker lama hilang (wilayah P3). Outbox di store tidak hilang dan dikirim ulang.
- **Sweep menunggu outbox.** Selama broker mati, salinan lama tetap ada, jadi persamaan 2 × count baru berlaku setelah relay mengirim outbox.
- **Loop tester `wipe-node`.** Lihat `temuan-tester-agent.md`.

## Belum dibuktikan

- `make p1-load` (P1.1) menunggu dataset katalog asisten, yang dibagikan setelah tenggat. Gunakan `CHUNK=100`.
- Partisi jaringan yang benar-benar menyekat pod: `S=partition` sudah dijalankan, tetapi tidak berpengaruh; menunggu jawaban asisten.

## Uji ulang 21 Nov 2026 — branch gabungan `c55ae39`

Branch `fix/p2-bmkg-checkpoint-size` = PR #22 (`fix/field-report-context`) + PR #21 (`feat/m2-p2-p4-acceptance`) + `fedc2da` (anti-entropy delta) + `e87d26e` (checkpoint BMKG). Image `c55ae396cc5a`, readiness Aggregator `httpGet /health` (dari PR #21). Waktu UTC.

**Baseline 10:57:48** (`make p1-checksum`, `make p1-distribution`): count 35.931, `partial:false`, `unavailable_units:[]`. Rows per unit: node-b 24.006, node-a 23.866, node-c 23.990 = 71.862 = 2 × count. Setiap unit 16 partisi, tidak ada `adding`/`removing`.

**`kill-store` (Tester, 10:58)** — `stop_pod role=store nodes=any`, terpilih `store-2` (node-c). Dipantau tiap ±2 s:

| Waktu | Aggregator Ready | Store | Checksum |
|---|---|---|---|
| 10:58:08 | 3/3 | semua ok | 35.935, `partial:false`, `[]` |
| 10:58:11 | 3/3 | `store-2` Running (belum ready) | 35.936, `partial:false`, `["store-2"]` |
| 10:58:16 | 3/3 | semua ok | 35.936, `partial:false`, `[]` |
| 11:00:09 | 3/3 | semua ok | 35.960, `partial:false`, `[]` |

- Readiness `/health` tidak menjatuhkan Aggregator saat satu store mati: `Router.Ping` hanya gagal bila tidak ada unit yang menjawab (`noUnitAnswered`).
- Count tidak pernah turun; API tetap menjawab selama unit hilang.
- Store yang menggantung tanpa RST (node mati) belum diuji di cluster. Sebelumnya `Router.Ping` menunggu hingga 3 s (`operationTimeout`) sementara `timeoutSeconds` probe 4 s; kini Ping memakai `unitReadTimeout` (1 s). `TestPingGivesUpOnHungUnitsWithinReadTimeout` (listener TCP yang tidak menjawab): 1,0 s dengan perbaikan, 3,0 s tanpa perbaikan.

**Anti-entropy** (`fedc2da`): sebelumnya selisih 1 row pada satu replika membuat seluruh partisi (±700 row) disalin bolak-balik setiap pass (69 kali per 30 menit, ke `store-0` yang write-nya sering timeout). Kini hanya row yang hilang/lebih baru yang disalin (indeks `hazard_id, ingested_at` + `report_id`). Di cluster sejak leader baru berjalan: `partition rows copied` 0, timeout write store 0.

**DLQ saat re-replikasi** (`c0e884b`): `syncPartition` (pindah partisi dan pengisian ulang setelah amnesia) kini juga menyalin `unprocessed_messages` partisi itu apa adanya. Sebelumnya hanya `hazard_events` dan `field_reports`, sehingga setelah `wipe-node` dan re-replikasi pesan DLQ partisi yang dipindah tinggal satu salinan. `TestSyncPartitionCopiesDeadLettersVerbatim` (Postgres) lulus.

## Verifikasi perbaikan P1 di cluster — 21 Nov 2026, image `b61977f29c0b`

**Restart per secret (`deploy.sh`).** Anotasi `bnpb/secrets-hash` kini per workload: `bmkg` = `pvmbg` (`source-credentials`), `rabbitmq` = `dashboard-consumer` = `notification-consumer` (`broker-credentials`), `sensor-pantai` sendiri (`pantai-token`). Deploy ulang tanpa perubahan (`SKIP_BUILD=1`): tidak ada revisi Deployment/StatefulSet baru dan himpunan pod identik (tidak ada restart).

**Readiness saat `kill-store` (12:03).** `store-2` hilang (`unavailable_units:["store-2"]` 12:03:00): Aggregator Ready 3/3, `/health` 200 dalam ±75 ms, `partial:false`, count naik terus (36.974 → 36.990).

**Amnesia `store-1` (`make p1-amnesia S=store-1`, 12:04:41).** `p1-amnesia` kini juga mengosongkan `field_reports` dan `unprocessed_messages`, seperti disk yang benar-benar dihapus.
- `store unit lost its data` 12:04:46 → `store unit refilled; serving reads again` 12:04:58,8 (±13 s). Setiap partisi membawa `rows` (±1.500), `reports`, dan `dead_letters` (25–46; total 539).
- Setelahnya: checksum count 37.002, `partial:false`; rows node-b 24.718 + node-a 24.562 + node-c 24.728 = 74.008 = 2 × count; setiap unit 16 partisi.
- DLQ `store-1` di 16 partisinya: 541 dari 549 baris yang ada di unit lain untuk partisi itu. 8 sisanya (partisi 16–23, `parked`, dibuat 09:52–09:54) hanya ada di `store-0`, yang bukan replika partisi itu: salinan tertinggal dari pemindahan partisi sebelum perbaikan (sweep tidak menghapus DLQ, dan pemindahan lama tidak menyalinnya). Pesan tetap terbaca (pembacaan DLQ ke semua unit), tetapi hanya satu salinan.
- Celah tersisa: DLQ belum punya anti-entropy, sehingga salinan yang sudah menyimpang atau tertinggal di unit non-replika tidak direkonsiliasi.

## Dampak checkpoint Satellite ke storage dan perbaikannya — 21 Nov 2026

Waktu UTC. Rincian P4 di `bukti-p4-cluster.md` dan `temuan-pr22.md` temuan 15.

**Gangguan (image `872f2545518d`, 15:21–15:27).** Checkpoint Satellite sebesar ±14 MB ditulis ke ketiga unit lewat `saveCursor` (`operationTimeout` 3 s, perlu 2 ack). Sejak 15:21:35 hanya 1 unit yang sempat ack (`store: checkpoint acknowledged by 1 units; need 2`), dan worker mencoba lagi tiap 10 s dengan payload yang sama. Write lain ikut timeout:
- upsert Pantai `canonical operation failed` 2.994 ms lalu `store replica write failed; replica now lags` (unit `store-2`);
- `record PVMBG dead letter: store-1: timeout: context deadline exceeded`;
- `partition map reload failed`;
- Pantai `stale:true`.

Warning non-Satellite di leader: 2/menit (15:05) → 22, 9, 16, 32/menit (15:21–15:24). Status storage tetap `reachable`/`serving` di ketiga unit dan `under_replicated_partitions` 0, jadi gangguannya berupa saturasi, bukan unit yang hilang. Setelah worker dimatikan (15:27): 0 warning/menit, semua sumber `stale:false`.

Ukuran baris `ingest_cursor` saat itu: `SATELIT` 14.410.256–14.458.585 B per unit, `BMKG` ±107 kB.

**Perbaikan di sisi storage.**
- Migration `007_satellite_delta_cursor.sql` (`5840563`) menambahkan `SATELIT_DELTA` ke CHECK `ingest_cursor.source`. Tanpa migration ini write delta ditolak semua unit (`checkpoint acknowledged by 0 units`), yang tertangkap oleh `TestWorkerSyntheticBurstCatchUp`. Migration diterapkan bertahap oleh `Router` saat rolling update (`version < len(migrations)`).
- `store/router_test.go` dan `coastal/processor_postgres_test.go` memakai `OpenRouterWithMemoryMap` (`872f254`), sehingga `go test -race` + PostgreSQL lulus di dalam pod dengan `KUBERNETES_SERVICE_HOST` terisi.
- Sebelum deploy 16:24, baris `SATELIT` lama dihapus langsung di ketiga unit (14.636.932 / 14.646.760 / 14.601.649 B). Restore akan mencoba menulis ulang 14,6 MB itu ke 3 unit dan macet di tempat yang sama.

**Verifikasi (image `58405634daf3`).** Selama catch-up 2,09 jt offset (16:24–17:07) dan sesudahnya: baris `SATELIT` 1,45–5,81 MB, `SATELIT_DELTA` ≤0,15 MB, 0 warning non-Satellite per menit, storage 3 unit `serving`, semua sumber `stale:false`. Satu-satunya warning storage setelah deploy berikutnya adalah `store unit unreachable` untuk ketiga unit tepat saat pod baru start (17:35:02, sebelum jaringan pod siap), tanpa pengulangan.

**Catatan.** `saveCursor` tetap menulis ke ketiga unit walau cukup 2 ack. Dengan checkpoint yang kini kecil hal ini tidak mendesak, tetapi tetap menjadi biaya bandwidth unit ketiga untuk payload besar.

## Anti-entropy DLQ — 21 Nov 2026, image `73b8c813f29f` (`73b8c81`)

Menutup celah di bagian "Verifikasi perbaikan P1" di atas (DLQ belum punya anti-entropy). Waktu UTC.

**Perubahan.**
- Digest anti-entropy kini juga membandingkan DLQ per partisi (jumlah baris dan jumlah versi). Versi status = `attempts × 3 + (parked 0 / retrying 1 / recovered 2)`, mengikuti siklus `pvmbgreplay.go`: `attempts` hanya naik, pesan `parked` di-queue ulang sebagai `retrying` dengan `attempts` sama, dan `recovered` final. Partisi yang berbeda diselaraskan dua arah lewat `syncDelta`.
- `importUnprocessed` mengganti salinan hanya dengan versi yang lebih baru (`ON CONFLICT ... DO UPDATE ... WHERE versi lama < versi baru`), sehingga unit yang sempat mati saat status berubah ikut mengejar, dan salinan lama tidak pernah menimpa yang baru.
- `sweep` mengadopsi salinan yatim: baris DLQ di partisi yang tidak direplikasi unit itu disalin ke semua write replica partisinya, lalu dihapus dari unit asal. Partisi yang salah satu replikanya mati atau gagal tetap ditahan sampai putaran berikutnya. Pembacaan DLQ tetap menyisir semua unit, jadi pesan tidak pernah hilang dari pandangan.
- Test (PostgreSQL, `-race`): `TestAntiEntropyReconcilesDeadLetters` (salinan hilang, `recovered` yang tertinggal, re-queue yang tertinggal, dan salinan lama tidak menimpa yang baru) dan `TestSweepAdoptsOrphanDeadLetters` (adopsi, plus penahanan saat replika mati). Seluruh paket aggregator lulus di pod node-b.

**Sebelum deploy (18:0x).** Partisi 0–15 di store-0 dan store-2/store-1 (genap/ganjil), 16–23 di store-1 dan store-2.

| Unit | Total | `recovered` | Baris di partisi yang bukan miliknya |
|---|---|---|---|
| store-0 | 1.036 | 15 | p16=1, p17=2, p18=3, p22=1, p23=2 (9 baris) |
| store-1 | 1.097 | 16 | — |
| store-2 | 1.075 | 14 | p7=1, p9=1 (2 baris) |

Jumlah baris berbeda antar replika di 7 partisi: p2 69/70, p5 80/79, p8 63/61, p9 73/72, p13 71/70, p14 69/71, p15 65/66.

**Setelah deploy.** Log leader: `partition rows copied ... dead_letters 1–3` untuk p2, p8, p14 (18:05:40), salinan ke store-2 untuk p16/17/18/22/23 (18:06:04), dan `orphan dead letters adopted by their replicas` (10×, misalnya store-0 137 baris pukul 18:12:27 saat partisi berpindah). Pemeriksaan 18:23:38 terhadap peta partisi yang berlaku (setelah `moves_in_progress` 0 dan `under_replicated_partitions` 0):
- 24 partisi, **0 berbeda** antar replika: jumlah, jumlah `recovered`, total `attempts`, dan hash `md5(message_id || status || attempts)` per partisi identik.
- **0 baris** di partisi yang bukan milik unitnya.
- 1.690 dead letter unik; store-0 1.166 + store-1 1.086 + store-2 1.128 = 3.380 = 2 × 1.690.
- Checksum Audit 18:23:52: count 111.012, `partial:false`; 0 warning di leader dalam 3 menit terakhir.

## Insiden disk pressure node-a — 21 Nov 2026 18:04–18:19

- Saat deploy `73b8c81`, disk root node-a mencapai 88% (16 GB dari 19 GB). Kubelet memasang taint `node.kubernetes.io/disk-pressure:NoSchedule` dan mengusir pod. `store-1` (PV lokal di node-a) `Pending` dengan `0/3 nodes are available: 1 node(s) had untolerated taint(s), 2 node(s) didn't match PersistentVolume's node affinity`. Puluhan pod grafana/prometheus/aggregator ikut `Evicted`.
- Selama store-1 hilang, reconcile memindahkan replikanya (misalnya p21/22/23 disalin ke store-0, ±4.400 baris per partisi, 18:05:59–18:06:04). Data tetap terbaca dari replika lain.
- Penyebab: sisa build Docker dari deploy berulang hari ini (build cache 3,9 GB, image 1,9 GB yang sudah di-push), ditambah volume `gomodcache` 380 MB dan rekaman stream 51 MB dari pengujian.
- Penanganan: `docker builder prune -af`, `docker image prune -af`, hapus volume `gomodcache` dan rekaman stream → 58%. Taint hilang 18:19:03, store-1 `Running` 18:19:18. GC image kubelet sempat menghapus image dari containerd, sehingga aggregator, dashboard-consumer, dan rabbitmq-2 di node-a `ImagePullBackOff` (unduhan blob dari CDN Docker Hub gagal sesaat). `make prepull NODES=node-a` (19 image) memulihkannya; pod `Failed`/`Evicted` dibersihkan. 18:23:38 storage 3 unit `serving`, 0 move, 0 under-replicated.
- Runbook: deploy berulang di node-a menumpuk build cache. Jalankan `docker builder prune -af` di node-a bila `df -h /` mendekati 80%, sebelum kubelet mulai mengusir pod (di node ini disk pressure terpicu pada ±88% terpakai), lalu `make prepull` bila ada `ImagePullBackOff`.
