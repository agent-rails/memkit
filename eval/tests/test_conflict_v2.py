import importlib.util
import sys
from pathlib import Path

import pytest

from harness.models import RetrievedFact, WriteResult
from harness.replay import replay_workload
from harness.workload_loader import load_synthetic_workload
from scripts.run_conflict_v2 import UPDATE_CATEGORIES, format_table, run, score_conflict

SYNTHETIC_DIR = Path(__file__).parent.parent / "data" / "synthetic"
GEN_PATH = SYNTHETIC_DIR / "generate_conflict_v2.py"


def _load_generator():
    spec = importlib.util.spec_from_file_location("generate_conflict_v2", SYNTHETIC_DIR / "generate_conflict_v2.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules["generate_conflict_v2"] = module
    spec.loader.exec_module(module)
    return module


gen = _load_generator()


@pytest.fixture(scope="module")
def built():
    return {split: gen.build_split(split) for split in ("dev", "test")}


class PerfectBackend:
    def __init__(self, gold_by_query):
        self._gold = gold_by_query

    def write(self, fact_id, text, at):
        return WriteResult(ok=True, latency_ms=1.0, token_count=1)

    def query(self, text, at):
        return [RetrievedFact(fact_id=i, text="") for i in self._gold.get(text, [])]

    def storage_bytes(self):
        return 0


class OverwriteOnAttributeWordBackend:
    KEYWORDS = ("works", "lives", "drink", "editor", "project", "manager")

    def __init__(self):
        self._facts = []

    def write(self, fact_id, text, at):
        key = next((k for k in self.KEYWORDS if k in text), None)
        if key is not None:
            self._facts = [(i, t) for i, t in self._facts if key not in t]
        self._facts.append((fact_id, text))
        return WriteResult(ok=True, latency_ms=1.0, token_count=1)

    def query(self, text, at):
        words = {w.strip("?.,").lower() for w in text.split()}
        scored = [(len(words & {w.lower() for w in t.split()}), i, t) for i, t in self._facts]
        scored = [s for s in scored if s[0] > 0]
        scored.sort(key=lambda s: -s[0])
        return [RetrievedFact(fact_id=i, text=t) for _, i, t in scored[:5]]

    def storage_bytes(self):
        return 0


def _gold_by_query(workload, meta):
    return {workload.events[int(i)].query_text: q["expected_ids"] for i, q in meta["queries"].items()}


@pytest.mark.parametrize("split", ["dev", "test"])
def test_generator_is_deterministic_across_processes(split):
    import hashlib
    import json
    import os
    import subprocess
    import sys

    code = (
        "import hashlib, importlib.util, json, sys;"
        f"spec = importlib.util.spec_from_file_location('g', {str(GEN_PATH)!r});"
        "m = importlib.util.module_from_spec(spec); sys.modules['g'] = m; spec.loader.exec_module(m);"
        f"print(hashlib.sha256(json.dumps(m.build_split({split!r}), sort_keys=True).encode()).hexdigest())"
    )
    digests = set()
    for seed in ("1", "2", "random"):
        env = {**os.environ, "PYTHONHASHSEED": seed}
        out = subprocess.run([sys.executable, "-c", code], env=env, capture_output=True, text=True, check=True)
        digests.add(out.stdout.strip())
    committed = (
        json.loads((SYNTHETIC_DIR / f"conflict_v2_{split}.json").read_text()),
        json.loads((SYNTHETIC_DIR / f"conflict_v2_{split}.meta.json").read_text()),
    )
    committed_digest = hashlib.sha256(json.dumps(list(committed), sort_keys=True).encode()).hexdigest()
    assert len(digests) == 1, "output depends on hash seed or process state"
    assert digests == {committed_digest}, "committed files differ from a fresh generation"


def test_dev_and_test_differ():
    assert gen.build_split("dev")[0] != gen.build_split("test")[0]


@pytest.mark.parametrize("split", ["dev", "test"])
def test_committed_files_match_generator(split, built):
    import json

    workload, meta = built[split]
    assert json.loads((SYNTHETIC_DIR / f"conflict_v2_{split}.json").read_text()) == workload
    assert json.loads((SYNTHETIC_DIR / f"conflict_v2_{split}.meta.json").read_text()) == meta


@pytest.mark.parametrize("split", ["dev", "test"])
def test_workload_loads_through_loader(split, built):
    workload = load_synthetic_workload(f"conflict_v2_{split}")
    assert workload.kind == "synthetic"
    assert len(workload.events) == len(built[split][0]["events"])
    for index, q in built[split][1]["queries"].items():
        event = workload.events[int(index)]
        assert event.kind == "query"
        assert event.gold.exact_ids == frozenset(q["expected_ids"])


@pytest.mark.parametrize("split", ["dev", "test"])
def test_no_duplicate_fact_text_or_ids(split, built):
    writes = [e for e in built[split][0]["events"] if e["kind"] == "write"]
    assert len({e["fact_text"] for e in writes}) == len(writes)
    assert len({e["fact_id"] for e in writes}) == len(writes)


@pytest.mark.parametrize("split", ["dev", "test"])
def test_per_category_counts_at_least_100(split, built):
    counts = gen.category_counts(built[split][1])
    assert set(counts) == set(gen.CATEGORIES)
    assert all(n >= 100 for n in counts.values()), counts


@pytest.mark.parametrize("split", ["dev", "test"])
def test_writes_precede_their_queries(split, built):
    workload, meta = built[split]
    write_index = {e["fact_id"]: i for i, e in enumerate(workload["events"]) if e["kind"] == "write"}
    for index, q in meta["queries"].items():
        for fact_id in q["expected_ids"] + q["stale_ids"]:
            assert write_index[fact_id] < int(index)
        if q["stale_ids"]:
            assert write_index[q["stale_ids"][0]] < write_index[q["expected_ids"][0]]


@pytest.mark.parametrize("split", ["dev", "test"])
def test_scenarios_are_interleaved(split, built):
    events = built[split][0]["events"]
    adjacent_same_scenario = sum(
        1
        for a, b in zip(events, events[1:], strict=False)
        if a["kind"] == "write"
        and b["kind"] == "write"
        and a["fact_id"].rsplit("_", 1)[0] == b["fact_id"].rsplit("_", 1)[0]
    )
    assert adjacent_same_scenario < len(events) * 0.1


def test_cross_subject_pairs_share_surname_and_value(built):
    workload, _ = built["dev"]
    texts = {e["fact_id"]: e["fact_text"] for e in workload["events"] if e["kind"] == "write"}
    cross_a = [i for i in texts if "_cross_" in i and i.endswith("_a")]
    assert len(cross_a) >= 50
    for a in cross_a:
        text_a, text_b = texts[a], texts[a[:-1] + "b"]
        assert text_a != text_b
        assert any(n_a.split()[-1] == n_b.split()[-1] for n_a, n_b in [(_subject(text_a), _subject(text_b))])


def _subject(text):
    words = text.split()
    for i in range(len(words) - 1):
        if words[i][0].isupper() and words[i + 1][0].isupper() and words[i] not in ("The", "These", "At", "A"):
            return f"{words[i]} {words[i + 1]}".replace("'s", "")
    raise AssertionError(text)


def test_split_pools_are_disjoint():
    gen.check_spec_hygiene()
    dev, test = gen.SPECS["dev"], gen.SPECS["test"]
    assert not {f.name for f in dev.families} & {f.name for f in test.families}
    assert not set(dev.first_names) & set(test.first_names)
    assert not set(dev.last_names) & set(test.last_names)
    assert not gen._all_values(dev) & gen._all_values(test)
    assert not gen._templates(dev) & gen._templates(test)
    assert len(dev.families) == len(test.families) == 6


def test_generated_subjects_and_values_are_disjoint(built):
    dev_names = set(gen.SPECS["dev"].first_names) | set(gen.SPECS["dev"].last_names)
    test_text = " ".join(e["fact_text"] for e in built["test"][0]["events"] if e["kind"] == "write")
    assert not any(n in test_text.split() for n in dev_names)


def test_test_split_has_value_before_subject_frames():
    value_first = [f for f in gen.SPECS["test"].families if f.frame.index("{v}") < f.frame.index("{s}")]
    assert len(value_first) >= 2


def test_every_family_has_three_rewordings():
    for spec in gen.SPECS.values():
        assert all(len(f.rewordings) >= 3 for f in spec.families)


def test_refuses_duplicate_fact_text():
    workload = {
        "name": "x",
        "events": [
            {"kind": "write", "fact_text": "same"},
            {"kind": "write", "fact_text": "same"},
        ],
    }
    with pytest.raises(SystemExit):
        gen.check_no_duplicate_fact_text(workload)


@pytest.mark.parametrize("split", ["dev", "test"])
def test_perfect_backend_scores_clean(split, built):
    workload = load_synthetic_workload(f"conflict_v2_{split}")
    meta = built[split][1]
    backend = PerfectBackend(_gold_by_query(workload, meta))
    run_log = replay_workload(backend, workload)
    result = score_conflict(run_log, workload, meta)
    for category in UPDATE_CATEGORIES:
        assert result["categories"][category]["stale_rate"]["value"] == 0.0
        assert result["categories"][category]["update_recall"]["value"] == 1.0
    for category in gen.RETENTION_CATEGORIES:
        assert result["categories"][category]["retention_rate"]["value"] == 1.0
        assert result["categories"][category]["false_supersession_rate"]["value"] == 0.0
    assert result["write_latency_ms"]["median"] == 1.0
    assert result["write_latency_ms"]["p95"] == 1.0
    assert "retention_rate" in format_table(
        {"categories": result["categories"], "write_latency_ms": result["write_latency_ms"]}
    )


def test_overwrite_backend_falsely_supersedes_cross_subject(built):
    workload = load_synthetic_workload("conflict_v2_dev")
    meta = built["dev"][1]
    run_log = replay_workload(OverwriteOnAttributeWordBackend(), workload)
    result = score_conflict(run_log, workload, meta)
    cross = result["categories"]["cross_subject_same_value"]
    assert cross["retention_rate"]["value"] < 1.0
    assert cross["false_supersession_rate"]["value"] > 0.0
    assert cross["retention_rate"]["value"] + cross["false_supersession_rate"]["value"] == pytest.approx(1.0)
    assert result["categories"]["update_same_frame"]["stale_rate"]["value"] == 0.0


def test_incomplete_run_is_refused(built):
    class Failing:
        def write(self, fact_id, text, at):
            return WriteResult(ok=False, latency_ms=0.0, token_count=0, error="down")

        def query(self, text, at):
            return []

        def storage_bytes(self):
            return 0

    workload = load_synthetic_workload("conflict_v2_dev")
    run_log = replay_workload(Failing(), workload)
    with pytest.raises(SystemExit):
        score_conflict(run_log, workload, built["dev"][1])


def test_run_refuses_non_empty_store():
    class Used:
        def write(self, fact_id, text, at):
            return WriteResult(ok=True, latency_ms=0.0, token_count=0)

        def query(self, text, at):
            return [RetrievedFact(fact_id="old")]

        def storage_bytes(self):
            return 0

    with pytest.raises(SystemExit):
        run(Used(), "dev", "Used")


def test_run_end_to_end_with_stub(built):
    workload = load_synthetic_workload("conflict_v2_test")
    backend = PerfectBackend(_gold_by_query(workload, built["test"][1]))
    result = run(backend, "test", "perfect")
    assert result["split"] == "test"
    assert result["categories"]["restatement"]["retention_rate"]["n"] >= 100
