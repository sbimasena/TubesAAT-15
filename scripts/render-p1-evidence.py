#!/usr/bin/env python3
"""Render collected P1 evidence as local HTML and real headless-Chrome screenshots.

Screenshots are views of report-check.json, not simulated terminal sessions.
Requires Google Chrome. Uses only the Python standard library.
"""
import argparse
import html
import json
from pathlib import Path
import subprocess
import tempfile


def esc(value):
    return html.escape(str(value))


def panel(title, value):
    return '<section><h2>' + esc(title) + '</h2><pre>' + esc(json.dumps(value, indent=2, ensure_ascii=False)) + '</pre></section>'


def table(headers, rows):
    return '<table><thead><tr>' + ''.join('<th>' + esc(h) + '</th>' for h in headers) + '</tr></thead><tbody>' + ''.join(
        '<tr>' + ''.join('<td>' + esc(v) + '</td>' for v in row) + '</tr>' for row in rows) + '</tbody></table>'


def document(number, title, data, body):
    return '''<!doctype html><html lang="id"><meta charset="utf-8"><title>''' + esc(title) + '''</title>
<style>
*{box-sizing:border-box}body{margin:0;padding:28px 32px;background:#fff;color:#17212c;font:18px Arial,sans-serif}
header{border-bottom:2px solid #283a4b;margin-bottom:22px;padding-bottom:16px}h1{font-size:28px;margin:0 0 12px}
.meta{font-size:15px;line-height:1.6;color:#455767}.status{float:right;border:1px solid #246442;padding:6px 14px;color:#246442;font-weight:700}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:20px}section{border:1px solid #9eacb7;padding:16px;margin-bottom:18px;break-inside:avoid}
h2{font-size:21px;margin:0 0 12px}pre{font:17px/1.38 Menlo,Consolas,monospace;margin:0;white-space:pre-wrap;overflow-wrap:anywhere}
table{width:100%;border-collapse:collapse;margin-bottom:20px;font:16px/1.35 Menlo,Consolas,monospace}
td,th{border:1px solid #a6b3bd;padding:9px;text-align:left;overflow-wrap:anywhere}th{background:#eef2f5}
.note{margin:8px 0 20px;font-size:17px;line-height:1.5}footer{border-top:1px solid #9eacb7;padding-top:12px;font-size:14px;color:#455767}
</style><header><span class="status">''' + esc(data['result']) + '''</span><h1>''' + esc(title) + '''</h1>
<div class="meta">Project: ''' + esc(data['project']) + ''' | Pengujian: ''' + esc(data['started_at']) + ''' s.d. ''' + esc(data['finished_at']) + '''<br>
Sumber aplikasi: WORKSPACE, commit dasar ''' + esc(data['source_commit']) + '''<br>
Polling: ''' + esc(data['configuration']['POLL_INTERVAL_SECONDS']) + ''' detik | Generator BMKG/PVMBG: ''' + esc(data['configuration']['BMKG_GENERATE_INTERVAL_SECONDS']) + '/' + esc(data['configuration']['PVMBG_GENERATE_INTERVAL_SECONDS']) + ''' detik | Delay PVMBG: ''' + esc(data['configuration']['PVMBG_DELAY_MS']) + ''' ms</div></header>''' + body + '''
<footer>Gambar ''' + str(number) + '''. Cuplikan bukti pengujian dari report-check.json.</footer></html>'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, default=Path('docs/evidence/member-a/p1/report-check.json'))
    parser.add_argument('--chrome', default='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome')
    args = parser.parse_args()
    data = json.loads(args.input.read_text())
    if data.get('result') != 'PASS':
        raise SystemExit('Only a completed passing evidence file can produce these report figures.')
    folder = args.input.parent.resolve()
    mapping = data['bmkg']
    body1 = '<div class="grid"><div>' + panel('SeismicEvent dari BMKG', mapping['source_event']) + panel('TsunamiWarning terkait', mapping['source_warning']) + '</div><div>' + panel('HazardEvent yang tersimpan', mapping['hazard']) + '</div></div>'
    body1 += '<p class="note">Pencocokan event_id, koordinat, atribut, waktu kejadian, ID deterministik, dan severity dari warning: ' + esc(mapping['mapping_and_correlation']) + '.</p>'
    body2 = '<div class="grid"><div>' + panel('VolcanicReport v1 dari PVMBG', data['pvmbg_v1']['source_report']) + panel('HazardEvent v1', data['pvmbg_v1']['hazard']) + '</div><div>' + panel('VolcanicReport v2 dari PVMBG', data['pvmbg_v2']['source_report']) + panel('HazardEvent v2', data['pvmbg_v2']['hazard']) + '</div></div>'
    body2 += '<p class="note">POST /admin/schema-version dengan body {"version":2} menambahkan laporan baru saat runtime. confidence_level sumber = attributes.confidence_level kanonis = ' + esc(data['pvmbg_v2']['source_report']['confidence_level']) + '. Record v1 tetap tidak memiliki field tersebut. Pemetaan kedua versi: PASS.</p>'
    body3 = '<h2>Identitas container dan waktu startup sebelum/sesudah uji</h2>' + table(['Layanan', 'Container ID', 'StartedAt', 'Tidak berubah'], [
        [name, value['container_id'], value['started_at'], str(value['unchanged']).lower()] for name, value in sorted(data['no_restart'].items())])
    before, after = data['migrations_before'][0], data['migrations_after'][0]
    body3 += '<h2>Migrasi database</h2>' + table(['Kondisi', 'Version', 'AppliedAt'], [['Sebelum', before['version'], before['applied_at']], ['Sesudah', after['version'], after['applied_at']]])
    body3 += '<h2>Alur laporan v2 pada log Aggregator</h2><p class="note">Correlation ID: ' + esc(data['v2_correlation_id']) + '</p>' + table(['Waktu UTC', 'Operasi / pesan log', 'Status / event', 'Latensi (ms)'], [
        [r['time'], r.get('operation', r.get('msg')), r.get('status', 'events=' + str(r['events']) if 'events' in r else 'message_id=' + str(r['message_id']) if 'message_id' in r else ''), r.get('latency_ms', '')] for r in data['v2_trace']])
    body3 += '<h2>Snapshot v2 pada dua consumer</h2>' + table(['Consumer', 'Message ID', 'Hazard ID', 'Revisi', 'Hasil'], [
        [name, row['message_id'], row['hazard_id'], row['hazard_revision'], row['result']] for name, row in data['consumers'].items()])
    body3 += '<p class="note">Kedua payload consumer cocok dengan snapshot HazardEvent v2 dan mempertahankan correlation ID yang sama. Pemeriksaan payload, tanpa restart, dan tanpa migrasi tambahan: PASS.</p>'
    pages = [('p1-01-bmkg-tsunami', 1, 'P1: pemetaan BMKG dan korelasi tsunami', body1, 960),
             ('p1-02-pvmbg-schema-evolution', 2, 'P1: pemetaan PVMBG sebelum dan sesudah perubahan skema', body2, 1160),
             ('p1-03-runtime-verification', 3, 'P1: perubahan runtime tanpa restart atau migrasi tambahan', body3, 1530)]
    with tempfile.TemporaryDirectory(prefix='tubesaat-p1-chrome-') as profile:
        for stem, number, title, body, height in pages:
            page = folder / (stem + '.html')
            page.write_text(document(number, title, data, body))
            screenshot = folder / (stem + '.png')
            result = subprocess.run([args.chrome, '--headless', '--disable-gpu', '--hide-scrollbars',
                                     '--no-pdf-header-footer', '--force-device-scale-factor=1.5',
                                     '--user-data-dir=' + profile, '--window-size=1600,' + str(height),
                                     '--screenshot=' + str(screenshot), page.as_uri()], capture_output=True, text=True)
            if result.returncode or not screenshot.exists():
                raise SystemExit('Chrome screenshot failed: ' + result.stderr[-2000:])
            print(screenshot.relative_to(Path.cwd()))


if __name__ == '__main__':
    main()
