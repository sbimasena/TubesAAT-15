# Mock PVMBG

PVMBG is an independent Go service that seeds 20 historical volcanic reports and generates reports every `PVMBG_GENERATE_INTERVAL_SECONDS` (default: 10 seconds). The default response delay is 750 ms; set `PVMBG_DELAY_MS=3000` to reproduce the slow-upstream scenario. Supported delay values are 500–3000 ms.

## API

- `GET /health` is public and reports the active schema version, response delay in milliseconds, and whether outage simulation is active. These values let infrastructure checks restore the original runtime settings.
- `GET /volcanic-reports?since=<RFC3339>` requires `Authorization: Bearer <PVMBG_TOKEN>`.
- `POST /admin/schema-version` is **tokenless by decision of member B (16 Nov 2026)**: the M2 spec (Tester Agent contract, line 99) lets Tester Agent call admin endpoints on mocks without providing a credential, so the schema-injection lever stays callable (previous 401 finding in `docs/m2/temuan-tester-agent.md`). It accepts `{"version":N}` or `{"mode":"<name>"}` (set exactly one of the two). Reports already emitted keep their original fields; a new report with the new shape is appended immediately. If the assistant later confirms Tester Agent sends a token, re-wrap this handler with `requirePVMBGToken`.

### Mode skema (P3)

| Version | Mode | Bentuk wire untuk laporan baru | Dampak ke konsumen |
|---|---|---|---|
| 1 | `compatible` | Kontrak asli (M1) | — |
| 2 | `compatible` | + `confidence_level` (aditif) | Diterima otomatis |
| 3 | `rename_field` | `alert_level` → `status` | Field wajib hilang dari sudut pandang mapper |
| 4 | `change_type` | `reported_at` jadi unix seconds (int) | Gagal decode RFC3339 |
| 5 | `drop_required` | `alert_level` dihapus | Field wajib hilang |
| 6 | `broken_json` | Body respons dipotong di tengah dokumen | Respons tidak valid sebagai JSON |
| 7 | `oversize_json` | Field `padding` ±2 MiB | Melampaui batas ukuran pesan |
| 8 | `deep_json` | Field `extra` bersarang 20000 level | Melampaui batas kedalaman decoder |
| 9 | `invalid_utf8` | Field `note` berisi byte UTF-8 tidak valid | Gagal validasi UTF-8 |

**Kebijakan evolusi skema BNPB (deklarasi P3.1):**

- **Diterima otomatis** (mapping dilanjutkan tanpa perubahan kode): field baru, field opsional hilang, urutan field berubah, penambahan `confidence_level` (v2).
- **Di-park ke DLQ** (tidak dipetakan, tidak menggantung cursor): rename field wajib (`report_id`, `volcano_id`, `alert_level`, `reported_at`), perubahan tipe, field wajib hilang, pesan rusak (truncated / oversize / nested berlebihan / UTF-8 tidak valid).
- Mekanisme "penyesuaian sistem" sesudah kebijakan: alias eksplisit versi mapper berikutnya + reprocess dari DLQ, tanpa duplikat.

Semua perubahan mode berlaku pada laporan yang dibuat setelahnya dan tidak memerlukan restart maupun deploy ulang service ini.
- `POST /admin/outage` requires the PVMBG bearer token and accepts `{"enabled":true}` or `{"enabled":false}`.

Reports are returned as JSON arrays. Additive fields are included without restarting this service. Configure the token, delay, and generation interval through environment variables. The local Compose token default is `dev-pvmbg-token` and is for development only.
