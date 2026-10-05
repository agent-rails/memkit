from __future__ import annotations

import argparse
import json
import statistics
import sys
import uuid
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

from backends import MemoryBackend
from harness.models import RateMetric, RunLog, Workload, WriteLogEntry
from harness.replay import replay_workload
from harness.scoring_stats import wilson_ci
from harness.workload_loader import SYNTHETIC_ROOT, load_synthetic_workload

UPDATE_CATEGORIES = ("update_same_frame", "update_reworded")
RETENTION_CATEGORIES = ("cross_subject_same_value", "additive_other_attribute", "restatement")


def load_meta(split: str, root: Path = SYNTHETIC_ROOT) -> dict:
    path = root / f"conflict_v2_{split}.meta.json"
    if not path.exists():
        raise SystemExit(f"sidecar not found: {path}")
    return json.loads(path.read_text(encoding="utf-8"))


def _rate(metric: RateMetric) -> dict:
    return {"value": metric.value, "n": metric.n, "ci_low": metric.ci_low, "ci_high": metric.ci_high}


def _retention_hit(query_meta: dict, retrieved_ids: set[str]) -> bool:
    expected = set(query_meta["expected_ids"])
    if query_meta["match"] == "all_of":
        return expected <= retrieved_ids
    if query_meta["match"] == "any_of":
        return bool(expected & retrieved_ids)
    raise ValueError(f"unknown match semantics: {query_meta['match']}")


def _p95(values: list[float]) -> float:
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(0.95 * len(ordered)))]


def score_conflict(run_log: RunLog, workload: Workload, meta: dict) -> dict:
    if run_log.incomplete:
        raise SystemExit("run incomplete: a backend call failed; refusing to score")
    if meta["workload"] != workload.name:
        raise SystemExit(f"sidecar is for {meta['workload']!r}, workload is {workload.name!r}")

    stale_hits: dict[str, int] = {}
    recall_hits: dict[str, int] = {}
    retention_hits: dict[str, int] = {}
    totals: dict[str, int] = {}
    for index_str, query_meta in meta["queries"].items():
        entry = run_log.entries[int(index_str)]
        category = query_meta["category"]
        retrieved_ids = {r.fact_id for r in entry.retrieved}
        totals[category] = totals.get(category, 0) + 1
        if category in UPDATE_CATEGORIES:
            stale_hits[category] = stale_hits.get(category, 0) + bool(retrieved_ids & set(query_meta["stale_ids"]))
            recall_hits[category] = recall_hits.get(category, 0) + _retention_hit(query_meta, retrieved_ids)
        elif category in RETENTION_CATEGORIES:
            retention_hits[category] = retention_hits.get(category, 0) + _retention_hit(query_meta, retrieved_ids)
        else:
            raise ValueError(f"unknown category: {category}")

    categories: dict[str, dict] = {}
    for category in UPDATE_CATEGORIES:
        n = totals.get(category, 0)
        categories[category] = {
            "stale_rate": _rate(wilson_ci(stale_hits.get(category, 0), n)),
            "update_recall": _rate(wilson_ci(recall_hits.get(category, 0), n)),
        }
    for category in RETENTION_CATEGORIES:
        n = totals.get(category, 0)
        hits = retention_hits.get(category, 0)
        categories[category] = {
            "retention_rate": _rate(wilson_ci(hits, n)),
            "false_supersession_rate": _rate(wilson_ci(n - hits, n)),
        }

    write_latencies = [e.result.latency_ms for e in run_log.entries if isinstance(e, WriteLogEntry)]
    return {
        "categories": categories,
        "write_latency_ms": {
            "median": statistics.median(write_latencies) if write_latencies else None,
            "p95": _p95(write_latencies) if write_latencies else None,
            "n": len(write_latencies),
        },
    }


def _fmt(metric: dict) -> str:
    if metric["value"] is None:
        return "n/a"
    return f"{metric['value']:.3f} [{metric['ci_low']:.3f},{metric['ci_high']:.3f}] n={metric['n']}"


def format_table(result: dict) -> str:
    lines = [f"{'category':<28}{'metric':<26}value [95% wilson]"]
    for category, metrics in result["categories"].items():
        for name, metric in metrics.items():
            lines.append(f"{category:<28}{name:<26}{_fmt(metric)}")
    latency = result["write_latency_ms"]
    lines.append(f"write latency ms: median={latency['median']:.2f} p95={latency['p95']:.2f} n={latency['n']}")
    return "\n".join(lines)


def build_backend(name: str, memkit_url: str, api_key: str, user: str) -> MemoryBackend:
    if name == "memkit":
        from backends.memkit_backend import MemkitBackend

        return MemkitBackend(base_url=memkit_url, api_key=api_key, user_id=user)
    if name == "vector":
        from backends.local_vector import LocalVectorBackend

        return LocalVectorBackend()
    raise SystemExit(f"unknown backend: {name}")


def run(backend: MemoryBackend, split: str, backend_name: str, root: Path = SYNTHETIC_ROOT) -> dict:
    workload = load_synthetic_workload(f"conflict_v2_{split}", root=root)
    meta = load_meta(split, root=root)
    if backend.query("probe", at=0.0):
        raise SystemExit("backend store is not empty; refusing to reuse existing state")
    run_log = replay_workload(backend, workload, backend_name=backend_name)
    result = score_conflict(run_log, workload, meta)
    return {"split": split, "backend": backend_name, **result}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--split", choices=["dev", "test"], required=True)
    parser.add_argument("--backend", choices=["memkit", "vector"], required=True)
    parser.add_argument("--memkit-url", default="http://localhost:8080")
    parser.add_argument("--api-key", default="dev-key")
    parser.add_argument("--user", default=None)
    parser.add_argument("--json-out", type=Path, default=None)
    args = parser.parse_args()

    user = args.user if args.user is not None else f"conflict-v2-{uuid.uuid4()}"
    backend = build_backend(args.backend, args.memkit_url, args.api_key, user)
    try:
        result = run(backend, args.split, args.backend)
    finally:
        close = getattr(backend, "close", None)
        if close is not None:
            close()
    result["user"] = user
    print(format_table(result))
    if args.json_out is not None:
        args.json_out.write_text(json.dumps(result, indent=2), encoding="utf-8")


if __name__ == "__main__":
    main()
