# Consumer Notifikasi

Layanan Go mandiri menerima JSON `HazardEvent` dari exchange fanout durable `hazard.events`, melalui queue durable `notification`. `BROKER_URL` wajib diisi. Layanan hanya berada pada jaringan pesan, tanpa akses/kredensial PostgreSQL. Tidak mengimpor modul layanan lain atau business logic Aggregator.

## Hasil simulasi

Setiap ID pesan baru menghasilkan satu record simulasi notifikasi untuk semua severity yang valid. Tidak mengirim SMS/email atau memanggil layanan eksternal.

Hasil disimpan sebagai `notification_simulated` di `/data/events.jsonl` pada volume khusus layanan. Record mencakup ID pesan, ID hazard, revisi, correlation ID, waktu penerimaan, dan payload penuh termasuk atribut tambahan. ID outbox menjadi kunci deduplikasi; update hazard dengan ID pesan baru diproses tersendiri.

## Ack, restart, dan IO

Prefetch satu, manual ack, serta exclusive consumer (satu consumer aktif untuk queue ini). Queue tetap nonexclusive dan persisten. Record ditulis dan `file.Sync()` selesai sebelum ack; duplicate yang sudah tercatat di-ack tanpa append ulang. Lock file advisory menolak writer kedua pada volume yang sama. File dan directory di-sync saat startup.

Restart membaca jurnal untuk membangun ulang ID. Baris terakhir tanpa newline dipotong sebelum append; record rusak yang sudah diterminasi newline menyebabkan kegagalan eksplisit. Kegagalan write/sync tidak di-ack: sesi broker ditutup, file ditutup, dan jurnal dibuka/dipulihkan kembali sebelum retry setelah tiga detik. Ini mencegah append setelah hasil IO yang belum pasti.

Jaminan idempotensi berlaku untuk hasil simulasi di jurnal selama volumenya dipertahankan, dengan ID producer yang tetap unik. Menghapus journal menghapus deduplikasi; menginisialisasi ulang database producer dengan sequence ID awal sambil mempertahankan jurnal lama dapat menyebabkan benturan ID. Tidak ada jaminan exactly-once untuk efek eksternal. Jurnal/indeks belum dikompaksi dan bertambah mengikuti jumlah pesan; gunakan storage consumer lebih kuat saat workload melampaui M1.

## Validasi dan kesehatan

Consumer menerima body JSON maksimal 1 MiB, hazard ID maksimal 128 byte, source/type kanonis, severity valid, dan area nonkosong. Metadata harus berisi ID pesan integer positif, revision positif, schema_version=1, content_type=application/json, serta correlation ID 1–128 byte yang cocok dengan header X-Correlation-ID. Field payload tambahan dipertahankan. Payload/metadata yang tidak didukung ditolak tanpa requeue dan **dibuang** untuk subscription ini karena belum ada DLQ.

`GET /health` pada port internal 8080 mengembalikan 200 saat jurnal berhasil dipulihkan dan subscription aktif, atau 503 ketika sesi terputus/IO gagal. Compose menjalankan `/service --healthcheck`; port tidak diexpose ke host. Health menandakan readiness sesi terakhir, bukan jaminan bahwa setiap operasi disk berikutnya pasti berhasil. Reconnect memakai heartbeat lima detik, batas setup/IO jaringan lima detik, retry tiga detik, dan shutdown SIGTERM menutup socket untuk requeue pesan tanpa ack. Operasi filesystem tidak memiliki deadline jaringan tersebut.

Log JSON mencakup service, message_id, hazard_id, hazard_revision, correlation_id, result, redelivered, dan latency_ms. Record hasil unik, sedangkan log duplicate/penerimaan dapat muncul berulang. [Semantik acknowledgement RabbitMQ](https://www.rabbitmq.com/docs/confirms).

## Pemeriksaan

Dari root repositori:

```sh
docker compose --env-file .env up --build -d notification-consumer dashboard-consumer
docker compose --env-file .env -f docker-compose.yml -f tests/compose-consumers.yml run --build --rm --no-deps notification-consumer
python3 scripts/check-stage-3.py
```

Tes memakai file sementara, fake acknowledgement untuk menguji urutan IO/ack, dan race detector. Skrip runtime memakai broker nyata serta tujuh container yang sudah berjalan; menghentikan notifikasi/broker sementara, menguji retry ID yang sama lintas restart, dan memulihkan layanan tanpa menghapus volume. Gunakan `--output <file.json>` untuk menentukan lokasi hasil pemeriksaan.
