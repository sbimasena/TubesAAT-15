# Tambahan laporan Member A

Panduan ini dibuat dari snapshot laporan lokal sebelum penyuntingan Google Docs. Petunjuk dan koreksi historis di bawah perlu dicocokkan dengan [audit versi Google Docs 10 Oktober 2026](google-docs-audit.md); sebagian koreksi, termasuk angka waktu tunggu expiry dan definisi freshness, sudah diterapkan pada versi tersebut.

Panduan ini mengikuti judul, tabel, dan kalimat pada `IF4031_M1_FithraGaMasukKelas.md`, serta nomor halaman pada PDF yang telah dibaca. Bagian “Lokasi dan tindakan” merupakan petunjuk penyuntingan, sedangkan paragraf dan tabel setelahnya merupakan isi siap tempel. Screenshot P1 menggunakan nomor Gambar 1, 2, dan 3 agar rujukan Gambar 4 sampai 27 yang sudah ada tetap dapat dipakai. Nomor halaman mengacu pada versi PDF awal dan dapat berubah setelah penyuntingan.

Bukti P1 berasal dari pengujian baru pada 9 Oktober 2026, pukul 19.41.19 sampai 19.41.39 WIB, pada project Docker Compose `tubesaat-p1-report`. Pengujian menggunakan polling 3 detik, generator kedua mock 10 detik, dan delay PVMBG 750 ms. Bukti ini tidak menggantikan hasil load test P2 dalam laporan.

## 1. Bab 1: kontribusi anggota (halaman 2)

Lokasi dan tindakan: ganti seluruh isian baris Muhammad Fithra Rizki, bukan baris anggota lain.

| Anggota dan NIM | Perancangan | Implementasi | Penulisan laporan |
|---|---|---|---|
| Muhammad Fithra Rizki, 13523049 | Kontrak sumber BMKG/PVMBG; model kanonis HazardEvent; aturan pemetaan dan korelasi tsunami; polling mandiri, cursor, serta kebijakan perubahan skema dan freshness sumber. | BMKG Mock dan PVMBG Mock; adapter HTTP upstream; poller dan pengelolaan cursor/cache Aggregator; pemetaan data kanonis; API internal dan status ingestion; simulasi perubahan skema/outage serta pemeriksaan pemetaan dan evolusi skema. | Uraian mock dan Aggregator pada arsitektur/teknologi; keputusan pemetaan dan polling; pembahasan P1; bagian upstream dan freshness pada P2; autentikasi upstream pada P3; keterlacakan kode dan lampiran bukti P1. |

## 2. Bab 3: arsitektur sistem (halaman 3)

Lokasi dan tindakan: sisipkan kedua paragraf berikut setelah kalimat “Data seismik, peringatan tsunami terkait, dan laporan vulkanik dinormalisasi oleh Aggregator menjadi bentuk kanonis HazardEvent.”

Di dalam Aggregator, pengambilan data BMKG dan PVMBG dijalankan oleh dua poller yang memiliki siklus masing-masing. Poller BMKG mengambil kejadian seismik dan peringatan tsunami, sedangkan poller PVMBG mengambil laporan aktivitas vulkanik. Setiap poller melakukan pengambilan awal ketika layanan dimulai, kemudian mengulangi polling berdasarkan ticker. Satu poller menyelesaikan siklusnya sebelum memulai siklus berikutnya sehingga tidak membentuk worker baru pada setiap tick. Panggilan jaringan dilakukan di luar lock pengelolaan state agar respons PVMBG yang lambat tidak menahan akses state poller BMKG.

Data sumber melewati adapter HTTP, korelasi dan pemetaan, lalu penyimpanan melalui repository milik Aggregator. Jalur query HTTP membaca data kanonis yang telah tersimpan dan tidak melakukan polling upstream sebagai bagian dari permintaan klien. Pemisahan ini memungkinkan query seismik tetap dilayani ketika pengambilan data PVMBG belum selesai. Metadata freshness menyertai respons agar klien dapat menilai apakah pipeline sumber masih berhasil memperbarui data.

Lokasi dan tindakan untuk diagram: tempatkan diagram alur internal ini setelah dua paragraf tersebut atau gunakan sebagai diagram pendamping arsitektur utama.

```text
                      Batas layanan Aggregator
       +-----------------------------------------------------+
BMKG --|--> Adapter BMKG --> Poller BMKG                      |
 HTTP  |                        |                            |
       |                 Cache event/warning                 |
       |                        | korelasi event_id           |
       |                        v                            |
       |                   MapSeismic -------+               |
       |                                     |               |
PVMBG -|--> Adapter PVMBG --> Poller PVMBG    |               |
 HTTP  |                        |            v               |
       |                   MapVolcanic --> Repository -------|--> PostgreSQL
       |                                     ^               |    hazard + outbox
       |                                     |               |
API ---|--> GET /internal/v1/hazards ---------+               |
Klien  |          baca data tersimpan                         |
 HTTP  |                                                     |
       |    Worker outbox -----------------------------------|--> RabbitMQ
       +-----------------------------------------------------+

Poller BMKG dan PVMBG berjalan mandiri.
Query klien dan worker outbox berjalan terpisah dari polling.
```

Versi gambar siap pakai dari diagram ini: [PNG alur internal Aggregator](../diagrams/bab-3-alur-internal-aggregator.png) dan [SVG yang dapat diedit](../diagrams/bab-3-alur-internal-aggregator.svg). Diagram utama Bab 3 juga dapat diganti dengan [PNG arsitektur sistem](../diagrams/bab-3-arsitektur-sistem.png) atau [SVG arsitektur sistem](../diagrams/bab-3-arsitektur-sistem.svg). Caption serta petunjuk penempatan tersedia pada [README diagram](../diagrams/README.md).

## 3. Bab 3.1: inventaris service dan kontrak jaringan (halaman 4)

Lokasi dan tindakan: ganti kolom “Keputusan implementasi” pada baris BMKG Mock dan PVMBG Mock. Kolom kontrak yang sudah ada dapat dipertahankan.

| Komponen | Keputusan implementasi |
|---|---|
| BMKG Mock | Go 1.27.1 dengan router standar `net/http`. Katalog dalam memori dilindungi `sync.RWMutex`, memuat 20 kejadian historis pada startup, dan diperbarui oleh generator periodik. Endpoint data memiliki delay acak 50 sampai 150 ms serta memvalidasi `X-BMKG-Key`. Image dibangun secara multistage dari `golang:1.27.1` dengan runtime `scratch`. |
| PVMBG Mock | Go 1.27.1 dengan `net/http`. Katalog awal berisi 20 laporan vulkanik. `PVMBG_DELAY_MS` menetapkan delay respons pada rentang 500 sampai 3.000 ms. Header Bearer memakai kredensial PVMBG tersendiri. Endpoint admin mengubah versi skema dan flag outage saat runtime; perubahan ke versi 2 langsung menambahkan laporan baru dengan `confidence_level`. Build multistage menggunakan `golang:1.27.1` dan runtime `scratch`. |

## 4. Bab 4: teknologi yang digunakan (halaman 5)

Lokasi dan tindakan: ganti seluruh baris “Mock dan Aggregator” yang masih berisi `[Isi]`.

| Komponen | Teknologi dan versi | Alasan terkait kebutuhan | Alternatif yang ditimbang |
|---|---|---|---|
| Mock dan Aggregator | Go 1.27.1; HTTP/JSON melalui `net/http` dan `encoding/json`; goroutine, ticker, `context.Context`, dan `sync.RWMutex`; log JSON melalui `log/slog`. | Pustaka standar mencukupi untuk endpoint mock, polling periodik, timeout, dan penghentian worker. Struct bertipe menyediakan pemetaan field inti, sedangkan decoder khusus PVMBG mempertahankan properti tambahan untuk `attributes`. Log dan correlation ID menghubungkan panggilan sumber dengan hasil ingestion. | Framework seperti Gin atau Echo mempermudah middleware pada API yang lebih besar, tetapi endpoint M1 masih terbatas. Decoder yang hanya menggunakan struct lebih ringkas, tetapi mengabaikan properti tambahan yang tidak dideklarasikan. Decoder PVMBG menggabungkan struct field inti dengan penampungan field tambahan. |

## 5. Bab 5: asumsi dan keputusan lintas service (halaman 8-9)

Lokasi dan tindakan: ganti baris “Interval polling, cursor dan deduplikasi”, isi baris pemetaan `volcano_id`, lalu tambahkan baris korelasi tsunami dan kebijakan field tambahan setelahnya.

| Topik | Keputusan / asumsi kelompok | Alasan dan dampak |
|---|---|---|
| Interval polling, cursor dan deduplikasi | Polling default 3 detik dengan cursor terpisah untuk kejadian seismik, warning tsunami, dan laporan vulkanik. Permintaan `since` memakai overlap satu menit. Cursor maju setelah transaksi persistence berhasil. Identitas hazard bersifat deterministik berdasarkan sumber dan ID rekaman sumber. | Overlap membaca ulang jendela waktu terakhir. Replay identik tidak membuat hazard atau pesan baru. Kegagalan penyimpanan mempertahankan cursor untuk retry. Cursor/cache berada di memori; pemulihan setelah restart bergantung pada riwayat yang masih disediakan mock. |
| Pemetaan `volcano_id` ke nama/koordinat | Aggregator memiliki tabel referensi statis enam gunung api: Merapi, Semeru, Anak Krakatau, Sinabung, Agung, dan Marapi. | PVMBG cukup mengirim identitas gunung api; nama dan koordinat kanonis ditentukan oleh BNPB. ID yang belum terdaftar gagal dipetakan dan mempertahankan cursor PVMBG agar rekaman dapat dicoba kembali. |
| Korelasi warning tsunami | `TsunamiWarning.event_id` dicocokkan dengan `SeismicEvent.event_id`. Cache menyimpan kejadian dan warning terakhir berdasarkan ID kejadian. Warning dengan `issued_at` lebih baru digunakan untuk pemetaan ulang. | Warning yang datang setelah kejadian dapat memperbarui hazard yang sama. Warning tanpa pasangan kejadian menahan cursor warning dan membuat sumber stale sampai korelasi serta penyimpanan berhasil. |
| Field tambahan PVMBG | Field inti dipetakan secara eksplisit; properti tambahan yang tidak dikenal ditampung oleh `VolcanicReport.UnmarshalJSON` dan diteruskan ke `attributes`. `confidence_level` memakai field opsional bertipe numerik. | Penambahan properti dapat disimpan tanpa menambah kolom kanonis. Record lama tidak diberi nilai confidence buatan. Perubahan nama, tipe, atau makna field inti tetap membutuhkan penyesuaian adapter. |

Lokasi dan tindakan: tambahkan tabel referensi berikut tepat setelah tabel keputusan lintas service.

| `volcano_id` | Nama gunung api | Latitude | Longitude |
|---|---|---:|---:|
| V-001 | Merapi | -7.5407 | 110.4457 |
| V-002 | Semeru | -8.1080 | 112.9220 |
| V-003 | Anak Krakatau | -6.1020 | 105.4230 |
| V-004 | Sinabung | 3.1700 | 98.3920 |
| V-005 | Agung | -8.3420 | 115.5080 |
| V-006 | Marapi | -0.3810 | 100.4730 |

## 6. Bab 3.1 dan Bab 6: konfigurasi generator serta pengujian P1

Lokasi dan tindakan pertama: pada akhir Bab 3.1 (halaman 5), ganti paragraf yang dimulai dengan “Setiap mock menyediakan sedikitnya 20 record historis ...”.

Setiap mock menyediakan 20 record historis pada startup. Interval pembentukan record baru dapat diatur melalui `BMKG_GENERATE_INTERVAL_SECONDS` dan `PVMBG_GENERATE_INTERVAL_SECONDS`. Nilai default kode dan Compose adalah 15 detik. Pada pengujian P1 dalam laporan ini, kedua interval diatur menjadi 10 detik untuk memenuhi ketentuan sekurang-kurangnya satu event baru per 10 detik per instansi. Interval generator berbeda dari interval polling Aggregator yang ditetapkan menjadi 3 detik.

Lokasi dan tindakan kedua: pada Bab 6 (halaman 11), isi bagian “Problem 1” sebelum judul “Problem 2”.

Pengujian P1 membandingkan payload sumber dengan `HazardEvent`, mengubah skema PVMBG dari versi 1 ke versi 2, dan memeriksa apakah `confidence_level` tersimpan dengan nilai yang sama. Checker juga membandingkan ID serta waktu startup container dan catatan `schema_migrations` sebelum dan sesudah pengujian. Kedua consumer diperiksa untuk memastikan snapshot hasil pemetaan diteruskan melalui jalur distribusi event.

Gunakan file konfigurasi lokal yang telah diisi sesuai README, misalnya `.env.p1.local`, dengan generator kedua mock diatur menjadi 10 detik. Overlay operator diperlukan untuk membaca API Aggregator dari host. Nama project memisahkan container dan volume; pilih pula port host yang bebas agar tidak berbenturan dengan demo lain. Output run baru ditempatkan di direktori terpisah agar bukti yang dirujuk laporan tetap tersedia.

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

`check-p1-report.py` menjalankan `check-ingestion.py` untuk perubahan skema, fanout, outage, dan recovery, kemudian menambah pemeriksaan pemetaan BMKG/PVMBG serta catatan migrasi. Pengujian menambah record dan hasil jurnal, lalu mengembalikan PVMBG ke skema versi 1 serta `outage=false`. Skrip tidak menghapus data atau me-restart layanan. Freeze mengambil gambar keluaran terminal dari `show-p1-evidence.py`, yang membaca hasil JSON tersimpan. Perintah gambar ketiga bagian serta log checker asli tersedia pada [README bukti](../evidence/member-a/p1/README.md).

## 7. Bab 7.1: rancangan solusi dan mekanisme P1 (halaman 12)

Lokasi dan tindakan: ganti paragraf petunjuk `[Jelaskan parser dan validasi ...]` dengan empat paragraf berikut. Sebutan `related_event_id` pada petunjuk diganti menjadi `event_id` sesuai kontrak kode.

Aggregator menggunakan adapter HTTP terpisah untuk BMKG dan PVMBG. Respons JSON BMKG didekode menjadi `SeismicEvent` dan `TsunamiWarning`, sedangkan laporan PVMBG menjadi `VolcanicReport`. Field inti dipetakan ke sebelas field kanonis `HazardEvent`; field khusus sumber ditempatkan pada `attributes`. Timestamp kejadian berasal dari sumber, sementara `ingested_at` diisi ketika pemetaan dilakukan dan digunakan pada snapshot yang disimpan. `hazard_id` dibentuk dari 16 byte pertama hash SHA-256 atas gabungan nama sumber, tanda titik dua, dan ID referensi sumber, kemudian diberi awalan `haz_`. Rekaman sumber yang sama menghasilkan identitas hazard yang sama ketika polling diulang.

Pemetaan seismik mengubah nama wilayah dan koordinat episentrum menjadi `area_name`, `latitude`, dan `longitude`. Magnitudo, kedalaman, serta indikator potensi tsunami dipertahankan pada `attributes`. Aggregator mengorelasikan warning melalui kesamaan `event_id`. Jika warning tersedia, tingkat ancamannya menentukan severity dan rinciannya disimpan pada `attributes.tsunami_warning`. Tanpa warning, magnitudo di bawah 5,0 menghasilkan `NORMAL`, magnitudo 5,0 sampai kurang dari 6,5 menghasilkan `WASPADA`, dan magnitudo sekurang-kurangnya 6,5 menghasilkan `SIAGA`. Warning terlambat dapat memicu pemetaan ulang kejadian yang telah tersimpan tanpa membuat identitas hazard baru.

Laporan vulkanik memakai `report_id` sebagai referensi sumber dan `reported_at` sebagai waktu kejadian. Nama serta koordinat gunung api diperoleh dari tabel referensi statis Aggregator berdasarkan `volcano_id`. `alert_level` dinormalisasi ke enum severity, sementara jumlah erupsi dan tinggi kolom abu disimpan pada `attributes`. Parser khusus `VolcanicReport.UnmarshalJSON` mendekode field inti sekaligus mengumpulkan properti yang tidak dikenal dalam map `Extra`. Saat pemetaan, properti tersebut dikonversi menjadi nilai JSON dan dimasukkan ke `attributes`.

`confidence_level` dideklarasikan sebagai pointer numerik agar ketiadaan nilai dapat dibedakan dari angka nol. Pada skema versi 1, properti ini tidak disertakan dalam hasil pemetaan. Permintaan `POST /admin/schema-version` dengan body `{"version":2}` mengubah perilaku generator PVMBG dan langsung menambahkan satu laporan versi 2. Aggregator yang sedang berjalan membaca field tersebut pada polling berikutnya dan menyimpannya sebagai `attributes.confidence_level`. Record versi 1 mempertahankan bentuk awalnya sehingga kedua versi dapat dibaca bersamaan.

## 8. Bab 7.1: alasan pilihan dan alternatif (halaman 13)

Lokasi dan tindakan: ganti seluruh petunjuk pada subbagian “Alasan pilihan dan alternatif”.

Pemetaan eksplisit mempertahankan arti field kanonis, sedangkan map atribut memberi ruang bagi informasi tambahan milik sumber. BNPB tetap menentukan identitas, jenis bencana, severity, nama wilayah, dan koordinat yang dipakai downstream. Penambahan properti sumber dapat diterima selama tidak mengubah kontrak field inti. Pendekatan ini mendukung perubahan aditif PVMBG dengan perubahan operasional yang terbatas pada sumber tersebut.

Decoder yang hanya menggunakan struct akan mengabaikan field JSON yang belum dideklarasikan. Sebaliknya, pemetaan seluruh payload secara dinamis memerlukan pemeriksaan tipe dan aturan domain secara manual. Implementasi PVMBG menggabungkan struct untuk field yang telah dikenal dengan map untuk properti tambahan. Schema registry belum digunakan karena M1 hanya mengintegrasikan dua kontrak sumber; biaya pengelolaan versi dan layanan tambahan belum diperlukan untuk demonstrasi perubahan aditif ini.

| Pendekatan | Konsekuensi | Keputusan |
|---|---|---|
| Struct tanpa penampungan field tambahan | Pemetaan inti ringkas, tetapi properti yang tidak dideklarasikan diabaikan oleh decoder. | Digunakan pada kontrak BMKG; perlu perubahan kode jika field tambahan BMKG harus dipertahankan. |
| Struct field inti dan map tambahan | Field yang dikenal tetap bertipe; properti tambahan dapat disimpan pada atribut dinamis. | Dipilih untuk PVMBG. |
| Seluruh payload sebagai map dinamis | Fleksibel, tetapi validasi tipe serta pemetaan domain harus diperiksa manual. | Tidak dipilih sebagai representasi seluruh kontrak. |
| Schema registry | Menyediakan pengelolaan versi kontrak, tetapi membutuhkan komponen dan prosedur tambahan. | Belum digunakan pada M1. |

## 9. Bab 7.1: batasan yang disadari (halaman 13)

Lokasi dan tindakan: ganti seluruh petunjuk pada subbagian “Batasan yang disadari”.

Dukungan evolusi skema terbatas pada penambahan properti JSON yang kompatibel dengan field inti. Perubahan tipe field yang sudah bertipe, seperti mengubah `confidence_level` dari angka menjadi string, dapat membuat decoding respons gagal. Penggantian nama atau penghapusan identitas sumber dan timestamp tidak didukung sebagai perubahan kompatibel. `volcano_id` yang belum terdaftar ditolak oleh pemetaan. Jika pemetaan PVMBG gagal, record yang valid tetap dapat disimpan, tetapi cursor tidak dimajukan agar record bermasalah tetap berada dalam jendela retry.

Validasi belum membentuk schema validator yang memeriksa keberadaan seluruh field. Field numerik yang tidak dikirim dapat menjadi nilai nol dari struct Go. `alert_level` yang tidak dikenali menggunakan fallback `NORMAL`; threat level warning yang tidak dikenali tidak menggantikan severity berbasis magnitudo. Repository menolak timestamp kosong dan constraint database membatasi identitas, enum kanonis, serta koordinat. Perilaku ini memadai untuk kontrak mock yang dikendalikan pada M1, tetapi belum menjamin bahwa seluruh perubahan semantik upstream akan terdeteksi.

Penampungan otomatis field tidak dikenal berlaku pada laporan PVMBG. Kontrak BMKG memakai struct biasa sehingga field tambahan yang tidak dideklarasikan diabaikan. Cursor dan cache korelasi berada di memori; restart Aggregator membangun ulang state melalui polling riwayat yang masih tersedia. Karena katalog mock membatasi retensi pada 1.000 record, pemulihan ini tidak menjamin pengambilan kembali seluruh riwayat setelah downtime yang sangat panjang.

## 10. Bab 7.1: bukti penyelesaian (halaman 13)

Lokasi dan tindakan: tambahkan paragraf berikut setelah paragraf klasifikasi Ringkasan/Mentah, lalu ganti tabel “Bukti penyelesaian” yang masih berisi `[Isi]`.

Pengujian pada 9 Oktober 2026 menggunakan project `tubesaat-p1-report` dan menghasilkan status `PASS`. Kejadian BMKG `BMKG-20261009-005` memiliki magnitudo 6,0 dan warning `SIAGA`; hasil kanonis menggunakan severity `SIAGA`, mengikuti warning terkait. Laporan PVMBG versi 2 menyimpan `confidence_level` sebesar `0.7036322458518115`, sama dengan nilai pada payload sumber. Record versi 1 tetap tidak memiliki properti tersebut. ID dan waktu startup sembilan container tidak berubah selama pemeriksaan; migrasi tetap pada versi 1 dengan `applied_at` yang sama. Snapshot versi 2 diterima kedua consumer sebagai message ID 55 dengan hazard ID dan correlation ID yang sama.

| Kriteria yang dibuktikan | Bukti dan lokasi dalam laporan / repositori |
|---|---|
| Pemetaan awal kedua sumber | Gambar 1 memperlihatkan pasangan SeismicEvent/TsunamiWarning dan HazardEvent. Gambar 2 memperlihatkan pemetaan VolcanicReport v1/v2, termasuk nama dan koordinat dari referensi BNPB. Kode: `services/aggregator/internal/domain/hazard.go`, fungsi `MapSeismic` dan `MapVolcanic`; hasil: `docs/evidence/member-a/p1/report-check.json`. |
| Perubahan skema saat runtime | `POST /admin/schema-version` dengan versi 2 menghasilkan laporan baru. Gambar 3 memperlihatkan ID dan StartedAt seluruh container tetap serta migrasi versi 1 dengan applied_at yang sama. Checker: `scripts/check-ingestion.py` dan `scripts/check-p1-report.py`. |
| Event sesudah perubahan | Gambar 2 memperlihatkan confidence sumber dan kanonis sama, sedangkan record v1 tetap tanpa confidence. Gambar 3 menghubungkan fetch, commit, publish, dan penerimaan kedua consumer melalui correlation ID. Kode: `VolcanicReport.UnmarshalJSON`, `MapVolcanic`, dan `Manager.pollPVMBG`. |

## 11. Bab 7.2: rancangan solusi dan mekanisme P2 (halaman 14)

Lokasi dan tindakan pertama: ganti paragraf pembuka yang menyebut PVMBG memiliki delay acak dan BMKG memiliki delay tetap.

BMKG Mock memberi delay acak 50 sampai 150 ms pada endpoint data. Delay PVMBG ditentukan melalui konfigurasi `PVMBG_DELAY_MS` pada rentang 500 sampai 3.000 ms; nilainya tetap untuk konfigurasi proses yang sedang berjalan. Pada skenario P2, PVMBG diatur menjadi 3.000 ms untuk menguji apakah permintaan seismik tetap cepat ketika sumber vulkanik lambat. Simulasi outage PVMBG mengembalikan HTTP 503 dari endpoint data, sementara endpoint health dan admin tetap tersedia.

Lokasi dan tindakan kedua: tambahkan dua paragraf berikut tepat setelah judul “Rancangan solusi dan mekanisme”, sebelum paragraf “Pada downstream path, Client-Facing API menggunakan timeout ...”.

Pada jalur upstream, `Manager.Run` menjalankan dua goroutine poller yang terpisah. Poller BMKG mengambil kejadian seismik dan warning secara berurutan dalam siklus BMKG; pengambilan PVMBG berlangsung pada worker lain. Timeout HTTP BMKG adalah 2 detik, sedangkan PVMBG 4 detik agar delay uji 3 detik tetap dapat diselesaikan. Request membawa `context.Context` untuk pembatalan saat layanan dihentikan. Setiap worker melakukan retry pada siklus polling berikutnya, tanpa penambahan goroutine pada setiap tick.

Kegagalan fetch atau commit tidak menghapus data kanonis yang sudah tersimpan. Cursor hanya maju setelah penyimpanan berhasil sehingga siklus berikutnya dapat mengulang data yang belum selesai diproses. Ketika salah satu endpoint BMKG gagal, data dari endpoint yang berhasil masih dapat dipetakan dan disimpan, tetapi status sumber BMKG tetap stale. Pada PVMBG, kegagalan mapping mempertahankan cursor meskipun record valid dalam respons yang sama telah tersimpan. Jalur query tetap membaca Canonical Store, sehingga kelambatan upstream tidak menjadi waktu tunggu langsung bagi setiap permintaan klien.

Lokasi dan tindakan ketiga: sisipkan paragraf dan tabel berikut setelah kalimat “Dengan mekanisme ini, last-known data tetap dapat digunakan selama storage masih tersedia.”

Freshness diperbarui berdasarkan keberhasilan satu siklus ingestion yang lengkap. `available` menunjukkan keberhasilan fetch terakhir, sedangkan `last_ingested_at` diperbarui setelah pemetaan dan penyimpanan berhasil. Fetch yang berhasil tetapi diikuti commit gagal dapat menghasilkan `available=true` dan `stale=true`. Sumber juga menjadi stale jika belum pernah menyelesaikan ingestion atau waktu sejak ingestion terakhir melewati nilai terbesar antara 15 detik dan tiga interval polling. Siklus tanpa record baru tetap dapat memperbarui freshness jika transaksi penyimpanan berhasil. Metadata ini menggambarkan kondisi pipeline; umur masing-masing hazard tetap dinilai melalui `occurred_at` dan `ingested_at`.

| Kondisi sumber / ingestion | Perilaku status dan data |
|---|---|
| Fetch, pemetaan, dan commit berhasil | `available=true`, `stale=false`; `last_ingested_at` diperbarui dan penanda kegagalan dipulihkan. |
| Fetch berhasil tetapi commit gagal | `available=true`, `stale=true`; timestamp ingestion terakhir dipertahankan dan `ingestion_error` tersedia pada API internal. |
| Upstream gagal / outage | `available=false`, `stale=true`; data terakhir tetap dapat dibaca jika storage tersedia. |
| BMKG hanya berhasil pada satu endpoint | Data yang tersedia dapat disimpan, tetapi sumber tetap stale karena siklus belum lengkap. |
| Poller tidak menyelesaikan siklus melampaui ambang | Snapshot status menandai sumber stale meskipun belum ada error HTTP baru. |
| Storage tidak dapat dibaca | Aggregator mengembalikan 503; Client-Facing API meneruskan kegagalan sebagai respons terkontrol. |

## 12. Bab 7.2: alasan dan batasan P2 (halaman 14-15)

Lokasi dan tindakan pertama: tambahkan paragraf berikut pada awal “Alasan pilihan dan alternatif”, sebelum pembahasan concurrency limiter yang sudah ada.

Dua poller dipilih karena karakteristik latensi BMKG dan PVMBG berbeda. Dalam satu loop yang memanggil kedua sumber secara berurutan, waktu tunggu PVMBG ikut menunda siklus BMKG. Worker terpisah memberi masing-masing sumber jadwal dan timeout sendiri. Query terhadap data persisten juga menghindari pengambilan upstream ulang pada setiap permintaan klien. Konsekuensinya, respons harus menyertakan freshness agar keterlambatan pembaruan sumber tetap terlihat.

Lokasi dan tindakan kedua: tambahkan paragraf berikut pada akhir “Batasan yang disadari”, sebelum subjudul “Konfigurasi dan hasil pengujian”.

Pemisahan worker mengisolasi waktu tunggu sumber pada proses yang sudah berjalan, tetapi kedua poller tetap menggunakan database dan resource host yang sama. Bottleneck storage atau keterbatasan CPU masih dapat memengaruhi query. Compose juga menunggu dependency healthy pada startup sehingga pengujian outage setelah sistem siap tidak membuktikan bahwa cold start dapat selesai ketika seluruh dependency tidak tersedia. Pengujian unit memastikan BMKG tetap polling ketika PVMBG ditahan, sedangkan pemenuhan target p95 dinilai dari load test terautentikasi yang hasilnya dicantumkan pada tabel P2.

## 13. Bab 7.3: autentikasi upstream (halaman 16)

Lokasi dan tindakan: tambahkan paragraf berikut setelah judul “Rancangan solusi dan mekanisme”, sebelum “Downstream authentication menggunakan tiga identity ...”.

Pada boundary upstream, BMKG hanya memvalidasi nilai pada `X-BMKG-Key`, sedangkan PVMBG memvalidasi pasangan skema Bearer dan token pada `Authorization`. Nilai yang diterima dibandingkan menggunakan `crypto/subtle.ConstantTimeCompare`. Kredensial salah, header dari domain sumber lain, atau kredensial yang tidak dikirim menghasilkan HTTP 401. Aggregator memiliki adapter sumber terpisah yang memasang header sesuai tujuan dan menolak startup jika kedua nilai kredensial upstream sama. Endpoint admin PVMBG dilindungi oleh token PVMBG yang sama dengan endpoint datanya. Kredensial downstream BNPB tetap dikelola oleh Auth Service dan tidak dipakai sebagai kredensial mock.

## 14. Bab 7.4 dan 7.5: hubungan ingestion dengan persistence dan distribusi

Lokasi dan tindakan untuk Bab 7.4 (halaman 19): sisipkan paragraf setelah kalimat “Cursor ingestion maju setelah commit berhasil agar kegagalan write tidak menyebabkan event terlewat.”

Overlap polling dan cache korelasi dapat menghasilkan pemetaan ulang atas rekaman sumber yang sama. Repository membedakan replay identik dari perubahan isi hazard. Perubahan `ingested_at` saja tidak menaikkan revisi atau menambahkan pesan outbox. Warning tsunami yang baru atau lebih mutakhir dapat mengubah severity maupun atribut, sehingga hazard dengan ID yang sama diperbarui dan snapshot revisi baru ditulis. Aturan ini menghubungkan korelasi sumber pada Aggregator dengan idempotensi penyimpanan.

Lokasi dan tindakan untuk Bab 7.5 (halaman 23): sisipkan paragraf setelah kalimat “Retry/reconnect memakai jeda tiga detik dan operasi jaringan dibatasi lima detik.”

Correlation ID dibuat pada awal setiap siklus polling dan diteruskan ke request upstream. ID yang sama menyertai penyimpanan hazard/outbox, publikasi AMQP, dan pencatatan consumer. Jalur ini memungkinkan hasil transformasi satu laporan sumber ditelusuri sampai snapshot yang diterima subscriber. Worker publisher berjalan terpisah dari poller; gangguan broker mempertahankan outbox pending sementara ingestion masih dapat menyimpan data selama PostgreSQL tersedia.

## 15. Bab 8: rekap bukti dan keterlacakan (halaman 25)

Lokasi dan tindakan: ganti baris P1; tambahkan path bagian A pada baris P2 dan P3 tanpa menghapus path anggota lain.

| Problem | Lokasi kode / konfigurasi | Bukti utama / lampiran |
|---|---|---|
| P1 | `services/bmkg/cmd/server/main.go`; `services/pvmbg/cmd/server/main.go`; `services/aggregator/internal/domain/hazard.go` (`UnmarshalJSON`, `MapSeismic`, `MapVolcanic`); `services/aggregator/internal/ingest/manager.go`; `scripts/check-p1-report.py`; `scripts/check-ingestion.py`. | Gambar 1-3; `docs/evidence/member-a/p1/report-check.json`; `docs/evidence/member-a/p1/ingestion-check.json`. |
| P2, tambahan bagian A | `services/aggregator/internal/source/http.go`; `services/aggregator/internal/ingest/manager.go` dan `status.go`; `services/aggregator/internal/ingest/status_test.go`; `services/aggregator/internal/httpapi/server.go`. | Gambar 4-9 yang sudah ada; unit test independensi poller, commit gagal, status stale, dan recovery. |
| P3, tambahan bagian A | `requireBMKGKey` pada `services/bmkg/cmd/server/main.go`; `requirePVMBGToken` pada `services/pvmbg/cmd/server/main.go`; adapter `services/aggregator/internal/source/http.go`; `scripts/check-cross-credentials.py`. | Gambar 15 yang sudah ada. |

## 16. Bab 10: lampiran bukti P1 (halaman 26)

Lokasi dan tindakan: ganti tulisan “P1” yang masih kosong di bawah “B. Bukti Pengujian” dengan judul dan isi berikut. Tempatkan sebelum lampiran P2. Gunakan tiga screenshot baru sebagai Gambar 1-3. Jika ingin penomoran sublampiran berurutan, ubah B.1 P2 menjadi B.2, B.2 P3 menjadi B.3, B.3 P4 menjadi B.4, dan B.4 P5 menjadi B.5; nomor gambar lama tetap.

### B.1 Problem 1: arsitektur dan interoperabilitas data

Pengujian dilakukan pada 9 Oktober 2026 pukul 19.41.19 sampai 19.41.39 WIB pada project `tubesaat-p1-report`. Sembilan container dijalankan dengan polling Aggregator 3 detik, interval generator BMKG dan PVMBG 10 detik, serta delay PVMBG 750 ms. Hasil disimpan pada `docs/evidence/member-a/p1/report-check.json` dan `ingestion-check.json`. Gambar dihasilkan menggunakan Freeze dari keluaran perintah terminal yang membaca hasil pengujian tersimpan. Keluaran checker asli tersedia pada `ingestion-run.log`.

Gambar 1 memperlihatkan kejadian BMKG dan warning dengan `event_id` yang sama. Kejadian bermagnitudo 6,0 tersebut dipetakan menjadi hazard seismik di Laut Maluku dengan severity `SIAGA` sesuai warning. Koordinat, magnitudo, kedalaman, potensi tsunami, dan rincian warning dipertahankan pada hasil kanonis.

**Sisipkan `docs/evidence/member-a/p1/p1-01-bmkg-tsunami.png`.**

Caption: Gambar 1. Pemetaan SeismicEvent dan korelasi TsunamiWarning BMKG menjadi HazardEvent kanonis.

Gambar 2 membandingkan laporan PVMBG versi 1 dan versi 2 dengan hasil pemetaan masing-masing. Laporan baru menyimpan `confidence_level` sebesar `0.7036322458518115` pada `attributes`, sama dengan nilai sumber. Record lama tetap tidak memiliki properti tersebut. Nama dan koordinat kanonis ditentukan dari tabel referensi BNPB berdasarkan `volcano_id`.

**Sisipkan `docs/evidence/member-a/p1/p1-02-pvmbg-schema-evolution.png`.**

Caption: Gambar 2. Pemetaan laporan PVMBG v1/v2; confidence_level dipertahankan pada atribut kanonis tanpa mengubah record lama.

Gambar 3 memperlihatkan bahwa ID dan `StartedAt` sembilan container tidak berubah selama skenario dijalankan. Catatan migrasi tetap pada versi 1 dengan `applied_at` `2026-10-09T12:40:13.983126+00:00`. Log laporan versi 2 menghubungkan fetch sumber, commit penyimpanan, dan konfirmasi publikasi melalui correlation ID `poll-PVMBG-1791549680067624251`. Notification Consumer dan Dashboard Consumer menerima message ID 55, revisi 1, dengan hazard ID `haz_c92aa4f419dd7cda468954e348041af8` serta snapshot payload yang sama.

**Sisipkan `docs/evidence/member-a/p1/p1-03-runtime-verification.png`.**

Caption: Gambar 3. Verifikasi perubahan skema saat runtime tanpa restart atau migrasi tambahan, beserta keterlacakan snapshot pada dua consumer.

## 17. Koreksi konsistensi hasil yang sudah ada

Bagian ini merupakan catatan penyuntingan, bukan paragraf tambahan laporan. Sesuaikan narasi dengan bukti yang dipilih; jangan mencampur angka dari run berbeda.

| Lokasi | Isi sekarang | Tindakan |
|---|---|---|
| Bab 7.2, bukti “Tidak ada resource exhaustion” (halaman 16) | 194 HTTP 200 dan 62 HTTP 429 pada burst 256 request. | Screenshot Gambar 8-9 menampilkan 224 HTTP 200 dan 32 HTTP 429. Jika screenshot tersebut dipertahankan, ganti angka narasi menjadi 224 dan 32. |
| Bab 7.3, bukti expiry (halaman 19) | Waktu tunggu 60,083 detik. | Screenshot Gambar 13-14 menampilkan `expiry_wait_seconds=59.831`. Ganti dengan “Waktu tunggu tercatat 59,831 detik pada pengujian natural expiry dengan TTL access token 60 detik.” TTL dan lama tunggu yang tercatat tidak harus identik karena token diterbitkan sebelum proses menunggu dimulai. |
| Bab 5, definisi stale (halaman 9) | “available dan stake” serta penjelasan freshness umum. | Ganti salah ketik `stake` menjadi `stale`. Gunakan definisi freshness pipeline dari tambahan Bab 7.2 agar tidak disamakan dengan umur setiap record. |
| Bab 6 dan beberapa screenshot Auth | Narasi merujuk `check-member-b-auth.py`, screenshot menampilkan `check-auth.py`. | Jelaskan apakah checker pengambilan bukti berasal dari checkout berbeda atau skrip lokal; cocokkan rujukan dengan bukti. Jangan mengganti nama pada screenshot seolah run lama memakai file baru. |

P1 baru tidak mengukur throughput maupun p95. Angka P2 pada laporan tetap berasal dari pengujian sustained load yang sudah dilampirkan.

## 18. Bab 9: penggunaan AI pada tambahan Member A

Lokasi dan tindakan: tambahkan paragraf berikut setelah paragraf pembuka “Bantuan AI digunakan untuk menyusun draf awal ...”. Isian tentang bantuan AI lain dan Form Penggunaan AI tetap perlu dilengkapi menurut penggunaan aktual kelompok.

Pada penyusunan tambahan bagian Member A, Codex digunakan untuk membaca konteks proyek, menelaah implementasi mock dan Aggregator, serta menyusun draf uraian arsitektur, pemetaan data, polling, freshness, dan autentikasi upstream. Bantuan juga mencakup pembuatan skrip pengumpulan bukti P1 dan rendering screenshot dari hasil pengujian. Pengujian dijalankan pada sistem lokal dengan Docker Compose; payload sumber dibandingkan dengan data kanonis, state container dan migrasi diperiksa sebelum serta sesudah perubahan skema, dan snapshot hasil dibandingkan pada dua consumer. Draf tambahan dan bukti ini menjadi bahan peninjauan penulis sebelum dimasukkan ke laporan final.
