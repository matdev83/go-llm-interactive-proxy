#!/usr/bin/env python3
"""Enumerate specification coverage obligations. This does not execute the proxy."""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path
import sys


def load_universe(spec: Path) -> dict:
    return json.loads((spec / 'coverage/universe.json').read_text(encoding='utf-8'))


def canonical_digest(value: dict) -> str:
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def coordinates(universe: dict, frontend: str | None = None, backend: str | None = None):
    """Yield the complete mask product BEFORE native capability filtering."""
    frontends = [f['id'] for f in universe['frontends']]
    backends = [b['id'] for b in universe['backends']]
    if frontend is not None and frontend not in frontends:
        raise ValueError(f'Unknown frontend: {frontend}')
    if backend is not None and backend not in backends:
        raise ValueError(f'Unknown backend: {backend}')
    for f in sorted(frontends):
        if frontend is not None and f != frontend:
            continue
        for b in sorted(backends):
            if backend is not None and b != backend:
                continue
            for i in universe['base_input_masks']:
                for o in universe['base_output_masks']:
                    yield {'coordinate_id': f'{f}|{b}|in={i:02x}|out={o:02x}',
                           'frontend': f, 'backend': b, 'input_mask': i, 'output_mask': o,
                           'state': 'NOT_RUN',
                           'expansion': 'REQUIRED_REAL_PROFILE_OPERATION_CARRIER_MODE_EXPANSION'}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--spec', type=Path, default=Path(__file__).resolve().parents[1])
    parser.add_argument('--summary', action='store_true')
    parser.add_argument('--frontend')
    parser.add_argument('--backend')
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    try:
        u = load_universe(args.spec)
        nf, nb = len(u['frontends']), len(u['backends'])
        summary = {'kind': 'SPECIFICATION_OBLIGATIONS_ONLY', 'frontends': nf, 'backends': nb,
                   'interface_pairs': nf * nb,
                   'base_signature_obligations': nf * nb * len(u['base_input_masks']) * len(u['base_output_masks']),
                   'universe_digest': canonical_digest(u), 'implementation_verified': False,
                   'extra_expansion': 'Every real profile, operation, physical carrier and independent delivery/transport mode remains required.'}
        if args.summary or args.output is None:
            print(json.dumps(summary, indent=2))
            return 0
        # Explicit path, never an accidental huge stdout stream.
        if args.output.exists():
            raise ValueError('Refusing to overwrite an existing output file.')
        args.output.parent.mkdir(parents=True, exist_ok=True)
        count = 0
        digest = hashlib.sha256()
        with args.output.open('w', encoding='utf-8', newline='\n') as out:
            for row in coordinates(u, args.frontend, args.backend):
                line = json.dumps(row, sort_keys=True, separators=(',', ':')) + '\n'
                out.write(line)
                digest.update(line.encode())
                count += 1
        print(json.dumps({'rows': count, 'sha256': digest.hexdigest(),
                          'output': str(args.output.resolve()), 'all_rows': 'NOT_RUN',
                          'implementation_verified': False}, indent=2))
        return 0
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print(f'Coverage obligation generation failed: {exc}', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
