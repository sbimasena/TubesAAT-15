#!/usr/bin/env python3
"""Print saved P1 test evidence for terminal capture; does not rerun ingestion."""
import argparse
import json
from pathlib import Path


def dump(value):
    return json.dumps(value, indent=2, ensure_ascii=False)


def columns(left, right):
    left, right = left.splitlines(), right.splitlines()
    width = max(len(line) for line in left) + 4
    for i in range(max(len(left), len(right))):
        print((left[i] if i < len(left) else '').ljust(width) + (right[i] if i < len(right) else ''))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('section', choices=['bmkg', 'pvmbg', 'runtime'])
    parser.add_argument('--input', type=Path, default=Path('docs/evidence/member-a/p1/report-check.json'))
    args = parser.parse_args()
    d = json.loads(args.input.read_text())
    print('P1 / ' + args.section.upper() + ' / hasil pengujian tersimpan')
    print('Sumber: ' + str(args.input))
    print('Run: ' + d['started_at'] + ' s.d. ' + d['finished_at'])
    print('Project: ' + d['project'] + ' | Result: ' + d['result'])
    print()
    if args.section == 'bmkg':
        b = d['bmkg']
        columns('SEISMIC EVENT BMKG\n' + dump(b['source_event']) + '\n\nTSUNAMI WARNING\n' + dump(b['source_warning']),
                'HAZARD EVENT KANONIS\n' + dump(b['hazard']))
        print('\nMapping + korelasi: ' + b['mapping_and_correlation'])
    elif args.section == 'pvmbg':
        def version(key):
            v = d[key]
            return key.upper() + ' / SUMBER\n' + dump(v['source_report']) + '\n\nHAZARD EVENT KANONIS\n' + dump(v['hazard'])
        columns(version('pvmbg_v1'), version('pvmbg_v2'))
        print('\nMapping v1: ' + d['pvmbg_v1']['mapping'] + ' | Mapping v2: ' + d['pvmbg_v2']['mapping'])
        print('confidence_level sumber = kanonis = ' + str(d['pvmbg_v2']['source_report']['confidence_level']))
    else:
        print('CONTAINER SEBELUM/SESUDAH (ID: 12 karakter awal)')
        print(f'{"Service":24} {"ID":12} {"StartedAt":35} Unchanged')
        for name, state in sorted(d['no_restart'].items()):
            print(f'{name:24} {state["container_id"][:12]:12} {state["started_at"]:35} {state["unchanged"]}')
        print('\nMIGRASI SEBELUM/SESUDAH')
        for key in ('migrations_before', 'migrations_after'):
            for row in d[key]:
                print(f'{key:18} version={row["version"]} applied_at={row["applied_at"]}')
        print('\nCORRELATION ID: ' + d['v2_correlation_id'])
        print('\nTRACE AGGREGATOR')
        for row in d['v2_trace']:
            print(row['time'], row.get('operation', row.get('msg', '')),
                  'status=' + str(row['status']) if 'status' in row else
                  'message_id=' + str(row['message_id']) if 'message_id' in row else '')
        print('\nCONSUMER')
        for name, row in d['consumers'].items():
            print(name + ':')
            print('  message_id=' + str(row['message_id']) + ' revision=' + str(row['hazard_revision']) + ' result=' + row['result'])
            print('  hazard_id=' + row['hazard_id'])
            print('  correlation_id=' + row['correlation_id'])
        print('\nResult pengujian keseluruhan: ' + d['result'])


if __name__ == '__main__':
    main()
