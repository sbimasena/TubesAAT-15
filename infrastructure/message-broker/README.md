# RabbitMQ dan Distribusi Event

Compose menjalankan `rabbitmq:4.2-management-alpine`, hostname tetap `message-broker`, dan volume `broker-data` untuk identitas node/data. AMQP port 5672 hanya pada jaringan `events`; management UI dipetakan ke `127.0.0.1:15672`. Jaringan ini terpisah dari `storage` PostgreSQL, tetapi bukan jaringan Docker `internal` agar UI lokal dapat diakses. Gunakan kredensial contoh hanya untuk development.

`start-with-topology.sh` menjalankan entrypoint resmi dan menunggu node selesai boot sebelum `rabbitmqctl import_definitions`. Import setelah boot memungkinkan RabbitMQ membuat user dari environment terlebih dahulu: import definitions saat boot pada node kosong akan melewati pembuatan user default. [Dokumentasi import](https://www.rabbitmq.com/docs/definitions).

Bootstrap membersihkan marker readiness lama, memperbaiki ownership volume jika dijalankan sebagai root, lalu memakai `su-exec` yang tersedia di image Alpine untuk menjalankan **server dan CLI sebagai rabbitmq**. Ini mencegah race pembuatan cookie Erlang milik root pada volume kosong. Marker lama dibersihkan sebelum turun privilege agar restart container lama juga berhasil. Tidak ada cookie/password yang dicetak atau volume yang dihapus. Entry point resmi tetap menjalankan server; [sumber entrypoint image](https://github.com/docker-library/rabbitmq/blob/master/docker-entrypoint.sh) menunjukkan pergantian user untuk perintah rabbitmq.

Readiness `/tmp/topology-ready` dibuat setelah import sukses; healthcheck memeriksa file dan ping node. Aggregator menunggu broker healthy pada startup Compose. Volume yang sama mempertahankan user lama: mengubah environment password tidak otomatis mengganti user pada volume yang sudah ada.

## Topology

`definitions.json` memuat exchange fanout durable `hazard.events`, queue durable classic `notification` dan `dashboard`, serta binding independen. Queue tidak exclusive/auto-delete dan tidak punya TTL/max-length. Producer hanya memeriksa exchange, tanpa mendeklarasikan atau mengetahui nama queue. Consumer notifikasi dan dashboard membaca queue masing-masing dengan manual ack/prefetch satu; queue menampung backlog jika consumer offline. Jurnal idempotensi berada pada volume khusus consumer, terpisah dari broker.

Pesan persistent dengan positive publisher confirm dan volume yang dipertahankan dapat bertahan restart. Mandatory return menandakan tidak ada route; confirm tidak membuktikan consumer selesai atau kedua queue masih terikat. Satu node/classic queue tidak memberi replikasi atau high availability. Pesan yang sudah di-ack dihapus; subscriber baru tidak menerima sejarah sebelum binding. [Confirms dan acknowledgement](https://www.rabbitmq.com/docs/confirms).

## Inspeksi queue dan uji recovery broker

```sh
docker compose --env-file .env up -d message-broker aggregator
docker compose --env-file .env exec -T message-broker rabbitmqctl list_queues name durable messages_ready messages_unacknowledged
docker compose --env-file .env logs --tail 50 aggregator
python3 scripts/check-stage-2.py
```

Skrip `check-stage-2.py` memakai file environment lokal lengkap dan overlay operator, memerlukan kedua consumer offline, memeriksa pesan dengan requeue, serta stop/start broker. Ia tidak purge/delete queue atau volume. Pesan yang diinspeksi menjadi redelivered; ini memang diperbolehkan oleh kontrak at-least-once.
