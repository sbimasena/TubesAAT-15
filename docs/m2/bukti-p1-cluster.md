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
