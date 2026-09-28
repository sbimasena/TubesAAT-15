# Sistem Koordinasi Bencana IF4031 M1

Inisialisasi repositori untuk sistem koordinasi bencana terdistribusi yang mengintegrasikan data BMKG dan PVMBG untuk BNPB.

## Teknologi

- Backend: Go 1.27.1
- Kontainer: Docker
- Orkestrasi lokal: Docker Compose (kontainer Canonical Store dan broker menunggu keputusan teknologi)
- Router HTTP, Canonical Store, dan message broker: TBD

## Struktur Repositori

- `services/`: tujuh layanan Go yang dapat dibangun secara mandiri
- `clients/`: klien CLI untuk media, tim lapangan, dan operasi internal
- `infrastructure/`: catatan keputusan Canonical Store dan message broker
- `docs/`: dokumentasi arsitektur dan API
- `scripts/`: skrip pengembangan
- `tests/`: skenario lintas layanan

Setiap layanan memiliki modul Go sendiri. Jalankan `go test ./...` dari direktori layanan untuk mengompilasi dan menguji modul tersebut. Entrypoint saat ini masih berupa placeholder; perilaku layanan belum diimplementasikan.

Compose saat ini mencakup tujuh kontainer aplikasi. Kontainer Canonical Store dan message broker akan ditambahkan setelah teknologinya dipilih. Entrypoint placeholder langsung selesai, sehingga layanan belum berjalan aktif.