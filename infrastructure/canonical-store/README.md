# Canonical Store

PostgreSQL 17 (`postgres:17-alpine`) berjalan sebagai `canonical-store` dengan volume `canonical-data`. Hanya Aggregator berada pada jaringan `storage` bersamanya dan menerima `DATABASE_URL`. PostgreSQL tidak memiliki port yang dipublikasikan ke host. Operator dapat menjalankan SQL melalui `docker compose exec` untuk inspeksi.

Pengaturan contoh ada pada `.env.example`: `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD`, dan `DATABASE_URL`. Gunakan nilai yang konsisten; karakter khusus pada password di URL perlu di-encode. Kredensial PostgreSQL contoh adalah untuk development. Role yang dibuat image PostgreSQL pada M1 juga menjalankan migrasi; belum ada pemisahan role migrasi dan role runtime.

Aggregator menjalankan migrasi embedded `services/aggregator/internal/store/migrations/001_canonical.sql` saat startup. `schema_migrations` mencatat versi 1. Tabel `hazard_events` menyimpan kolom kanonis dan `attributes JSONB`; tabel `hazard_outbox` menyimpan snapshot event dalam transaksi yang sama. Penambahan field PVMBG `confidence_level` hanya menambah key JSONB tanpa migrasi.

Restart container mempertahankan volume. Kehilangan atau penghapusan volume menghilangkan data; jangan memakai `docker compose down -v` untuk demo persistence. Worker Aggregator mengirim snapshot outbox ke RabbitMQ setelah commit; saat broker tidak tersedia, row tetap pending sampai pengiriman berhasil dikonfirmasi.
