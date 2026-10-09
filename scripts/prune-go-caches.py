#!/usr/bin/env python3
"""Select obsolete repository-owned Go cache snapshots; never delete directly."""

import argparse
import json
import re
from collections import defaultdict

SNAPSHOT = re.compile(
    r"^go-cache-(ci|qa|backend-plugin|acp|cost)-(Linux|Windows|macOS)"
    r"-v2-([^-]+)-go[^-]+-(.+)-[0-9a-f]{64}-[0-9a-f]{40}$"
)
LEGACY = re.compile(
    r"^go-cache-(ci|qa|backend-plugin|acp)-(Linux|Windows|macOS)-[0-9a-f]{64}$"
)
PR_REF = re.compile(r"^refs/pull/(\d+)/merge$")
BUILD_V3 = re.compile(r"^go-build-v3-([a-z0-9-]+)-(Linux|Windows|macOS)-([^-]+)-go[^-]+-[0-9a-f]{64}-[0-9a-f]{40}$")
MODULE_V3 = re.compile(r"^go-mod-v3-([a-z0-9-]+)-(Linux|Windows|macOS)-[0-9a-f]{64}-[0-9a-f]{40}$")
MODULE_SHARED = re.compile(r"^go-mod-v3-shared-[0-9a-f]{64}-[0-9a-f]{40}$")
SETUP_GO = re.compile(r"^setup-go-(Linux|Windows|macOS)-(?:x64|arm64)(?:-ubuntu[0-9]+)?-go-[0-9.]+-[0-9a-f]{64}$")
DEFAULT_BYTE_BUDGET = 7 * 1024**3  # Reserve space for CodeQL, npm and other owners.


def obsolete_snapshots(caches, open_prs, byte_budget=DEFAULT_BYTE_BUDGET):
    """Keep newest snapshot per family/OS/arch/job/ref, including active PRs.

    Retire legacy dependency-only snapshots only after a replacement exists in
    the same family/OS/ref. Unknown namespaces/formats and non-Go caches survive.
    """
    groups = defaultdict(list)
    replacements = set()
    obsolete = set()
    legacy = []
    owned = []
    setup_go = []
    for cache in caches:
        key = cache["key"]
        match = SNAPSHOT.fullmatch(key)
        old = LEGACY.fullmatch(key)
        build = BUILD_V3.fullmatch(key)
        modules = MODULE_V3.fullmatch(key)
        shared_modules = MODULE_SHARED.fullmatch(key)
        setup = SETUP_GO.fullmatch(key)
        if not match and not old and not build and not modules and not shared_modules and not setup:
            continue
        owned.append(cache)
        ref = cache["ref"]
        pr = PR_REF.fullmatch(ref)
        if pr and int(pr[1]) not in open_prs:
            obsolete.add(cache["id"])
            continue
        if old:
            legacy.append((cache, (old[1], old[2], ref)))
            continue
        if setup:
            setup_go.append((cache, setup[1]))
            continue
        if shared_modules:
            groups[("modules-shared-v3", "portable", "any", "modules", ref)].append(cache)
            continue
        if build or modules:
            if build:
                lane, os_name, arch = build.groups()
                family, job = "build-v3", lane
            else:
                lane, os_name = modules.groups()
                family, arch, job = "modules-v3", "any", lane
            groups[(family, os_name, arch, job, ref)].append(cache)
            # An advancing v3 producer supersedes the old combined snapshots
            # of this workload. Never retire a v2 lane before its replacement.
            old_family = {"ci-unit": "ci", "ci-db": "ci", "cost": "cost"}.get(lane, lane)
            replacements.add((old_family, os_name, ref))
            continue
        family, os_name, arch, job = match.groups()
        replacements.add((family, os_name, ref))
        groups[(family, os_name, arch, job, ref)].append(cache)
    for entries in groups.values():
        # Creation time, not last access: a frequently restored stale snapshot
        # must not displace the newest completed build progress.
        entries.sort(key=lambda c: (c["created_at"], c["id"]), reverse=True)
        obsolete.update(c["id"] for c in entries[1:])
    obsolete.update(c["id"] for c, lane in legacy if lane in replacements)
    shared_refs = {c["ref"] for c in owned if MODULE_SHARED.fullmatch(c["key"])}
    obsolete.update(c["id"] for c in owned if MODULE_V3.fullmatch(c["key"]) and c["ref"] in shared_refs)
    # Retire superseded v2 snapshots after a v3 producer exists on the same ref.
    v3_lanes = set()
    for cache in owned:
        key_match = BUILD_V3.fullmatch(cache["key"])
        if key_match:
            lane, os_name, _ = key_match.groups()
            family = {"ci-unit": "ci", "ci-db": "ci"}.get(lane, lane)
            v3_lanes.add((family, os_name, cache["ref"]))
    # Every setup-go writer has migrated. Retire its combined archive only
    # after a bounded compiler producer exists on the same OS and ref.
    v3_os_refs = {(os_name, ref) for _, os_name, ref in v3_lanes}
    obsolete.update(c["id"] for c, os_name in setup_go if (os_name, c["ref"]) in v3_os_refs)
    for cache in owned:
        match = SNAPSHOT.fullmatch(cache["key"])
        if match and (match[1], match[2], cache["ref"]) in v3_lanes:
            obsolete.add(cache["id"])
    survivors = [c for c in owned if c["id"] not in obsolete]
    total = sum(c.get("size_in_bytes", 0) for c in survivors)
    # Main baselines serve every PR. Evict private branch caches before them,
    # then the least recently accessed main lanes if still over the byte cap.
    survivors.sort(key=lambda c: (c["ref"] == "refs/heads/main", c.get("last_accessed_at", c["created_at"]), c["id"]))
    for cache in survivors:
        if total <= byte_budget:
            break
        obsolete.add(cache["id"])
        total -= cache.get("size_in_bytes", 0)
    return sorted(obsolete)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--caches", required=True, help="gh api --paginate --slurp cache pages")
    parser.add_argument("--open-prs", required=True, help="complete gh api --paginate --slurp open PR pages")
    args = parser.parse_args()
    with open(args.caches, encoding="utf-8") as stream:
        pages = json.load(stream)
    with open(args.open_prs, encoding="utf-8") as stream:
        pr_pages = json.load(stream)
    caches = [cache for page in pages for cache in page["actions_caches"]]
    open_prs = {pr["number"] for page in pr_pages for pr in page}
    print(json.dumps(obsolete_snapshots(caches, open_prs)))


if __name__ == "__main__":
    main()
