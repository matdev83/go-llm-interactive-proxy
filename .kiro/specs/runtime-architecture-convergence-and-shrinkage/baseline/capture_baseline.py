#!/usr/bin/env python3
"""Deterministic baseline capture for runtime-architecture-convergence-and-shrinkage.

Regenerates supplemental architecture metrics and the migration concept inventory
from an explicitly supplied repository/worktree root at a reviewed SHA.

This helper is scoped to the active Kiro spec baseline. It does not modify
production architecture tooling.

Deterministic inputs
--------------------
- --repo-root: absolute path to a Git worktree checked out at --reviewed-sha
- --reviewed-sha: full Git object name (verified via `git rev-parse HEAD`)
- Repository Go sources + `go list -e -json -test=false ./...`
- Fixed concept search patterns and role heuristics defined in this file

Deterministic outputs (under --out-dir)
---------------------------------------
- supplemental-metrics.json
- migration-inventory.json

Optional stdout: Markdown tables for the human baseline document (--print-markdown).

Examples
--------
  python3 capture_baseline.py --help

  python3 capture_baseline.py \\
    --repo-root \"$BASELINE_WORKTREE\" \\
    --reviewed-sha efe4624909cea318c7211d5cb3734059d3210802 \\
    --out-dir \"$OUT_DIR\"
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any, Iterable

DEFAULT_REVIEWED_SHA = "efe4624909cea318c7211d5cb3734059d3210802"

NAMED_SURFACES = [
    "internal/infra/runtimebundle",
    "internal/infra/runtimehost",
    "internal/stdhttp",
    "cmd/lipstd",
    "pkg/lipruntime",
]

MIGRATION_HOTSPOT_FILES = [
    "internal/infra/runtimehost/coordinator.go",
    "internal/infra/runtimehost/generation.go",
    "internal/infra/runtimebundle/candidate_compile.go",
    "internal/infra/runtimebundle/compile_generation.go",
    "internal/infra/runtimebundle/process_services.go",
    "internal/infra/runtimebundle/build.go",
    "pkg/lipruntime/build.go",
    "pkg/lipruntime/normalize.go",
    "pkg/lipruntime/reload.go",
    "pkg/lipruntime/reload_map.go",
    "internal/stdhttp/request_plane.go",
    "internal/infra/runtimebundle/reload_host.go",
]

EXPORT_PACKAGES = [
    "pkg/lipapi",
    "pkg/lipsdk",
    "pkg/lipruntime",
]

# Critical-file budget paths/max values mirrored from internal/archtest/critical_files.go
# at the reviewed baseline SHA (for supplemental reporting only).
CRITICAL_FILE_BUDGETS = [
    ("internal/core/runtime/executor.go", 150),
    ("internal/infra/runtimebundle/build.go", 220),
    ("internal/infra/runtimebundle/options.go", 240),
    ("internal/standardplugins/standard_table.go", 320),
    ("internal/pluginreg/reg.go", 320),
    ("internal/stdhttp/server.go", 300),
]


def die(msg: str, code: int = 1) -> None:
    print(f"capture_baseline: {msg}", file=sys.stderr)
    raise SystemExit(code)


def run(cmd: list[str], *, cwd: Path | None = None, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        cmd,
        cwd=str(cwd) if cwd else None,
        text=True,
        capture_output=True,
        check=check,
    )


def count_file_lines(path: Path) -> int:
    """Physical newline count matching scripts/arch-report.go countFileLines."""
    n = 0
    with path.open("rb") as f:
        for _ in f:
            n += 1
    return n


def verify_sha(repo_root: Path, reviewed_sha: str) -> str:
    try:
        head = run(["git", "rev-parse", "HEAD"], cwd=repo_root).stdout.strip()
    except subprocess.CalledProcessError as e:
        die(f"git rev-parse failed in {repo_root}: {e.stderr.strip()}")
    if head != reviewed_sha:
        die(
            f"repo-root HEAD {head} does not match --reviewed-sha {reviewed_sha}. "
            "Check out the reviewed SHA (detached worktree) before capture."
        )
    return head


def module_path(repo_root: Path) -> str:
    try:
        return run(["go", "list", "-e", "-m"], cwd=repo_root).stdout.strip()
    except subprocess.CalledProcessError as e:
        die(f"go list -m failed: {e.stderr.strip()}")


def package_import_graph(repo_root: Path) -> list[dict[str, Any]]:
    try:
        proc = run(["go", "list", "-e", "-json", "-test=false", "./..."], cwd=repo_root)
    except subprocess.CalledProcessError as e:
        die(f"go list ./... failed: {e.stderr.strip()}")
    pkgs: list[dict[str, Any]] = []
    decoder = json.JSONDecoder()
    data = proc.stdout
    idx = 0
    while idx < len(data):
        while idx < len(data) and data[idx].isspace():
            idx += 1
        if idx >= len(data):
            break
        obj, end = decoder.raw_decode(data, idx)
        pkgs.append(obj)
        idx = end
    return pkgs


def rel_import(mod: str, import_path: str) -> str:
    prefix = mod + "/"
    if import_path.startswith(prefix):
        return import_path[len(prefix) :]
    return import_path


def package_non_test_lines(pkg: dict[str, Any]) -> int:
    dir_path = Path(pkg["Dir"])
    total = 0
    for name in pkg.get("GoFiles") or []:
        total += count_file_lines(dir_path / name)
    return total


def internal_fan_out(pkg: dict[str, Any], mod: str) -> int:
    prefix = mod + "/internal/"
    return sum(1 for imp in (pkg.get("Imports") or []) if imp.startswith(prefix))


def build_metrics(repo_root: Path, reviewed_sha: str) -> dict[str, Any]:
    mod = module_path(repo_root)
    pkgs = package_import_graph(repo_root)
    by_rel: dict[str, dict[str, Any]] = {}
    for p in pkgs:
        ip = p.get("ImportPath") or ""
        if not ip.startswith(mod + "/") and ip != mod:
            continue
        rel = rel_import(mod, ip)
        by_rel[rel] = p

    # Fan-in: production packages whose Imports include the target.
    importers: dict[str, list[str]] = defaultdict(list)
    internal_prefix = mod + "/internal/"
    for p in pkgs:
        ip = p.get("ImportPath") or ""
        rel_src = rel_import(mod, ip)
        for imp in p.get("Imports") or []:
            if imp.startswith(mod + "/"):
                importers[rel_import(mod, imp)].append(rel_src)

    named: dict[str, Any] = {}
    named_sum = 0
    for rel in NAMED_SURFACES:
        p = by_rel.get(rel)
        if p is None:
            die(f"named surface package missing from go list: {rel}")
        lines = package_non_test_lines(p)
        fan_out = internal_fan_out(p, mod)
        fan_in_list = sorted(set(importers.get(rel, [])))
        # Prefer short importer names for readability; keep deterministic order.
        named[rel] = {
            "non_test_lines": lines,
            "fan_out_internal": fan_out,
            "fan_in": len(fan_in_list),
            "importers": fan_in_list,
        }
        named_sum += lines

    # stdhttp recursive: root + all subpackages.
    stdhttp_subs: dict[str, int] = {}
    stdhttp_recursive = 0
    for rel, p in sorted(by_rel.items()):
        if rel == "internal/stdhttp" or rel.startswith("internal/stdhttp/"):
            n = package_non_test_lines(p)
            stdhttp_subs[rel] = n
            stdhttp_recursive += n

    hotspots: dict[str, int] = {}
    for rel in MIGRATION_HOTSPOT_FILES:
        path = repo_root / rel
        if not path.is_file():
            die(f"migration hotspot file missing: {rel}")
        hotspots[rel] = count_file_lines(path)

    critical: list[dict[str, Any]] = []
    for rel, budget in CRITICAL_FILE_BUDGETS:
        path = repo_root / rel
        critical.append(
            {
                "file": rel,
                "lines": count_file_lines(path) if path.is_file() else None,
                "budget": budget,
            }
        )

    exports = count_exports(repo_root, EXPORT_PACKAGES)

    return {
        "reviewed_sha": reviewed_sha,
        "generator": "baseline/capture_baseline.py",
        "module": mod,
        "method": {
            "lines": "go list GoFiles + physical newline count (scripts/arch-report.go countFileLines)",
            "fan_out": "count of direct Imports under module/internal/",
            "fan_in": "count of production packages (go list -test=false) whose Imports include the target",
            "exports": "go/ast exported type/value/func decls in non-test .go files (scripts/arch-report.go exportedSymbols)",
        },
        "named_surface_packages": named,
        "named_surface_sum_root_stdhttp": named_sum,
        "stdhttp_recursive_non_test_lines": stdhttp_recursive,
        "stdhttp_packages": stdhttp_subs,
        "critical_hotspot_files": critical,
        "migration_hotspot_files": hotspots,
        "exported_symbols": exports,
    }


def count_exports(repo_root: Path, rel_dirs: list[str]) -> dict[str, int]:
    helper = Path(__file__).resolve().parent / "count_exports.go"
    if not helper.is_file():
        die(f"missing export helper: {helper}")
    abs_dirs = [str(repo_root / d) for d in rel_dirs]
    try:
        proc = run(["go", "run", str(helper), *abs_dirs], cwd=repo_root)
    except subprocess.CalledProcessError as e:
        die(f"count_exports failed: {e.stderr.strip()}")
    raw = json.loads(proc.stdout)
    # Remap absolute keys back to repo-relative paths.
    out: dict[str, int] = {}
    for rel, abs_dir in zip(rel_dirs, abs_dirs):
        if abs_dir in raw:
            out[rel] = raw[abs_dir]
        elif rel in raw:
            out[rel] = raw[rel]
        else:
            die(f"export count missing for {rel}; got keys {sorted(raw)}")
    return out


# ---------------------------------------------------------------------------
# Migration inventory
# ---------------------------------------------------------------------------


def classify_file(path: str) -> str:
    s = path.replace("\\", "/")
    if s.startswith("testdata/"):
        return "sample_external_module"
    if "internal/archtest/" in s or s.startswith("internal/archtest/"):
        return "architecture_check"
    if s.endswith("_test.go") or "/testdata/" in s:
        return "test_only"
    if s.endswith(".go"):
        return "production"
    if s.endswith(".md"):
        return "documentation"
    return "other"


def is_comment_line(text: str) -> bool:
    t = text.lstrip()
    return t.startswith("//") or t.startswith("*") or t.startswith("/*")


def iter_go_files(repo_root: Path) -> Iterable[Path]:
    skip_dirs = {".git", "vendor", "node_modules", ".venv", "bin"}
    for dirpath, dirnames, filenames in os.walk(repo_root):
        dirnames[:] = [d for d in dirnames if d not in skip_dirs and not d.startswith(".")]
        # Keep walking .kiro out; we only inventory *.go sources.
        for name in filenames:
            if name.endswith(".go"):
                yield Path(dirpath) / name


def package_clause(path: Path) -> str | None:
    try:
        with path.open("r", encoding="utf-8", errors="replace") as f:
            for line in f:
                s = line.strip()
                if s.startswith("package "):
                    return s.split()[1]
    except OSError:
        return None
    return None


def scan_matches(repo_root: Path, pattern: re.Pattern[str], *, path_filter=None) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for path in iter_go_files(repo_root):
        rel = path.relative_to(repo_root).as_posix()
        if path_filter is not None and not path_filter(rel):
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        for i, line in enumerate(text.splitlines(), start=1):
            if pattern.search(line):
                rows.append(
                    {
                        "path": rel,
                        "line": i,
                        "text": line.rstrip(),
                        "file_class": classify_file(rel),
                        "is_comment": is_comment_line(line),
                    }
                )
    return rows


def dedup_rows(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    seen: set[tuple[str, int, str]] = set()
    out: list[dict[str, Any]] = []
    for r in rows:
        key = (r["path"], r["line"], r["text"])
        if key in seen:
            continue
        seen.add(key)
        out.append(r)
    return out


def clean_built_rows(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    cleaned: list[dict[str, Any]] = []
    for r in rows:
        tmp = re.sub(r"Built-in", "", r["text"], flags=re.I)
        tmp = re.sub(r"built-in", "", tmp, flags=re.I)
        if re.search(r"\bBuilt\b", tmp):
            cleaned.append(r)
    return cleaned


def role_for(concept: str, row: dict[str, Any]) -> str:
    t = row["text"]
    path = row["path"]
    if row["is_comment"]:
        return "comment_mention"

    if concept == "Built":
        if re.search(r"type Built struct", t):
            return "declaration"
        if "requestPlaneAsBuilt" in t or re.search(r"func requestPlaneAsBuilt", t):
            return "compatibility_adapter"
        if re.search(r"\*runtimebundle\.Built|\*Built\b|Built\s+\*runtimebundle\.Built|Built\s+\*Built", t):
            return "type_usage"
        if re.search(r"\bBuilt:", t) or re.search(r"\.Built\b", t):
            return "field_usage"
        if re.search(r"&Built\{|&runtimebundle\.Built\{", t):
            return "construction"
        return "code_reference"

    if concept == "Build_compat":
        if path.endswith("runtimebundle/build.go") and "func Build(" in t:
            return "declaration_runtimebundle"
        if path.endswith("lipruntime/build.go") and "func Build(" in t:
            return "declaration_lipruntime"
        if "lipruntime.Build(" in t:
            return "caller_lipruntime"
        # runtimebundle.Build( or same-package Build( call (not func declaration).
        is_rb_call = "runtimebundle.Build(" in t
        is_same_pkg_call = (
            path.startswith("internal/infra/runtimebundle/")
            and re.search(r"(^|[^\w.])Build\(", t) is not None
            and re.search(r"func\s+Build\(", t) is None
        )
        if is_rb_call or is_same_pkg_call:
            if row["file_class"] == "production":
                return "production_caller"
            # Neutral role for test / sample / architecture call sites.
            return "caller"
        return "code_reference"

    if concept == "RunWithRuntime":
        if "func RunWithRuntime" in t:
            return "declaration"
        if "RunWithRuntime(" in t:
            return "caller"
        return "code_reference"

    if concept == "requestPlaneAsBuilt":
        if "func requestPlaneAsBuilt" in t:
            return "declaration"
        if "requestPlaneAsBuilt(" in t:
            return "caller"
        return "code_reference"

    if concept == "AttachReloadHost":
        if "func AttachReloadHost" in t:
            return "declaration"
        if "AttachReloadHost(" in t:
            return "caller"
        return "code_reference"

    if concept == "RequestPlane":
        if re.search(r"type RequestPlane\b", t) or "type RequestPlane interface" in t or "type RequestPlane struct" in t:
            return "declaration"
        if "ComposeRequestPlane" in t:
            return "composer"
        return "code_reference"

    # reload_contract / deprecated_Options: code_reference unless comment.
    return "code_reference"


def collect_concepts(repo_root: Path) -> dict[str, list[dict[str, Any]]]:
    concepts: dict[str, list[dict[str, Any]]] = {}

    concepts["Built"] = clean_built_rows(scan_matches(repo_root, re.compile(r"\bBuilt\b")))
    concepts["RunWithRuntime"] = scan_matches(repo_root, re.compile(r"\bRunWithRuntime\b"))
    concepts["RequestPlane"] = scan_matches(repo_root, re.compile(r"\bRequestPlane\b"))
    concepts["requestPlaneAsBuilt"] = scan_matches(repo_root, re.compile(r"\brequestPlaneAsBuilt\b"))
    concepts["AttachReloadHost"] = scan_matches(repo_root, re.compile(r"\bAttachReloadHost\b"))

    build_rows: list[dict[str, Any]] = []
    build_rows += scan_matches(
        repo_root,
        re.compile(r"func Build\("),
        path_filter=lambda p: p in {
            "internal/infra/runtimebundle/build.go",
            "pkg/lipruntime/build.go",
        },
    )
    build_rows += scan_matches(repo_root, re.compile(r"runtimebundle\.Build\("))
    build_rows += scan_matches(repo_root, re.compile(r"lipruntime\.Build\("))
    # Same-package Build( call sites inside runtimebundle (production + package tests).
    same_pkg = scan_matches(
        repo_root,
        re.compile(r"(^|[^\w.])Build\("),
        path_filter=lambda p: p.startswith("internal/infra/runtimebundle/") and p.endswith(".go"),
    )
    for r in same_pkg:
        if re.search(r"func\s+Build\(", r["text"]):
            continue
        # Only package runtimebundle (not runtimebundle_test, which uses runtimebundle.Build).
        pkg = package_clause(repo_root / r["path"])
        if pkg == "runtimebundle":
            build_rows.append(r)
    concepts["Build_compat"] = dedup_rows(build_rows)

    # Duplicate reload contract declarations/mappings: closed vocabulary types in
    # configreload + lipruntime, mapping funcs in reload_map.go, and mirrored consts.
    reload: list[dict[str, Any]] = []
    reload += scan_matches(
        repo_root,
        re.compile(
            r"type (TriggerKind|ReloadTrigger|ResultCategory|ReloadResult|ReloadStatus|HistoryEntry) "
        ),
    )
    reload += scan_matches(
        repo_root,
        re.compile(r"^func map(Trigger|Category|Result|History|Status)"),
        path_filter=lambda p: p == "pkg/lipruntime/reload_map.go",
    )
    reload += scan_matches(
        repo_root,
        re.compile(r"TriggerAPI|TriggerSIGHUP|ResultPublished|ResultNoop"),
        path_filter=lambda p: p in {
            "internal/core/configreload/model.go",
            "pkg/lipruntime/reload.go",
        },
    )
    concepts["reload_contract"] = dedup_rows(reload)

    dep: list[dict[str, Any]] = []
    dep += scan_matches(
        repo_root,
        re.compile(r"RequestProviders|AttemptProviders|ConcurrencyProvider|ProviderDescriptors|\bRater\b"),
        path_filter=lambda p: p.startswith("pkg/lipruntime/") and p.endswith(".go"),
    )
    dep += scan_matches(
        repo_root,
        re.compile(r"func legacy|func normalize"),
        path_filter=lambda p: p == "pkg/lipruntime/normalize.go",
    )
    concepts["deprecated_Options"] = dedup_rows(dep)

    for name, rows in concepts.items():
        for r in rows:
            r["role"] = role_for(name, r)

    return concepts


def summarize_inventory(concepts: dict[str, list[dict[str, Any]]]) -> dict[str, Any]:
    summary: dict[str, Any] = {}
    for name in [
        "Built",
        "Build_compat",
        "RunWithRuntime",
        "RequestPlane",
        "requestPlaneAsBuilt",
        "AttachReloadHost",
        "reload_contract",
        "deprecated_Options",
    ]:
        rows = concepts[name]
        files: dict[str, dict[str, Any]] = {}
        for r in rows:
            meta = files.setdefault(
                r["path"],
                {"file_class": r["file_class"], "roles": set(), "lines": []},
            )
            meta["roles"].add(r["role"])
            meta["lines"].append(
                {
                    "line": r["line"],
                    "role": r["role"],
                    "is_comment": r["is_comment"],
                }
            )
        file_out = {}
        for path in sorted(files):
            meta = files[path]
            lines = sorted(meta["lines"], key=lambda x: (x["line"], x["role"]))
            file_out[path] = {
                "file_class": meta["file_class"],
                "roles": sorted(meta["roles"]),
                "line_count": len(lines),
                "lines": lines,
            }
        summary[name] = {
            "hit_count": len(rows),
            "file_count": len(file_out),
            "files": file_out,
        }
    return summary


def build_inventory(repo_root: Path, reviewed_sha: str) -> dict[str, Any]:
    concepts = collect_concepts(repo_root)
    return {
        "reviewed_sha": reviewed_sha,
        "generator": "baseline/capture_baseline.py",
        "scan_method": (
            "deterministic Python walk of *.go under --repo-root; "
            "fixed identifier patterns; roles assigned by line heuristics in capture_baseline.py; "
            "comments classified separately; production_caller reserved for production file_class only"
        ),
        "summary": summarize_inventory(concepts),
    }


def write_json(path: Path, obj: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(obj, indent=2, sort_keys=False) + "\n", encoding="utf-8")


def print_markdown(metrics: dict[str, Any], inventory: dict[str, Any]) -> None:
    print("## Named surface packages")
    print()
    print("| Package | Non-test lines | Fan-out | Fan-in |")
    print("| --- | ---: | ---: | ---: |")
    for rel, meta in metrics["named_surface_packages"].items():
        print(
            f"| `{rel}` | {meta['non_test_lines']} | {meta['fan_out_internal']} | {meta['fan_in']} |"
        )
    print()
    print(f"Named-surface sum: **{metrics['named_surface_sum_root_stdhttp']}**")
    print(f"stdhttp recursive: **{metrics['stdhttp_recursive_non_test_lines']}**")
    print()
    print("## Migration hotspot files")
    print()
    print("| File | Lines |")
    print("| --- | ---: |")
    for rel, n in metrics["migration_hotspot_files"].items():
        print(f"| `{rel}` | {n} |")
    print()
    print("## Exported symbols")
    print()
    print("| Package | Exported symbols |")
    print("| --- | ---: |")
    for rel, n in metrics["exported_symbols"].items():
        print(f"| `{rel}` | {n} |")
    print()
    print("## Inventory summary")
    print()
    print("| Concept | Hits | Files | Production | Test | Arch | Other |")
    print("| --- | ---: | ---: | ---: | ---: | ---: | ---: |")
    for concept, s in inventory["summary"].items():
        classes = defaultdict(int)
        for meta in s["files"].values():
            classes[meta["file_class"]] += 1
        other = s["file_count"] - classes["production"] - classes["test_only"] - classes["architecture_check"]
        print(
            f"| `{concept}` | {s['hit_count']} | {s['file_count']} | "
            f"{classes['production']} | {classes['test_only']} | "
            f"{classes['architecture_check']} | {other} |"
        )


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    p = argparse.ArgumentParser(
        prog="capture_baseline.py",
        description=(
            "Regenerate supplemental architecture metrics and migration inventory "
            "for runtime-architecture-convergence-and-shrinkage Task 1.1."
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    p.add_argument(
        "--repo-root",
        required=True,
        type=Path,
        help="Absolute path to the Git worktree checked out at --reviewed-sha",
    )
    p.add_argument(
        "--reviewed-sha",
        default=DEFAULT_REVIEWED_SHA,
        help=f"Expected HEAD SHA (default: {DEFAULT_REVIEWED_SHA})",
    )
    p.add_argument(
        "--out-dir",
        required=True,
        type=Path,
        help="Directory that receives supplemental-metrics.json and migration-inventory.json",
    )
    p.add_argument(
        "--emit",
        choices=("all", "metrics", "inventory"),
        default="all",
        help="Which artifacts to write (default: all)",
    )
    p.add_argument(
        "--print-markdown",
        action="store_true",
        help="Print Markdown tables for the human baseline document to stdout",
    )
    p.add_argument(
        "--skip-sha-check",
        action="store_true",
        help="Skip HEAD==reviewed-sha verification (not recommended)",
    )
    return p.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    repo_root = args.repo_root.resolve()
    out_dir = args.out_dir.resolve()
    if not repo_root.is_dir():
        die(f"--repo-root is not a directory: {repo_root}")
    if not (repo_root / "go.mod").is_file():
        die(f"--repo-root missing go.mod: {repo_root}")

    reviewed = args.reviewed_sha
    if args.skip_sha_check:
        print(f"capture_baseline: WARNING skipping SHA check; recording {reviewed}", file=sys.stderr)
    else:
        verify_sha(repo_root, reviewed)

    metrics = None
    inventory = None
    if args.emit in ("all", "metrics"):
        metrics = build_metrics(repo_root, reviewed)
        write_json(out_dir / "supplemental-metrics.json", metrics)
        print(f"wrote {out_dir / 'supplemental-metrics.json'}", file=sys.stderr)
    if args.emit in ("all", "inventory"):
        inventory = build_inventory(repo_root, reviewed)
        write_json(out_dir / "migration-inventory.json", inventory)
        print(f"wrote {out_dir / 'migration-inventory.json'}", file=sys.stderr)

    if args.print_markdown:
        if metrics is None:
            metrics = json.loads((out_dir / "supplemental-metrics.json").read_text())
        if inventory is None:
            inventory = json.loads((out_dir / "migration-inventory.json").read_text())
        print_markdown(metrics, inventory)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
