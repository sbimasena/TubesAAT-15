#!/usr/bin/env python3
"""Draw exact architecture diagrams as editable SVG and render them with Chrome."""
import argparse
from html import escape
from pathlib import Path
import subprocess
import tempfile

INK = '#24313b'
MUTED = '#53616b'
BLUE = '#345d78'
BG = '#f5f7f8'


class Diagram:
    def __init__(self, width, height, title):
        self.width, self.height = width, height
        self.parts = [f'''<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}" role="img" aria-labelledby="title">
<title id="title">{escape(title)}</title>
<defs><marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="8" markerHeight="8" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="{INK}"/></marker>
<marker id="blue-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="8" markerHeight="8" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="{BLUE}"/></marker></defs>
<style>text {{font-family: Arial, Helvetica, sans-serif; fill: {INK};}} .body {{font-size: 23px;}} .small {{font-size: 21px; fill: {MUTED};}} .heading {{font-size: 27px; font-weight: 700;}} .heading-compact {{font-size: 23px; font-weight: 700;}} .group {{font-size: 24px; font-weight: 700; letter-spacing: .5px;}} .edge {{font-size: 21px;}} </style>
<rect width="100%" height="100%" fill="white"/>''']

    def text(self, x, y, lines, css='body', anchor='start', gap=31):
        if isinstance(lines, str):
            lines = [lines]
        self.parts.append(f'<text x="{x}" y="{y}" class="{css}" text-anchor="{anchor}">' + ''.join(
            f'<tspan x="{x}" dy="{0 if i == 0 else gap}">{escape(line)}</tspan>' for i, line in enumerate(lines)) + '</text>')

    def boundary(self, x, y, w, h, title):
        self.parts.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="none" stroke="#91a0ab" stroke-width="1.8" stroke-dasharray="9 6"/>')
        self.text(x + 22, y + 35, title, 'group')

    def node(self, x, y, w, h, title, lines, accent=False, heading='heading'):
        self.parts.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" fill="{BG if accent else "white"}" stroke="{BLUE if accent else INK}" stroke-width="2"/>')
        self.text(x + 19, y + 39, title, heading)
        self.text(x + 19, y + 77, lines, 'body', gap=30)

    def edge(self, points, blue=False, dashed=False):
        color = BLUE if blue else INK
        self.parts.append('<polyline points="' + ' '.join(f'{x},{y}' for x, y in points) +
                          f'" fill="none" stroke="{color}" stroke-width="2.3" stroke-linejoin="round"' +
                          (' stroke-dasharray="8 6"' if dashed else '') +
                          f' marker-end="url(#{"blue-arrow" if blue else "arrow"})"/>')

    def label(self, x, y, lines, anchor='middle'):
        if isinstance(lines, str):
            lines = [lines]
        width = max(len(line) for line in lines) * 11.7 + 16
        left = x - width / 2 if anchor == 'middle' else x - width if anchor == 'end' else x
        self.parts.append(f'<rect x="{left}" y="{y-23}" width="{width}" height="{len(lines)*29}" fill="white"/>')
        self.text(x, y, lines, 'edge', anchor, 29)

    def write(self, path):
        path.write_text('\n'.join(self.parts) + '\n</svg>\n')


def overview():
    d = Diagram(1600, 1220, 'Arsitektur sistem koordinasi kebencanaan BMKG–PVMBG–BNPB')
    d.text(40, 52, 'KLIEN DOWNSTREAM', 'group')
    d.text(840, 52, 'SUMBER MANDIRI', 'group')
    d.boundary(390, 330, 1150, 790, 'BNPB · LAYANAN DAN INFRASTRUKTUR')
    d.node(840, 90, 290, 160, 'BMKG Mock', ['SeismicEvent', 'TsunamiWarning', 'X-BMKG-Key'])
    d.node(1170, 90, 330, 160, 'PVMBG Mock', ['VolcanicReport', 'confidence_level opsional', 'Bearer token PVMBG'])
    d.node(40, 660, 260, 165, 'Klien', ['Media / Pers', 'Tim Lapangan', 'BNPB Internal Ops'])
    d.node(435, 405, 300, 145, 'Auth Service', ['Login dan refresh', 'JWT + rotasi token'])
    d.node(435, 660, 300, 165, 'Client-Facing API', ['GET /hazards', 'Otorisasi field', 'Batas konkurensi'])
    d.node(855, 660, 310, 165, 'Aggregator', ['2 poller · default 3 detik', 'Pemetaan + API internal', 'Worker outbox'], accent=True)
    d.node(1225, 660, 275, 165, 'Canonical Store', ['PostgreSQL', 'HazardEvent + JSONB', 'Transactional outbox'])
    d.node(855, 930, 310, 115, 'RabbitMQ', ['hazard.events · fanout'])
    d.node(1225, 905, 275, 85, 'Consumer notifikasi', ['Queue notification'], heading='heading-compact')
    d.node(1225, 1015, 275, 85, 'Consumer dashboard', ['Queue dashboard'], heading='heading-compact')

    # HTTP arrows indicate the caller; responses use the same connection.
    d.edge([(300, 700), (350, 700), (350, 477), (435, 477)], blue=True)
    d.label(330, 440, ['Login / refresh', 'HTTP'], 'end')
    d.edge([(300, 780), (435, 780)], blue=True)
    d.label(365, 746, ['HTTP', 'Bearer'])
    d.edge([(585, 660), (585, 550)], blue=True)
    d.label(610, 595, ['Introspeksi token', 'setiap request'], 'start')
    d.edge([(735, 742), (855, 742)], blue=True)
    d.label(795, 714, 'HTTP')
    d.edge([(930, 660), (930, 285), (985, 285), (985, 250)], blue=True)
    d.label(908, 466, ['Polling BMKG', 'HTTP/JSON'], 'end')
    d.edge([(1080, 660), (1080, 285), (1335, 285), (1335, 250)], blue=True)
    d.label(1100, 466, ['Polling PVMBG', 'HTTP/JSON'], 'start')
    d.edge([(1165, 742), (1225, 742)])
    d.label(1195, 624, ['SQL', 'baca / tulis'])
    d.text(1362, 864, 'Pemilik: Aggregator', 'small', 'middle')
    d.edge([(1010, 825), (1010, 930)], dashed=True)
    d.label(1010, 878, ['Publish dari outbox', 'AMQP · setelah commit'])
    d.edge([(1165, 965), (1195, 965), (1195, 947), (1225, 947)], dashed=True)
    d.edge([(1165, 1010), (1195, 1010), (1195, 1057), (1225, 1057)], dashed=True)
    d.text(40, 1160, 'Panah HTTP/SQL menunjukkan pemanggil → tujuan; respons kembali pada koneksi yang sama.', 'small')
    d.text(40, 1192, 'Garis putus-putus menunjukkan distribusi event. Setiap layanan dan infrastruktur berada pada container tersendiri.', 'small')
    return d


def aggregator():
    d = Diagram(1660, 1290, 'Alur internal Aggregator: polling, korelasi, pemetaan, query, dan outbox')
    d.boundary(360, 70, 920, 1095, 'AGGREGATOR · SATU LAYANAN')
    d.text(40, 53, 'LAYANAN SUMBER / KLIEN', 'group')
    d.text(1330, 53, 'INFRASTRUKTUR', 'group')
    d.node(40, 195, 250, 175, 'BMKG Mock', ['SeismicEvent', 'TsunamiWarning', 'X-BMKG-Key'])
    d.node(40, 455, 250, 155, 'PVMBG Mock', ['VolcanicReport', 'Bearer token PVMBG'])
    d.node(40, 740, 250, 140, 'Client-Facing API', ['Query HazardEvent', 'melalui API internal'])

    d.node(405, 195, 280, 175, 'Poller BMKG', ['Timeout HTTP: 2 detik', 'Fetch event lalu warning', 'Dua cursor terpisah'], accent=True)
    d.node(780, 195, 350, 175, 'Korelasi + MapSeismic', ['Cache event / warning', 'Korelasi melalui event_id', 'HazardEvent seismik'])
    d.node(405, 455, 280, 155, 'Poller PVMBG', ['Timeout HTTP: 4 detik', 'Cursor laporan'], accent=True)
    d.node(780, 455, 350, 185, 'MapVolcanic', ['Referensi volcano_id', 'Field tambahan → attributes', 'confidence_level opsional', 'HazardEvent vulkanik'])
    d.node(405, 740, 280, 140, 'API internal', ['GET /internal/v1/hazards', 'Data + status sumber'])
    d.node(780, 740, 350, 140, 'Repository', ['Upsert / List', 'Identitas hazard deterministik'])
    d.node(780, 1000, 350, 130, 'Worker outbox', ['Ambil pesan pending', 'Publish + publisher confirm'])
    d.node(1330, 740, 290, 170, 'Canonical Store', ['PostgreSQL', 'hazard_events + JSONB', 'hazard_outbox'])
    d.node(1330, 1000, 290, 130, 'RabbitMQ', ['Exchange hazard.events', 'Fanout ke dua queue'])

    d.edge([(405, 280), (290, 280)], blue=True)
    d.label(347, 246, 'HTTP')
    d.edge([(685, 280), (780, 280)])
    d.label(733, 249, 'Data')
    d.edge([(405, 532), (290, 532)], blue=True)
    d.label(347, 498, 'HTTP')
    d.edge([(685, 532), (780, 532)])
    d.label(733, 500, 'Data')
    d.edge([(1130, 280), (1205, 280), (1205, 780), (1130, 780)])
    d.label(1182, 706, 'HazardEvent')
    d.edge([(955, 640), (955, 740)])
    d.label(955, 696, 'HazardEvent')
    d.edge([(290, 810), (405, 810)], blue=True)
    d.label(347, 777, 'HTTP')
    d.edge([(685, 810), (780, 810)])
    d.label(733, 778, 'List')
    d.edge([(1130, 840), (1330, 840)])
    d.label(1230, 810, ['SQL', 'baca / tulis'])
    d.text(1475, 950, ['Hazard + outbox', 'dalam satu transaksi'], 'small', 'middle')
    d.edge([(1130, 1040), (1235, 1040), (1235, 890), (1330, 890)])
    d.label(1230, 980, ['SQL', 'Baca outbox'])
    d.edge([(1130, 1095), (1330, 1095)], dashed=True)
    d.label(1230, 1133, 'AMQP')

    d.text(406, 958, ['Poller BMKG dan PVMBG', 'berjalan mandiri.'], 'small')
    d.text(406, 1050, ['Query membaca data tersimpan;', 'tidak menunggu fetch upstream.'], 'small')
    d.text(40, 1210, 'Cursor maju setelah commit. Request jaringan dilakukan di luar lock pengelolaan state.', 'small')
    d.text(40, 1243, 'Panah HTTP/SQL menunjukkan pemanggil → tujuan; panah internal menunjukkan alur data atau operasi repository.', 'small')
    d.text(40, 1276, 'Worker outbox berjalan terpisah dari polling. Garis putus-putus menunjukkan publikasi event.', 'small')
    return d


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', type=Path, default=Path('docs/diagrams'))
    parser.add_argument('--chrome', default='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome')
    args = parser.parse_args()
    args.output_dir.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='tubesaat-diagram-chrome-') as profile:
        for stem, drawing in [('bab-3-arsitektur-sistem', overview()), ('bab-3-alur-internal-aggregator', aggregator())]:
            svg = (args.output_dir / (stem + '.svg')).resolve()
            png = svg.with_suffix('.png')
            drawing.write(svg)
            command = [args.chrome, '--headless', '--disable-gpu', '--hide-scrollbars', '--force-device-scale-factor=2',
                       '--user-data-dir=' + profile, f'--window-size={drawing.width},{drawing.height}',
                       '--screenshot=' + str(png), svg.as_uri()]
            run = subprocess.run(command, capture_output=True, text=True)
            if run.returncode:
                raise SystemExit(run.stderr[-2000:])
            print(svg.relative_to(Path.cwd()))
            print(png.relative_to(Path.cwd()))


if __name__ == '__main__':
    main()
