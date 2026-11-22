# Setup Cluster k3s (D-TOPO)

Panduan merakit cluster demo 3 node sesuai `context-m2.md` §9.1. Cluster ini juga dipakai sebagai cluster dev Anggota C.

| Node | Host | VM | IP |
|---|---|---|---|
| `node-a` (control plane + worker) | PC Windows (AMD, 32 GB) | VirtualBox, 8 GB, 4 vCPU, disk 40 GB | `192.168.18.201` |
| `node-b` (worker) | MacBook Intel (16 GB) | VirtualBox, 6 GB, 2 vCPU, disk 40 GB | `192.168.18.202` |
| `node-c` (worker) | PC Windows (VM kedua) | VirtualBox, 8 GB, 4 vCPU, disk 40 GB | `192.168.18.203` |

Semua host harus x86_64, karena image Tester Agent dan Sensor Pantai hanya amd64. Urutan yang disarankan: `node-a` → `node-c` (satu host, paling mudah) → `node-b` (lintas host lewat Wi-Fi, paling berisiko).

Cadangan bila `node-b` di MacBook gagal join atau MacBook dibawa pergi: buat `node-b` sebagai VM ketiga di PC (turunkan ketiga VM ke 7 GB). Hanya bonus host fisik yang hilang.

---

## 0. Cari subnet dan pilih IP (sekali saja) (PC SAKTI)

Di PC Windows, jalankan `ipconfig` dan lihat adapter Wi-Fi:
- `IPv4 Address`, mis. `192.168.18.5` → subnet `192.168.18.0/24`
- `Default Gateway`, mis. `192.168.18.1`

Pilih tiga IP di ujung atas subnet (mis. `.201`–`.203`), karena DHCP router rumahan biasanya membagi dari bawah. Pastikan belum terpakai: `ping 192.168.18.201` harus **gagal**. Lebih aman lagi bila IP itu dikeluarkan dari rentang DHCP di halaman admin router.

Hasil di jaringan kita: subnet `192.168.18.0/24`, gateway `192.168.18.1`, IP `.201`–`.203` sudah dicek kosong (12 Nov 2026). Semua perintah di bawah memakai IP ini; bila jaringan berganti, ganti seluruh `192.168.18.x` sesuai hasil `ipconfig`.

---

## 1. Buat VM (sama untuk tiap VM)

1. Unduh **Ubuntu Server 24.04 LTS amd64** (`ubuntu-24.04.x-live-server-amd64.iso`) dan **VirtualBox 7.1** (sudah terpasang di MacBook).
   - macOS: bila VirtualBox baru dipasang, setujui extension di *System Settings → Privacy & Security*, lalu reboot.
   - Windows: **wajib matikan hypervisor Windows** sebelum menjalankan VM. Bila Hyper-V/WSL2 aktif (ikon kura-kura di status bar VM), VM terlalu lambat untuk k3s dan muncul `watchdog: BUG: soft lockup`. Jalankan `bcdedit /set hypervisorlaunchtype off` (PowerShell Admin), hilangkan centang "Virtual Machine Platform" dan "Windows Hypervisor Platform" di *Windows Features*, matikan *Windows Security → Core isolation → Memory integrity*, lalu restart. Cek: `(Get-CimInstance Win32_ComputerSystem).HypervisorPresent` harus `False`. Bila masih `True`, VBS/Windows Hello Enhanced Sign-in Security masih aktif; matikan (PowerShell Admin) lalu restart:
     ```powershell
     reg add "HKLM\SYSTEM\CurrentControlSet\Control\DeviceGuard" /v EnableVirtualizationBasedSecurity /t REG_DWORD /d 0 /f
     reg add "HKLM\SYSTEM\CurrentControlSet\Control\DeviceGuard\Scenarios\WindowsHello" /v Enabled /t REG_DWORD /d 0 /f
     ```
     Efeknya WSL2/Docker Desktop tidak jalan dan login wajah/sidik jari mungkin perlu di-setup ulang; kembalikan dengan `bcdedit /set hypervisorlaunchtype auto` dan kedua nilai registry di atas ke `1`.
2. VirtualBox → **New**:
   - Name: `node-a` / `node-b` / `node-c`. ISO: file Ubuntu tadi. Centang **Skip Unattended Installation**.
   - Memory dan CPU sesuai tabel. Disk 40 GB (dynamically allocated).
3. Sebelum dinyalakan: **Settings → Network → Adapter 1 → Attached to: Bridged Adapter**, Name = adapter yang dipakai host ke router (Wi-Fi atau Ethernet; di PC: `RZ616 Wi-Fi 6E 160MHz`, di Mac biasanya `en0: Wi-Fi`). **Jangan** pilih `VirtualBox Host-Only Ethernet Adapter`.
4. Start VM dan ikuti installer Ubuntu:
   - **Network:** pilih interface (`enp0s3`) → *Edit IPv4* → **Manual**:
     - Subnet `192.168.18.0/24`, Address `192.168.18.201` (sesuai node), Gateway `192.168.18.1`, Name servers `1.1.1.1,8.8.8.8`
   - **Ubuntu archive mirror:** bila cek mirror gagal ("no longer has a Release file"), pastikan dulu jaringan di atas benar, lalu ganti mirror ke `http://archive.ubuntu.com/ubuntu`.
   - **Storage:** default (use entire disk).
   - **Profile:** server name **`node-a`** (persis; nama ini muncul di `kubectl get nodes` dan Audit API `location`), username bebas, mis. `bnpb`.
   - **SSH:** centang **Install OpenSSH server**, biarkan password authentication aktif; import SSH key tidak perlu.
   - Featured snaps: jangan pilih apa pun.
5. Setelah reboot, cek dari host (Mac/Windows): `ssh bnpb@192.168.18.201`. Bila gagal, lihat bagian Troubleshooting.

## 2. Persiapan OS (di tiap VM, lewat SSH)

```sh
sudo apt update && sudo apt -y upgrade
sudo swapoff -a && sudo sed -i '/swap/d' /etc/fstab   # kubelet tidak suka swap
hostnamectl                                           # pastikan Static hostname = node-x
timedatectl                                           # NTP service: active
# Tester Agent mencari iptables lewat /host; symlink absolut ke /etc/alternatives tidak bisa diikuti dari pod
cd /usr/sbin && for f in iptables iptables-save iptables-restore; do sudo ln -sf xtables-nft-multi $f; done; cd -
readlink -f /usr/sbin/iptables                        # /usr/sbin/xtables-nft-multi
```

Ulangi baris `ln -sf` di atas bila `apt upgrade` memperbarui paket `iptables`, karena upgrade bisa mengembalikan symlink absolut.

Matikan sleep di host:
- **Windows:** *Settings → System → Power* → Sleep = Never (saat dicolok).
- **Mac:** VM berhenti bila lid ditutup. Selama bekerja jalankan `caffeinate -dimsu` di terminal, atau biarkan lid terbuka.

## 3. k3s server di `node-a`

```sh
curl -sfL https://get.k3s.io | INSTALL_K3S_CHANNEL=stable sh -s - server \
  --node-ip 192.168.18.201 \
  --tls-san 192.168.18.201 \
  --write-kubeconfig-mode 644

k3s --version                                   # catat versinya, mis. v1.33.5+k3s1
sudo cat /var/lib/rancher/k3s/server/node-token # token join, JANGAN di-commit
kubectl get nodes -o wide                       # node-a Ready
```

Jangan pakai `--disable-network-policy` (NetworkPolicy wajib ditegakkan, `context-m2.md` §6.4). Traefik dan ServiceLB bawaan dibiarkan aktif.

## 4. k3s agent di `node-b` dan `node-c`

Pakai **versi yang sama persis** dengan `node-a`:

```sh
curl -sfL https://get.k3s.io | \
  INSTALL_K3S_VERSION='v1.33.5+k3s1' \
  K3S_URL='https://192.168.18.201:6443' \
  K3S_TOKEN='<token dari node-a>' \
  sh -s - agent --node-ip 192.168.18.202     # node-c: .203
```

Cek dari `node-a`: `kubectl get nodes -o wide` → semua node `Ready`, `INTERNAL-IP` sesuai tabel.

## 5. kubeconfig di laptop

Di Mac (atau laptop anggota lain):

```sh
mkdir -p ~/.kube
scp bnpb@192.168.18.201:/etc/rancher/k3s/k3s.yaml ~/.kube/bnpb.yaml
sed -i '' 's/127.0.0.1/192.168.18.201/' ~/.kube/bnpb.yaml   # Linux: sed -i tanpa ''
sed -i '' 's/: default$/: bnpb/' ~/.kube/bnpb.yaml          # nama context/cluster/user = bnpb
export KUBECONFIG=~/.kube/bnpb.yaml                         # tambahkan ke ~/.zshrc bila mau
kubectl get nodes -o wide
```

`~/.kube/bnpb.yaml` berisi kredensial admin cluster: jangan di-commit dan jangan dibagikan di chat publik.

## 6. Uji jaringan lintas node

Bagian ini menguji hal yang paling sering gagal (VXLAN flannel lewat Wi-Fi bridged dan DNS cluster):

```sh
kubectl run net-a --image=busybox:1.36 --restart=Never --overrides='{"spec":{"nodeName":"node-a"}}' -- sleep 3600
kubectl run net-b --image=busybox:1.36 --restart=Never --overrides='{"spec":{"nodeName":"node-b"}}' -- sleep 3600
kubectl wait --for=condition=Ready pod/net-a pod/net-b --timeout=120s
kubectl get pods -o wide                                   # catat IP pod net-b
kubectl exec net-a -- ping -c 3 <IP pod net-b>            # harus 0% packet loss
kubectl exec net-b -- nslookup kubernetes.default.svc.cluster.local  # harus 10.43.0.1 (busybox tidak memakai search domain, jadi nama pendek NXDOMAIN)
kubectl delete pod net-a net-b
```

## 7. Pasang Tester Agent

Namespace aplikasi harus ada lebih dulu, karena manifest tester membuat Role di dalamnya.

```sh
kubectl create namespace sistem-informasi-kebencanaan-bnpb
kubectl apply -f https://raw.githubusercontent.com/susTuna/if4031-tester-agent/ebb55261711123a9a211739ffbf140a275caa6da/manifest.yaml
kubectl rollout status ds/tester-agent -n tester-agent
kubectl get pods -n tester-agent -o wide                    # 1 pod Running per node, termasuk node-a
kubectl exec -n tester-agent daemonset/tester-agent -- /app list
```

## 8. Coba skenario latihan di cluster kosong

Buka dua terminal. Terminal 1 mengikuti log semua agen:

```sh
kubectl logs -n tester-agent -l app=tester-agent -f --prefix --max-log-requests 10
```

Terminal 2 menjalankan satu skenario, tunggu selesai, lalu lanjut ke skenario berikutnya:

```sh
kubectl exec -n tester-agent daemonset/tester-agent -- /app run practice clock-skew
kubectl exec -n tester-agent daemonset/tester-agent -- /app run practice partition
```

Selama `clock-skew`, jalankan `date` di tiap VM. Selama `partition`, coba `ping` antar-VM. Catat temuan di `docs/m2/temuan-tester-agent.md`:
- node mana yang dipilih, berapa lama, apa yang persis berubah (jam +45 s/−30 s? iptables ke IP mana?)
- apakah semuanya pulih sendiri sesudahnya (`kubectl get nodes`, `date`)
- pesan log untuk skenario yang butuh target (`kill-store`, `kill-leader`, `wipe-node`) saat namespace masih kosong

`wipe-node` **jangan** dicoba dulu: tunggu sampai ada data yang mau dibuktikan bertahan.

## 9. Akses remote untuk anggota lain (Tailscale di `node-a`)

Supaya A/B bisa `kubectl` dari rumah tanpa ikut menjadi node:

```sh
# di node-a
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up            # login, lalu undang akun A/B ke tailnet (atau share node-a)
tailscale ip -4              # mis. 100.101.102.103

# ulangi perintah install langkah 3 dengan SAN tambahan; installer memperbarui unit systemd tanpa menghapus data
curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION='v1.33.5+k3s1' sh -s - server \
  --node-ip 192.168.18.201 \
  --tls-san 192.168.18.201 \
  --tls-san 100.101.102.103 \
  --write-kubeconfig-mode 644
```

Kirim `k3s.yaml` ke A/B lewat kanal privat, dengan `server:` diganti `https://100.101.102.103:6443`. Mereka memasang Tailscale di laptopnya dan login ke tailnet yang sama. Jaringan antarnode tetap lewat LAN, bukan Tailscale.

## 10. Catat ke repo

Setelah berhasil, isi tabel deklarasi topologi di README (jumlah node, VM, host fisik, CPU/RAM host), versi k3s, subnet node (dipakai `ipBlock` NetworkPolicy untuk Tester Agent), dan CIDR pod (`10.42.0.0/16` bawaan k3s).

---

## Troubleshooting

| Gejala | Penyebab umum | Coba |
|---|---|---|
| VM tidak bisa di-ping dari host | Adapter bridged salah pilih, atau IP bentrok | Cek *Settings → Network* menunjuk adapter yang benar; `ip a` di VM; ganti IP |
| VM bisa ping gateway tapi tidak bisa ping VM di host lain | Router mengisolasi klien Wi-Fi (*AP/client isolation*) | Matikan isolation di admin router, atau pakai kabel LAN |
| Agent tidak join (`journalctl -u k3s-agent -f`: timeout ke `:6443`) | Jaringan antar-VM, token salah | `curl -k https://192.168.18.201:6443` dari VM agent harus membalas 401 |
| Pod beda node tidak bisa saling ping (langkah 6) | UDP 8472 (VXLAN) terblokir | Cek router/firewall; cadangan: `node-b` dipindah ke PC (`context-m2.md` §9.1) |
| Pod tester `ImagePullBackOff`/`exec format error` | Node bukan x86_64 | `uname -m` di VM harus `x86_64` |
| Node `NotReady` setelah host sleep | VM ikut berhenti | Nyalakan VM; `sudo systemctl restart k3s-agent` |
| IP VM berubah setelah reboot | Netplan masih DHCP | Edit `/etc/netplan/*.yaml` ke statis, `sudo netplan apply` |
