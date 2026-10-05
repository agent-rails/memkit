from __future__ import annotations

import json
import random
from dataclasses import dataclass
from pathlib import Path

SEEDS = {"dev": 20261101, "test": 20261102}
N_UPDATE_SCENARIOS = 102
N_CROSS_SCENARIOS = 51
N_ADDITIVE_SCENARIOS = 51
N_RESTATEMENT_SCENARIOS = 102
CATEGORIES = (
    "update_same_frame",
    "update_reworded",
    "cross_subject_same_value",
    "additive_other_attribute",
    "restatement",
)
UPDATE_CATEGORIES = ("update_same_frame", "update_reworded")
RETENTION_CATEGORIES = ("cross_subject_same_value", "additive_other_attribute", "restatement")


@dataclass(frozen=True)
class Family:
    name: str
    frame: str
    rewordings: tuple[str, ...]
    restatements: tuple[str, ...]
    query: str
    values: tuple[str, ...]


@dataclass(frozen=True)
class SplitSpec:
    name: str
    first_names: tuple[str, ...]
    last_names: tuple[str, ...]
    families: tuple[Family, ...]


def _product(left: list[str], right: list[str]) -> tuple[str, ...]:
    return tuple(f"{a} {b}" for a in left for b in right)


_DEV_MANAGERS = _product(
    ["Gideon", "Henrietta", "Lucan", "Mirela", "Octavia", "Perrin", "Quentin", "Rosalind"],
    ["Ashgrove", "Blackwood", "Cranmore", "Dunleavy", "Eastwick", "Fairbanks"],
)

_DEV = SplitSpec(
    name="dev",
    first_names=(
        "Wren",
        "Idris",
        "Marisol",
        "Callum",
        "Noor",
        "Thaddeus",
        "Ines",
        "Osei",
        "Petra",
        "Ronan",
        "Saoirse",
        "Kenji",
        "Amara",
        "Dmitri",
        "Yara",
        "Bastian",
        "Freya",
        "Tobias",
        "Nadia",
        "Emrys",
        "Solveig",
        "Kwame",
        "Ottilie",
        "Rafael",
    ),
    last_names=(
        "Achterberg",
        "Kowalczyk",
        "Nakashima",
        "Villanueva",
        "Ferreira",
        "Osgood",
        "Bratton",
        "Delacroix",
        "Adeyemi",
        "Hallgren",
        "Marchetti",
        "Okonkwo",
        "Vasilenko",
        "Larrabee",
        "Chowdhury",
        "Duplantis",
        "Ekstrom",
        "Bellweather",
        "Nazarov",
        "Osunde",
        "Pemberton",
        "Rasmussen",
        "Sandoval",
        "Takahashi",
    ),
    families=(
        Family(
            name="employer",
            frame="{s} works at {v}",
            rewordings=("{s} now works at {v}", "{s} has moved to {v}", "{s}'s employer is now {v}"),
            restatements=("{s} is employed at {v}", "{s} has a job at {v}"),
            query="Where does {s} work?",
            values=_product(
                ["Cinder", "Harbor", "Lumen", "Granite", "Auric", "Fenwick", "Bramble", "Sable"],
                ["Labs", "Group", "Partners", "Industries"],
            ),
        ),
        Family(
            name="city",
            frame="{s} lives in {v}",
            rewordings=("{s} now lives in {v}", "{s} relocated to {v}", "{s}'s home city is now {v}"),
            restatements=("{s} resides in {v}", "{s} is based in {v}"),
            query="Which city does {s} live in?",
            values=_product(
                ["Port", "Fort", "Lake", "Mount", "Saint"],
                ["Alder", "Brenner", "Corvin", "Dunmore", "Elsmere", "Ferrow"],
            ),
        ),
        Family(
            name="favorite drink",
            frame="{s}'s favorite drink is {v}",
            rewordings=(
                "{s} now prefers {v} as a drink",
                "{s} switched to {v} as a favorite drink",
                "These days {s} drinks {v} most",
            ),
            restatements=("{s} likes {v} best of all drinks", "The drink {s} favors is {v}"),
            query="What is {s}'s favorite drink?",
            values=_product(
                ["oat milk", "iced", "double", "smoked", "salted"],
                ["latte", "cortado", "mocha", "macchiato", "americano"],
            ),
        ),
        Family(
            name="editor",
            frame="{s} uses {v} as their primary editor",
            rewordings=(
                "{s} has switched to {v} as their primary editor",
                "{s}'s main editor is now {v}",
                "{s} now edits code in {v}",
            ),
            restatements=("{s} relies on {v} as their main editor", "{s} codes mostly in {v}"),
            query="Which editor does {s} use?",
            values=(
                "Sublime Text",
                "Visual Code Editor",
                "Helix Editor",
                "Zed Workbench",
                "Neovim Nightly",
                "Emacs Doom",
                "Kate Pro",
                "Atom Classic",
                "Nano Plus",
                "Geany Lite",
            ),
        ),
        Family(
            name="project",
            frame="{s} is working on the {v} project",
            rewordings=(
                "{s} has been reassigned to the {v} project",
                "{s}'s current project is {v}",
                "{s} now spends their days on the {v} project",
            ),
            restatements=("{s} is assigned to the {v} project", "The {v} project is where {s} spends time"),
            query="What project is {s} working on?",
            values=_product(
                ["Atlas", "Orion", "Nimbus", "Vertex", "Prism", "Halcyon"],
                ["Ledger", "Beacon", "Gateway", "Compass"],
            ),
        ),
        Family(
            name="manager",
            frame="{s}'s manager is {v}",
            rewordings=("{s} now reports to {v}", "{s}'s new manager is {v}", "{s} has been moved under {v}"),
            restatements=("{s} reports to {v}", "{v} is the manager of {s}"),
            query="Who is {s}'s manager?",
            values=_DEV_MANAGERS,
        ),
    ),
)

_TEST = SplitSpec(
    name="test",
    first_names=(
        "Anneliese",
        "Bartholomew",
        "Cosima",
        "Desmond",
        "Elodie",
        "Florian",
        "Gwendolyn",
        "Hakon",
        "Isadora",
        "Jorund",
        "Katya",
        "Leopold",
        "Matilde",
        "Niamh",
        "Octave",
        "Philippa",
        "Quincy",
        "Rhiannon",
        "Soren",
        "Tamsin",
        "Ulrich",
        "Vesper",
        "Wilhelmina",
        "Xavier",
    ),
    last_names=(
        "Aldercott",
        "Brightwater",
        "Castellanos",
        "Dragomir",
        "Eversley",
        "Fitzwilliam",
        "Grunewald",
        "Holloway",
        "Ishikawa",
        "Jorgensen",
        "Kellerman",
        "Lindqvist",
        "Montague",
        "Novotny",
        "Oyelaran",
        "Prescott",
        "Quarles",
        "Rosenthal",
        "Sutherland",
        "Thorvaldsen",
        "Underhill",
        "Vanterpool",
        "Whitlock",
        "Yeoman",
    ),
    families=(
        Family(
            name="instrument played",
            frame="The {v} is what {s} plays",
            rewordings=(
                "These days {s} plays the {v}",
                "{s} has taken up the {v}",
                "The {v} is now the instrument {s} plays",
            ),
            restatements=("{s} plays the {v}", "The instrument {s} performs on is the {v}"),
            query="What instrument does {s} play?",
            values=(
                "Fender Stratocaster",
                "Steinway Grand Piano",
                "Gibson Les Paul",
                "Selmer Alto Saxophone",
                "Ludwig Drum Kit",
                "Martin Dreadnought Guitar",
                "Stradivarius Violin Replica",
                "Moog Synthesizer",
                "Hohner Harmonica",
                "Pearl Marching Snare",
            ),
        ),
        Family(
            name="field studied",
            frame="{s} is studying {v}",
            rewordings=(
                "{s} switched majors to {v}",
                "{s}'s area of study is now {v}",
                "{s} is now enrolled in {v}",
            ),
            restatements=("{s} studies {v}", "{s} is pursuing a degree in {v}"),
            query="What does {s} study?",
            values=_product(
                ["Marine", "Quantum", "Medieval", "Forensic", "Computational"],
                ["Biology", "Chemistry", "History", "Linguistics", "Archaeology"],
            ),
        ),
        Family(
            name="car driven",
            frame="{v} is what {s} drives",
            rewordings=(
                "{s} now drives a {v}",
                "{s} traded in for a {v}",
                "A {v} is the car {s} drives these days",
            ),
            restatements=("{s} drives a {v}", "The car belonging to {s} is a {v}"),
            query="What car does {s} drive?",
            values=_product(
                ["red", "teal", "silver", "black", "green"],
                ["Subaru Outback", "Mazda Miata", "Volvo Estate", "Honda Civic", "Ford Ranger"],
            ),
        ),
        Family(
            name="language spoken at home",
            frame="{s} speaks {v} at home",
            rewordings=(
                "{s}'s household language is now {v}",
                "At home {s} has switched to speaking {v}",
                "{s} now converses in {v} with family",
            ),
            restatements=("{s} uses {v} around the house", "{v} is spoken in {s}'s household"),
            query="Which language does {s} speak at home?",
            values=(
                "Brazilian Portuguese",
                "Swiss German",
                "Canadian French",
                "Scots Gaelic",
                "Cantonese Chinese",
                "Egyptian Arabic",
                "Mexican Spanish",
                "Tagalog Filipino",
                "Haitian Creole",
                "Neapolitan Italian",
            ),
        ),
        Family(
            name="gym attended",
            frame="{s} trains at {v}",
            rewordings=(
                "{s} has joined {v} for workouts",
                "{s}'s gym membership is now with {v}",
                "{s} cancelled the old gym and signed up at {v}",
            ),
            restatements=("{s} works out at {v}", "{s} is a member of {v}"),
            query="Which gym does {s} attend?",
            values=_product(
                ["Iron", "Summit", "Anvil", "Vanguard", "Cobalt", "Titan"],
                ["Barbell Club", "Fitness Studio", "Strength Works", "Athletic Hall"],
            ),
        ),
        Family(
            name="pet name",
            frame="{v} is the name of {s}'s pet",
            rewordings=(
                "{s} renamed the pet to {v}",
                "{s}'s pet now goes by {v}",
                "The pet {s} owns is called {v} now",
            ),
            restatements=("{s}'s pet is named {v}", "{s} calls their pet {v}"),
            query="What is the name of {s}'s pet?",
            values=_product(
                ["Sir", "Lady", "Captain", "Professor", "Baron"],
                ["Waffles", "Biscuit", "Pickles", "Nutmeg", "Truffle"],
            ),
        ),
    ),
)

SPECS = {"dev": _DEV, "test": _TEST}


def _tokens(values) -> set[str]:
    return {t.lower() for v in values for t in v.split()}


def _templates(spec: SplitSpec) -> set[str]:
    out: set[str] = set()
    for f in spec.families:
        out.add(f.frame)
        out.add(f.query)
        out.update(f.rewordings)
        out.update(f.restatements)
    return out


def _all_values(spec: SplitSpec) -> set[str]:
    return {v for f in spec.families for v in f.values}


def check_spec_hygiene() -> None:
    dev, test = SPECS["dev"], SPECS["test"]
    problems = []
    for spec in SPECS.values():
        for f in spec.families:
            if len(f.rewordings) < 3:
                problems.append(f"{spec.name}/{f.name}: fewer than 3 rewordings")
            if len(set(f.values)) != len(f.values):
                problems.append(f"{spec.name}/{f.name}: duplicate values")
            if any(not 2 <= len(v.split()) <= 3 for v in f.values):
                problems.append(f"{spec.name}/{f.name}: values must be 2-3 tokens")
        names = _tokens(spec.first_names) | _tokens(spec.last_names)
        if names & _tokens(_all_values(spec)):
            problems.append(f"{spec.name}: subject names overlap value tokens")
    if {f.name for f in dev.families} & {f.name for f in test.families}:
        problems.append("attribute families overlap between splits")
    if _tokens(dev.first_names) & _tokens(test.first_names):
        problems.append("first-name pools overlap between splits")
    if _tokens(dev.last_names) & _tokens(test.last_names):
        problems.append("last-name pools overlap between splits")
    if _all_values(dev) & _all_values(test):
        problems.append("value pools overlap between splits")
    if _tokens(_all_values(dev)) & _tokens(_all_values(test)):
        problems.append("value tokens overlap between splits")
    if _templates(dev) & _templates(test):
        problems.append("sentence frames, rewordings, restatements or queries overlap between splits")
    if not sum(1 for f in test.families if f.frame.startswith("{v}") or f.frame.startswith("The {v}")) >= 2:
        problems.append("test needs at least two value-before-subject frames")
    if problems:
        raise SystemExit("refusing to generate: " + "; ".join(problems))


def _fact(template: str, subject: str, value: str) -> str:
    return template.format(s=subject, v=value)


def _allocate_subjects(spec: SplitSpec, rng: random.Random):
    combos = [(f, last) for f in spec.first_names for last in spec.last_names]
    rng.shuffle(combos)
    unused = set(combos)
    pairs = []
    lasts = list(spec.last_names)
    for _ in range(N_CROSS_SCENARIOS):
        candidates = [last for last in lasts if sum(1 for f in spec.first_names if (f, last) in unused) >= 2]
        last = rng.choice(candidates)
        firsts = [f for f in spec.first_names if (f, last) in unused]
        a, b = rng.sample(firsts, 2)
        unused.discard((a, last))
        unused.discard((b, last))
        pairs.append((f"{a} {last}", f"{b} {last}"))
    singles = [f"{f} {last}" for f, last in combos if (f, last) in unused]
    needed = 2 * N_UPDATE_SCENARIOS + N_ADDITIVE_SCENARIOS + N_RESTATEMENT_SCENARIOS
    if len(singles) < needed:
        raise SystemExit(f"{spec.name}: subject pool too small ({len(singles)} < {needed})")
    return pairs, singles


def _query_step(category: str, subject: str, family: Family, expected: list[str], stale: list[str], match: str):
    return {
        "kind": "query",
        "query_text": family.query.format(s=subject),
        "gold": {"exact_ids": expected, "stale_ids_that_must_not_surface": stale or None},
        "meta": {
            "category": category,
            "semantics": "update" if stale else "retention",
            "expected_ids": expected,
            "stale_ids": stale,
            "match": match,
            "attribute": family.name,
        },
    }


def _write_step(fact_id: str, text: str):
    return {"kind": "write", "fact_id": fact_id, "fact_text": text}


def _build_scenarios(spec: SplitSpec, rng: random.Random) -> list[list[dict]]:
    pairs, singles = _allocate_subjects(spec, rng)
    families = spec.families
    n_fam = len(families)
    scenarios: list[list[dict]] = []
    tag = spec.name

    for i, (sa, sb) in enumerate(pairs):
        fam = families[i % n_fam]
        value = rng.choice(fam.values)
        ia, ib = f"{tag}_cross_{i:03d}_a", f"{tag}_cross_{i:03d}_b"
        scenarios.append(
            [
                _write_step(ia, _fact(fam.frame, sa, value)),
                _write_step(ib, _fact(fam.frame, sb, value)),
                _query_step("cross_subject_same_value", sa, fam, [ia], [], "all_of"),
                _query_step("cross_subject_same_value", sb, fam, [ib], [], "all_of"),
            ]
        )

    cursor = 0
    for category, template_for in (("update_same_frame", None), ("update_reworded", "reword")):
        for i in range(N_UPDATE_SCENARIOS):
            subject = singles[cursor]
            cursor += 1
            fam = families[i % n_fam]
            old_value, new_value = rng.sample(fam.values, 2)
            new_template = fam.frame if template_for is None else fam.rewordings[(i // n_fam) % len(fam.rewordings)]
            short = "same" if template_for is None else "reword"
            io, inew = f"{tag}_{short}_{i:03d}_old", f"{tag}_{short}_{i:03d}_new"
            scenarios.append(
                [
                    _write_step(io, _fact(fam.frame, subject, old_value)),
                    _write_step(inew, _fact(new_template, subject, new_value)),
                    _query_step(category, subject, fam, [inew], [io], "all_of"),
                ]
            )

    for i in range(N_ADDITIVE_SCENARIOS):
        subject = singles[cursor]
        cursor += 1
        fam_a = families[i % n_fam]
        fam_b = rng.choice([f for f in families if f is not fam_a])
        ia, ib = f"{tag}_add_{i:03d}_a", f"{tag}_add_{i:03d}_b"
        scenarios.append(
            [
                _write_step(ia, _fact(fam_a.frame, subject, rng.choice(fam_a.values))),
                _write_step(ib, _fact(fam_b.frame, subject, rng.choice(fam_b.values))),
                _query_step("additive_other_attribute", subject, fam_a, [ia], [], "all_of"),
                _query_step("additive_other_attribute", subject, fam_b, [ib], [], "all_of"),
            ]
        )

    for i in range(N_RESTATEMENT_SCENARIOS):
        subject = singles[cursor]
        cursor += 1
        fam = families[i % n_fam]
        value = rng.choice(fam.values)
        restated = fam.restatements[(i // n_fam) % len(fam.restatements)]
        io, ir = f"{tag}_restate_{i:03d}_orig", f"{tag}_restate_{i:03d}_again"
        scenarios.append(
            [
                _write_step(io, _fact(fam.frame, subject, value)),
                _write_step(ir, _fact(restated, subject, value)),
                _query_step("restatement", subject, fam, [io, ir], [], "any_of"),
            ]
        )
    return scenarios


def build_split(split: str) -> tuple[dict, dict]:
    spec = SPECS[split]
    rng = random.Random(SEEDS[split])
    scenarios = _build_scenarios(spec, rng)
    active = [list(reversed(s)) for s in scenarios]
    events: list[dict] = []
    queries: dict[str, dict] = {}
    t = 0.0
    while active:
        idx = rng.randrange(len(active))
        step = active[idx].pop()
        if not active[idx]:
            active.pop(idx)
        event = {k: v for k, v in step.items() if k != "meta"}
        event["at"] = t
        t += 1.0
        if step["kind"] == "query":
            queries[str(len(events))] = step["meta"]
        events.append(event)
    name = f"conflict_v2_{split}"
    return (
        {"name": name, "kind": "synthetic", "events": events},
        {"workload": name, "split": split, "queries": queries},
    )


def check_no_duplicate_fact_text(workload: dict) -> None:
    texts = [e["fact_text"] for e in workload["events"] if e["kind"] == "write"]
    dupes = len(texts) - len(set(texts))
    if dupes:
        raise SystemExit(f"refusing to write {workload['name']}: {dupes} duplicate fact_text values")


def category_counts(meta: dict) -> dict[str, int]:
    counts = dict.fromkeys(CATEGORIES, 0)
    for q in meta["queries"].values():
        counts[q["category"]] += 1
    return counts


def main() -> None:
    check_spec_hygiene()
    out_dir = Path(__file__).parent
    for split in ("dev", "test"):
        workload, meta = build_split(split)
        check_no_duplicate_fact_text(workload)
        counts = category_counts(meta)
        if min(counts.values()) < 100:
            raise SystemExit(f"{split}: category below 100 query events: {counts}")
        (out_dir / f"{workload['name']}.json").write_text(json.dumps(workload, indent=2), encoding="utf-8")
        (out_dir / f"{workload['name']}.meta.json").write_text(json.dumps(meta, indent=2), encoding="utf-8")
        print(f"wrote {workload['name']} -- {counts}")


if __name__ == "__main__":
    main()
