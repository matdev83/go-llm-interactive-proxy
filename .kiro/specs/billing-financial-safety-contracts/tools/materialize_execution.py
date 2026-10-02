#!/usr/bin/env python3
"""Materialize the execution packet tree shipped with this spec-only PR.

The archive is an exact packed copy of the approved execution/ directory.
Extraction is deterministic, path-confined, and SHA-256 verified.  The generated
execution/ directory is intentionally git-ignored; it is runtime working
material, not a second source of truth.
"""
from __future__ import annotations

import hashlib
import shutil
import sys
import tarfile
from pathlib import Path

SPEC = Path(__file__).resolve().parents[1]
ARCHIVE = SPEC / "execution-packets.tar.xz"
EXPECTED_SHA256 = "e3c3831931fd49a2fd35758831ff25f199fe9a99378c8a21d89051df45df7897"
TARGET = SPEC / "execution"


def fail(message: str) -> "None":
    raise SystemExit(f"materialize_execution: {message}")


def main() -> int:
    payload = ARCHIVE.read_bytes()
    actual = hashlib.sha256(payload).hexdigest()
    if actual != EXPECTED_SHA256:
        fail(f"archive SHA-256 mismatch: expected {EXPECTED_SHA256}, got {actual}")

    if TARGET.exists():
        shutil.rmtree(TARGET)

    with tarfile.open(ARCHIVE, mode="r:xz") as tf:
        members = tf.getmembers()
        for member in members:
            p = Path(member.name)
            if not p.parts or p.parts[0] != "execution" or p.is_absolute() or ".." in p.parts:
                fail(f"unsafe archive member: {member.name!r}")
            if member.issym() or member.islnk():
                fail(f"links are forbidden in execution packet archive: {member.name!r}")
        tf.extractall(SPEC, members=members, filter="data")

    required = [TARGET / "order.md", TARGET / "task-manifest.json", TARGET / "T01.md", TARGET / "T113.md"]
    missing = [str(p.relative_to(SPEC)) for p in required if not p.is_file()]
    if missing:
        fail("materialized packet tree is incomplete: " + ", ".join(missing))

    count = sum(1 for p in TARGET.rglob("*") if p.is_file())
    print(f"materialized {count} execution files under {TARGET}")
    print(f"archive sha256 {actual}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
