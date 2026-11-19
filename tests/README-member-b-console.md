# Konsol lokal pengujian Auth dan Client API

Konsol menyediakan pemeriksaan HTTP, tes komponen, dan langkah operator untuk Auth dan Client API. Jalankan dari root repo menggunakan Python 3; tidak perlu npm atau library tambahan.

## Menjalankan

Jalankan stack Compose sesuai [README](../README.md) terlebih dahulu.

```sh
python scripts/serve-member-b-ui.py --env-file .env.member-b.local
```

Buka **http://127.0.0.1:18765**. Port Auth/API Klien dibaca dari file lingkungan. Target dapat diganti lewat argumen origin localhost:

```sh
python scripts/serve-member-b-ui.py --auth-url http://127.0.0.1:8084 --client-url http://127.0.0.1:18080
```

Tanpa `--env-file`, masukkan password pada kartu identitas. Dengan file tersebut, password dapat dikosongkan; server lokal membacanya tanpa mengirim password ke halaman. ID custom tetap dapat diisi. Konsol tidak menyimpan password atau token ke disk. File lingkungan harus tetap di luar commit.

HTML tidak dijalankan dengan klik ganda/file://: Auth/API Klien tidak menyediakan CORS. Server Python menyajikan halaman dan meneruskan request ke endpoint publik yang telah dibatasi. Server hanya bind `127.0.0.1`; target hanya origin HTTP loopback. Auth introspection internal, Aggregator langsung, dan PostgreSQL tidak diakses oleh konsol.

## Urutan pemeriksaan

1. **Periksa health** untuk Auth/API Klien.
2. **Jalankan rangkaian HTTP**: login tiga peran, tujuh/sebelas field, dua sumber, password salah/silang, bearer hilang/rusak, raw Media 403, query/field 400, filter kosong 200, alias checkpoint, refresh, replay, penolakan access token lama, correlation ID, dan no-store. Data kedua sumber diperlukan. Rangkaian ini merotasi token pada sesi yang diuji.
3. **Jalankan tes Go + race**: menjalankan ulang tes kedua modul B dengan `-count=1`. Argon2id, penyimpanan hash refresh, rotasi bersamaan, revokasi, restart, timeout, cancellation, pelepasan permit, malformed response, dan redaksi log diuji pada fixture. Go dan race toolchain harus tersedia.
4. **Uji expiry alami Tim Lapangan**: login sesi baru, tunggu TTL nyata (maksimal 300 detik), periksa 401, lalu refresh tanpa login lagi. Jam tidak diubah. Penantian dapat dibatalkan.
5. Coba **request hazard** dengan pilihan peran, filter, dan raw/fields. Respons ditampilkan; token pasangan Auth disaring. Informasi JWT pada kartu hanya decode, bukan verifikasi signature oleh browser.
6. **Jalankan burst** memakai sesi Operasi Internal yang aktif. Maksimal 128 request paralel dari server konsol, bukan koneksi browser. Status 200/429 dan p50/p95 dicatat. Tanpa 429, hasil `CHECK`: kapasitas mungkin belum penuh; bukan kegagalan otomatis. Status lain menghasilkan `FAIL`. Lihat `tests/README.md` untuk pengujian beban berkelanjutan.
7. Operator dapat mengubah outage/lifecycle pada project uji, lalu memilih **Amati respons saat ini**. Observasi satu respons tidak membuktikan keseluruhan transisi outage/recovery. Petunjuk checker dan efek stop/start ada di halaman. Konsol tidak mengubah lifecycle atau status sumber.
8. **Unduh hasil tanpa token** untuk mendapatkan JSON pemeriksaan. Export tidak mengandung password, access/refresh token, atau payload hazard. JSON juga ditampilkan dalam kotak yang dapat disalin jika browser tidak mendukung unduhan. Simpan hasil di luar repo.

Verifikasi kontrak endpoint, lifecycle Docker, dan log proses melalui checker deployment. Latensi HTTP konsol mencakup relay. P2 berkelanjutan, upstream P3, dan fanout sembilan layanan tidak diklaim.

## Menghentikan dan menguji konsol

Ctrl+C menghentikan server konsol; service Docker tetap berjalan. Refresh/tutup halaman menghapus sesi lokal, tanpa melakukan revokasi di Auth. Setelah Auth restart, login ulang karena sesi Auth menggunakan memori.

```sh
python tests/test_member_b_console.py
```

Tes ini memakai server fixture localhost untuk memeriksa batas rute, pembatasan origin, tidak adanya password pada metadata, pelestarian status/correlation, penolakan redirect, dan batas burst. Ini bukan bukti runtime Auth/API Klien.

HTML, server konsol, petunjuk, dan tes pendukung dibuat sebagai perubahan lokal; tidak di-stage, di-commit, atau di-push.
