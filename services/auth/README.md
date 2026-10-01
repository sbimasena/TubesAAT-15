# Layanan Auth / Token

Layanan Auth saat ini adalah kerangka HTTP yang berjalan mandiri. `GET /health` mengembalikan `{"status":"ok","service":"auth"}`. Permintaan memiliki `X-Correlation-ID` dan log JSON terstruktur. Login, identitas, Argon2id, JWT, refresh, dan validasi token **belum diimplementasikan**.

`AUTH_PORT`, `JWT_SIGNING_SECRET`, `REFRESH_TOKEN_TTL_SECONDS`, serta ID dan password untuk ketiga klien downstream harus diisi melalui environment sebelum layanan dijalankan. `ACCESS_TOKEN_TTL_SECONDS` memiliki nilai awal 60 detik. Konfigurasi diperiksa saat startup, tetapi layanan belum menyimpan atau menggunakan kredensial untuk autentikasi. Jangan memasukkan secret nyata ke Git. **NOT FINAL:** The Auth port and Compose wiring await agreement with Member C.

Setelah variabel lingkungan wajib diisi, jalankan `go run ./cmd/server` dari direktori `services/auth`, lalu periksa `GET /health` pada port `AUTH_PORT`. Pengujian unit modul: `go test ./...` dari direktori yang sama.

Dockerfile tersedia untuk membangun modul secara mandiri; build dan runtime Docker belum terverifikasi karena Docker Engine tidak tersedia saat pemeriksaan lokal. Compose saat ini belum memasok seluruh variabel wajib Auth.
