# Pengujian Lintas Layanan

Pemeriksaan persistence tahap 1 memakai PostgreSQL nyata dan schema uji sementara milik Aggregator:

```sh
docker compose --env-file .env.example up --build -d canonical-store bmkg pvmbg aggregator
docker compose --env-file .env.example -f docker-compose.yml -f tests/compose-storage.yml run --build --rm --no-deps aggregator
```

Override hanya menjalankan tes modul Aggregator dengan builder image. Database dan record kanonis yang sudah ada tidak dihapus. Tanpa `TEST_DATABASE_URL`, pemeriksaan PostgreSQL pada `go test ./...` lokal dilewati; tes retry polling dan HTTP outage tetap dijalankan.

Bukti tahap 1 ada di `docs/evidence/anggota-c/stage-1/`. Skenario consumer/RabbitMQ dan load test ditambahkan pada tahap selanjutnya.
