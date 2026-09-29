"""Offline retention tests; no GitHub credentials or deletions."""
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("prune", Path(__file__).with_name("prune-go-caches.py"))
prune = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prune)


def snapshot(number, job="test", ref="refs/heads/main", version="go1.26.6"):
    return {
        "id": number,
        "key": f"go-cache-ci-Linux-v2-X64-{version}-{job}-{'a' * 64}-{'b' * 40}",
        "ref": ref,
        "created_at": f"2026-09-{number:02d}T00:00:00Z",
    }


class RetentionTests(unittest.TestCase):
    def test_keep_newest_per_job_and_ref_across_toolchain_updates(self):
        caches = [snapshot(1), snapshot(2, version="go1.26.7"), snapshot(3, job="db-parity"), snapshot(4, ref="refs/pull/10/merge")]
        self.assertEqual(prune.obsolete_snapshots(caches, {10}), [1])

    def test_closed_pr_only_go_namespaces(self):
        unrelated = snapshot(2, ref="refs/pull/11/merge")
        unrelated["key"] = "node-cache-important"
        self.assertEqual(prune.obsolete_snapshots([snapshot(1, ref="refs/pull/11/merge"), unrelated], set()), [1])

    def test_legacy_survives_until_same_ref_replacement(self):
        legacy = snapshot(1)
        legacy["key"] = "go-cache-ci-Linux-" + "a" * 64
        self.assertEqual(prune.obsolete_snapshots([legacy], set()), [])
        self.assertEqual(prune.obsolete_snapshots([legacy, snapshot(2, ref="refs/pull/10/merge")], {10}), [])
        self.assertEqual(prune.obsolete_snapshots([legacy, snapshot(3)], set()), [1])

    def test_unknown_key_is_never_selected(self):
        cache = snapshot(1)
        cache["key"] += "-custom"
        self.assertEqual(prune.obsolete_snapshots([cache], set()), [])


if __name__ == "__main__":
    unittest.main()
