# Layanan Auth / Token

Auth menyediakan login, rotasi refresh token, dan introspeksi untuk tiga identitas yang berbeda: Media, Tim Lapangan, dan Operasi Internal. Password dari lingkungan di-hash saat startup menggunakan Argon2id; store identitas menyimpan hash bersalt, bukan plaintext password.

## Endpoint

| Metode/path | Permintaan | Respons |
| --- | --- | --- |
| `GET /health` | Tanpa token | `200`, `{"status":"ok","service":"auth"}` |
| `POST /login` | JSON `client_id`, `password` | `200`, pasangan token; kredensial salah/tidak dikenal menghasilkan `401` |
| `POST /refresh` | JSON `refresh_token` | `200`, pasangan token baru; token kedaluwarsa, direvokasi, atau dipakai ulang menghasilkan `401` |
| `POST /internal/v1/introspect` | Header `X-Auth-Service-Key`, JSON `token` | `200`, `active=true` beserta `client_id`, `scope`, `session_id`; token tidak valid menghasilkan `200` dengan `active=false`; secret internal salah menghasilkan `401` |

Pasangan token berisi `access_token`, `token_type="Bearer"`, `expires_in` dalam detik, dan `refresh_token`. Scope ditentukan dari identitas di Auth; input `scope` dari klien tidak diterima. Body dibatasi 8 KiB; JSON rusak, field tidak dikenal, atau lebih dari satu dokumen JSON menghasilkan `400`. Respons memakai `Cache-Control: no-store`, correlation ID, dan log JSON tanpa password, token, atau secret.

Log penyelesaian login, refresh, dan introspeksi yang berhasil memuat `client_id` serta `scope` terverifikasi, bersama waktu, level, layanan, operasi, status, correlation ID, dan latensi. Identitas dari input login yang gagal tidak dicatat sebagai identitas terverifikasi. Metadata untuk log tidak menambah field pada respons pasangan token.

## Token dan sesi M1

JWT memakai HS256, issuer `tubesaat-auth`, audience `tubesaat-client-api`, serta klaim `sub`, `scope`, `iat`, `exp`, `jti`, `sid`, dan `generation`. Parser membatasi algoritma dan memeriksa expiry. `ACCESS_TOKEN_TTL_SECONDS` konfigurabel dengan nilai awal 60 detik. Signing key hanya dimiliki Auth; API Klien memakai introspeksi setiap request tanpa cache.

Sesi disimpan di memori milik Auth, sesuai keputusan M1. Refresh token berupa 32 byte acak yang dikodekan base64url; hanya hash SHA-256-nya yang disimpan dalam sesi. Refresh memeriksa token yang berlaku, menyiapkan pasangan baru, kemudian mengganti hash dan menaikkan versi sesi dalam satu lock. Kegagalan signing tidak mengonsumsi token lama; dua refresh dengan token yang sama tidak dapat sama-sama berhasil. Access token versi lama ditolak pada validasi setelah rotasi, termasuk jika belum kedaluwarsa. Request yang sudah lolos validasi tidak dibatalkan secara retroaktif.

Umur sesi refresh dihitung dari login pertama dan tidak diperpanjang oleh rotasi. Restart Auth menghapus sesi: klien harus login ulang. Ini keterbatasan PoC yang perlu dicatat dalam laporan; Auth tidak memakai database kanonis Aggregator. Revokasi sesi tersedia pada komponen internal dan diuji, tanpa endpoint administrasi publik pada M1.

Argon2id memakai salt acak 16 byte, memori 64 MiB, tiga iterasi, parallelism empat, dan hash 32 byte, mengikuti [dokumentasi Go Argon2](https://pkg.go.dev/golang.org/x/crypto/argon2). Verifikasi password diserialkan untuk membatasi pemakaian memori; ID tidak dikenal menjalankan verifikasi dummy.

## Konfigurasi dan pengujian

Isi `AUTH_PORT`, `JWT_SIGNING_SECRET`, `AUTH_INTERNAL_SECRET`, `REFRESH_TOKEN_TTL_SECONDS`, serta ID/password tiga klien melalui lingkungan. Kedua secret minimal 32 byte dan harus berbeda. `AUTH_INTERNAL_SECRET` hanya dibagikan kepada Auth/API Klien, terpisah dari kredensial upstream/downstream. `.env.example` menyediakan nama variabel dengan secret/password kosong. Jangan memasukkan konfigurasi nyata ke Git.

Auth memakai port container 8084. Compose utama mempublikasikannya hanya ke localhost; `AUTH_PORT` memilih port host dan tidak mengubah URL internal `http://auth:8084`.

Jalankan `go run ./cmd/server` dari `services/auth` setelah lingkungan diisi. Pengujian: `go test ./...` atau `go test -race ./...`. Binary mendukung `/service --healthcheck` dengan timeout 3 detik; mode healthcheck tidak membutuhkan secret startup.

Compose utama menyediakan konfigurasi wajib dan healthcheck Auth. Panduan login/refresh/expiry ada di [README API Klien](../client-api/README.md#pemeriksaan-autentikasi).
