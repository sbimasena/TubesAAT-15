# API untuk Klien

Layanan ini sekarang menyediakan `GET /health`, log JSON terstruktur, dan propagasi `X-Correlation-ID`. URL Aggregator dan timeout diatur melalui `AGGREGATOR_BASE_URL` dan `AGGREGATOR_REQUEST_TIMEOUT_MS`; port layanan memakai `CLIENT_API_PORT` (awal 8080). Layanan memanggil Aggregator lewat HTTP dan tidak mengakses Canonical Store secara langsung.

Untuk checkpoint integrasi lokal saja, set `ENABLE_PROVISIONAL_HAZARD_ENDPOINT=true` dan panggil `GET /internal/provisional/hazards`. Endpoint ini meneruskan hanya filter `source`, `hazard_type`, `since`, dan `limit` ke `GET /internal/v1/hazards` Aggregator. Respons sukses meneruskan JSON Aggregator tanpa proyeksi field. Endpoint ini **tanpa autentikasi, mengandung field mentah, dan nonaktif secara default**; jangan publikasikan sebagai API klien final. Kegagalan/response tidak valid dari Aggregator menghasilkan 502, dan timeout menghasilkan 504. **NOT FINAL:** The Aggregator contract and public error semantics await agreement with Member A and the team.

Timeout penulisan respons server minimal 15 detik dan selalu menyediakan jeda 5 detik setelah timeout Aggregator, sehingga respons 504 masih dapat dikirim. Timeout saat membaca body Aggregator juga menghasilkan 504. **NOT FINAL:** The final dependency timeout value awaits team agreement.

Setelah variabel lingkungan wajib diisi, jalankan `go run ./cmd/server` dari direktori `services/client-api`, lalu periksa `GET /health` pada port `CLIENT_API_PORT`. Pengujian unit modul: `go test ./...` dari direktori yang sama.

Dockerfile tersedia untuk membangun modul secara mandiri; build dan runtime Docker belum terverifikasi karena Docker Engine tidak tersedia saat pemeriksaan lokal. **NOT FINAL:** Compose wiring awaits agreement with Member C. Compose saat ini belum memasok seluruh variabel wajib API Klien.
