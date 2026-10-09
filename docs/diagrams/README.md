# Diagram Bab 3

Diagram disusun berdasarkan implementasi aktual pada `docker-compose.yml`, adapter sumber, ingestion manager, API internal, persistence, dan publisher. SVG merupakan sumber vektor yang dapat diedit. PNG dirender pada skala 2× melalui Chrome dan diperiksa secara visual.

| Diagram | Penempatan | Caption yang dapat dipakai |
|---|---|---|
| `bab-3-arsitektur-sistem.svg` / `.png` | Menggantikan diagram utama setelah judul Bab 3. | Arsitektur sistem koordinasi kebencanaan: polling sumber mandiri, akses klien melalui API dan Auth Service, penyimpanan milik Aggregator, serta distribusi event melalui RabbitMQ. |
| `bab-3-alur-internal-aggregator.svg` / `.png` | Menggantikan diagram ASCII pada tambahan Bab 3, setelah paragraf yang berakhir dengan “pipeline sumber masih berhasil memperbarui data.” | Alur internal Aggregator: dua poller mandiri, korelasi dan pemetaan data kanonis, query terhadap data tersimpan, serta publikasi melalui worker outbox. |

Panah HTTP dan SQL menunjukkan pemanggil ke tujuan; respons kembali pada koneksi yang sama. Panah internal menunjukkan data atau operasi repository. Garis putus-putus menunjukkan publikasi/distribusi event. Batas putus-putus yang mengelilingi komponen menunjukkan lingkup BNPB atau batas satu layanan Aggregator.

Diagram utama memuat sembilan container. Klien downstream berada di luar daftar sembilan container. Diagram internal memerinci komponen di dalam satu Aggregator, bukan layanan atau container tambahan.

Reproduksi dari root repo:

```sh
python3 -B scripts/render-report-diagrams.py
```

Python 3 dan Google Chrome diperlukan. Gunakan `--chrome` untuk menentukan lokasi Chrome pada mesin lain dan `--output-dir` untuk direktori keluaran berbeda. Perubahan label/layout dilakukan pada skrip sumber; kode layanan tidak diubah.
