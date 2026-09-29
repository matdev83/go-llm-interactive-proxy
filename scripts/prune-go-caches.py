#!/usr/bin/env python3
"""Select obsolete repository-owned Go cache snapshots; never delete directly."""

import argparse
import json
import re
from collections import defaultdict

SNAPSHOT = re.compile(
    r"^go-cache-(ci|qa|backend-plugin|acp|cursorsdk|cost)-(Linux|Windows|macOS)"
    r"-v2-([^-]+)-go[^-]+-(.+)-[0-9a-f]{64}-[0-9a-f]{40}$"
)
LEGACY = re.compile(
    r"^go-cache-(ci|qa|backend-plugin|acp|cursorsdk)-(Linux|Windows|macOS)-[0-9a-f]{64}$"
)
PR_REF = re.compile(r"^refs/pull/(\d+)/merge$")


def obsolete_snapshots(caches, open_prs):
    """Keep newest snapshot per family/OS/arch/job/ref, including active PRs.

    Retire legacy dependency-only snapshots only after a replacement exists in
    the same family/OS/ref. Unknown namespaces/formats and non-Go caches survive.
    """
    groups = defaultdict(list)
    replacements = set()
    obsolete = set()
    legacy = []
    for cache in caches:
        key = cache["key"]
        match = SNAPSHOT.fullmatch(key)
        old = LEGACY.fullmatch(key)
        if not match and not old:
            continue
        ref = cache["ref"]
        pr = PR_REF.fullmatch(ref)
        if pr and int(pr[1]) not in open_prs:
            obsolete.add(cache["id"])
            continue
        if old:
            legacy.append((cache, (old[1], old[2], ref)))
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
