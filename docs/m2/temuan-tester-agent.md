# Temuan Tester Agent

Bagian clock-skew dan partition diuji pada cluster kosong 2 node (12 Nov). Bagian kill-store dan wipe-node diuji pada cluster 3 node berisi data (15 Nov).

Cluster: `node-a` (control plane + worker, 192.168.18.201), `node-c` (worker, 192.168.18.203); `node-b` belum join. Tester Agent commit `ebb5526`, sha256 biner `86d511c7…aa418`. Diuji 12 Nov 2026 (UTC).

## clock-skew (practice)

- Node terpilih: `node-c` (`nodes=any`). Aksi: `shift_clock +300s selama 30s`.
- Timestamp log agent: langkah mulai `17:32:06`, langkah selesai `17:27:36`, skenario berikutnya mulai `17:32:41`. Artinya setelah "pemulihan" jam `node-c` sempat **mundur ±5 menit**, lalu kembali normal beberapa detik kemudian.
- Dugaan (belum diverifikasi): `systemd-timesyncd` sudah mengoreksi lompatan +300 s selama skenario, lalu pemulihan agent mengurangi 300 s lagi. Cek: `journalctl -u systemd-timesyncd` di `node-c`.
- Implikasi desain: lease/fencing harus tahan terhadap jam yang melompat **maju dan mundur**, bukan hanya jam yang terlambat.

## partition (practice)

- Gagal: `biner iptables tidak ditemukan di host (/host/sbin, /host/usr/sbin, dsb)`. Agent tetap melaporkan "pemulihan akhir selesai; keadaan bersih".
- Penyebab: di Ubuntu 24.04 `/usr/sbin/iptables`, `iptables-save`, `iptables-restore` adalah symlink absolut ke `/etc/alternatives/…`; dari pod (host di-mount di `/host`) symlink itu menunjuk ke `/etc/alternatives` milik container, yang tidak ada.
- Perbaikan (semua node): ganti dengan symlink relatif ke `xtables-nft-multi`, lihat `setup-cluster.md` langkah 2.
- Uji ulang setelah perbaikan: berhasil, `block_internode selama 30s` di `node-c` (17:35:37–17:36:07 UTC), keadaan bersih sesudahnya.
- Uji ketiga (blokir 17:39:03–17:39:33 UTC): `ping -D` node-a → node-c 17:38:54–17:39:50 **0% loss**; `curl -k https://192.168.18.201:6443/ping` dari node-c tetap `pong`. Dengan 2 node (1 worker), `partition` tidak memblokir lalu lintas host-ke-host yang terlihat.
- Uji keempat: `diff` `nft list ruleset` saat jendela vs sesudahnya di `node-a` dan `node-c` hanya berisi perubahan counter; **tidak ada aturan yang ditambahkan** di node mana pun. Dengan 2 node, `partition` praktis no-op meski log melaporkan sukses.
- Dugaan (belum diverifikasi): agent hanya menyekat antar-worker non-control-plane, sehingga butuh `node-b`. Uji ulang (`diff` + `ping -D` node-b ↔ node-c) setelah `node-b` join; bila tetap kosong, tanyakan ke asisten apa yang diblokir `block_internode` dan syarat topologinya.

## partition (practice, 15 Nov 2026, cluster 3 node berisi data)

- Langkah: `t+5s block_internode role= nodes=all` selama 30 s. Dijalankan dua kali. Agent `node-a` dan `node-c` mencatat jendela ±15:05:4x–15:06:16 dan 15:08:31–15:09:02 UTC, lalu "pemulihan akhir selesai; keadaan bersih". Agent `node-b` (`tester-agent-nvbwr`) tidak mencatat langkah apa pun.
- **Tidak ada efek yang teramati pada lalu lintas pod antar-node.** Store tersebar di tiga node, tetapi log Aggregator selama 30 menit tidak berisi satu pun `store unit unreachable`, `replica now lags`, atau `rows copied`. Checksum di jendela pertama `partial:false` dengan `unavailable_units:[]`. Hasil akhir tetap 2 × count (1201+1156+1169 = 2×1763).
- Dugaan (belum diverifikasi): `block_internode` hanya menyekat lalu lintas host-ke-host tertentu dan tidak menyentuh overlay pod (flannel VXLAN, UDP 8472). Pola ini sama dengan uji 2 node pada 12 Nov.
- Perlu ditanyakan ke asisten: apa saja yang diblokir `block_internode`, apakah lalu lintas pod antar-node seharusnya ikut terputus, dan mengapa agent `node-b` tidak ikut.
- Untuk laporan P1: ketahanan terhadap unit yang tidak terjangkau dibuktikan dengan poweroff `node-c` (lihat `bukti-p1-cluster.md`). Dari sisi Aggregator kondisinya setara dengan partisi: koneksi menggantung tanpa RST, timeout per unit bekerja, dan unit yang hilang tercatat di `unavailable_units`.

## schema (practice, 15 Nov 2026)

- Langkah: `t+5s call_mock_admin`. Gagal di 15:45:21 UTC: `tidak ada Service role=mock pada namespace sistem-informasi-kebencanaan-bnpb`, meskipun log tetap melaporkan "keadaan bersih".
- Penyebab: agent mencari **Service** berlabel `role=mock`, sedangkan kontrak §1.1 hanya mewajibkan label `role` pada pod. Label di pod saja tidak cukup.
- Percobaan pertama: memberi `role: mock` pada Service `bmkg` dan `pvmbg`. Hasilnya (15:52, 15:53) `mock POST /admin/schema-version -> HTTP 404`, karena agent memakai Service `role=mock` yang pertama, yaitu `bmkg`, dan hanya PVMBG yang punya route itu.
- Setelah label hanya di `pvmbg` (15:58:49): `mock POST /admin/schema-version -> HTTP 401`. Agent sekarang mencapai PVMBG, tetapi mock mewajibkan `Authorization: Bearer <PVMBG_TOKEN>` di endpoint admin (`services/pvmbg/cmd/server/main.go`, `requirePVMBGToken`), dan agent tidak punya token kita. Ini wilayah **P3 (B)**:
  - Endpoint admin perlu menerima panggilan tanpa token (hanya kait uji, tidak terekspos ke luar cluster).
  - `version: 2` saat ini hanya menambah `confidence_level` (kompatibel), padahal spesifikasi §256 meminta mode **tidak kompatibel**.
  - Payload yang dikirim agent belum diketahui (pertanyaan terbuka #4 di `context-m2.md`).
- NetworkPolicy `store-ingress` tidak terlibat: request agent sampai ke pod PVMBG.
- **Status 16 Nov 2026: butir pertama selesai** — `/admin/schema-version` dilepas dari `requirePVMBGToken` (mode incompatible juga sudah ditambah, lihat `services/pvmbg/README.md`). Butir ketiga (payload agent) masih pertanyaan terbuka #4.
- Perbaikan label: `role: mock` **hanya** pada Service `pvmbg`. Pod `bmkg` tetap berlabel `role=mock` sesuai kontrak. Service `sensor-pantai` diberi `role: sensor` untuk berjaga-jaga. Sesudah uji selalu jalankan `make reset` (schema PVMBG kembali ke versi 1).

## kill-store (practice, 15 Nov 2026, cluster 3 node berisi data)

- Satu langkah: `t+5s stop_pod role=store nodes=any`. Terpilih `store-2` di `node-c` (13:01:23 UTC).
- StatefulSet langsung membuat ulang pod dengan PVC yang sama (incarnation tetap). Checksum 58 s kemudian `partial:false`, count naik 292 → 348. Tidak ada re-replikasi karena unit kembali < 60 s.

## wipe-node (practice, 15 Nov 2026)

- Langkah: `wipe_and_shutdown node pemilik role=store nodes=any`. Terpilih `node-c` (store-2). Node langsung mati.
- **Loop mati-nyala:** setiap `node-c` dinyalakan, tester-agent di `node-c` memulai ulang skenario dari awal (`skenario "wipe-node" (practice) mulai`, lalu `wipe_and_shutdown` 5 s kemudian; terlihat di `/var/log/pods/tester-agent_*/agent/14.log`, `15.log`). Menunggu tidak membantu.
- Penyebab: run disimpan di `configmap/tester-run` (namespace `tester-agent`) dengan `"status":"go"`. Node mati sebelum agent sempat menandai run selesai, jadi status tidak pernah berubah dan agent membacanya lagi saat start.
- Cara memutus loop:
  1. Nyalakan `node-c` dengan kabel jaringan VM dicabut (VirtualBox: Network → Advanced → "Cable Connected" off). Tanpa API server, pod tester tidak jalan dan node tetap hidup.
  2. Dari `node-a`, backup lalu hapus run yang macet:
     ```bash
     kubectl -n tester-agent get configmap tester-run -o yaml > ~/tester-run-wipe-node-stuck.yaml
     kubectl -n tester-agent delete configmap tester-run
     ```
  3. Sambungkan kabel lagi, lalu nyalakan `node-c`.
- `snapshot.placements` di run yang macet mencatat Aggregator, auth, client-api, mock, dan broker juga di `node-c`, sehingga semuanya ikut mati. Aggregator baru pindah node setelah toleration 300 s, atau setelah `kubectl delete pod --force`.
- Perlu ditanyakan ke asisten: apakah loop ini disengaja, dan bagaimana cara resmi membatalkan run.

## Belum diuji

- `kill-leader`, `schema`: pesan log saat namespace masih kosong.
