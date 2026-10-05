# Infrastruktur

Canonical Store memakai PostgreSQL + JSONB dan dimiliki Aggregator. Compose menyediakan database, health check, volume persisten, dan jaringan storage khusus. Pesan yang perlu dipublish disimpan pada outbox dalam transaksi yang sama dengan hazard.

RabbitMQ dan worker outbox sudah tersedia. Infrastruktur mengimpor topology sebelum broker healthy; consumer notifikasi dan dashboard memakai volume jurnal masing-masing dan manual ack. Detail startup dan batas durability ada di [README RabbitMQ](message-broker/README.md). Cara memeriksa persistence, distribusi event, serta recovery ada di [panduan pengujian](../tests/README.md).
