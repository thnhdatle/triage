# SPDX-License-Identifier: Apache-2.0
"""Convert the fetched evaluation data to JSONL and draw the committed item-id sets."""

from __future__ import annotations

import argparse
import csv
import json
import random
import shutil
import sys
from collections.abc import Sequence
from pathlib import Path
from typing import Any

import pyarrow.parquet as pq

DEFAULT_SEED = 7
GSM8K_PER_SET = 8
MMLU_PER_SET = 7
LABELS_MMLU_SIZE = 300
LABELS_GSM8K_SIZE = 100
GSM8K_FILE = Path("hf-gsm8k/main/test-00000-of-00001.parquet")
LETTERS = "ABCD"
_CSV_FIELD_LIMIT = 1 << 24
_MMLU_PARTS = 3


def mmlu_prompt(question: str, choices: Sequence[str]) -> str:
    lines = [question] + [f"{LETTERS[i]}. {c}" for i, c in enumerate(choices)]
    return "\n".join(lines) + "\nAnswer:"


def _rows(path: Path) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = pq.read_table(path).to_pylist()
    return rows


def _write_jsonl(path: Path, rows: Sequence[dict[str, Any]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")


def convert(raw: Path, out: Path) -> None:
    """Convert the Parquet files under ``raw`` into JSONL under ``out``."""
    gsm = [{"question": r["question"], "answer": r["answer"]} for r in _rows(raw / GSM8K_FILE)]
    _write_jsonl(out / "gsm8k" / "test.jsonl", gsm)
    for sub in sorted((raw / "hf-mmlu").iterdir()):
        if not sub.is_dir():
            continue
        rows = [
            {
                "subject": r["subject"],
                "question": r["question"],
                "choices": list(r["choices"]),
                "answer": int(r["answer"]),
            }
            for r in _rows(sub / "test-00000-of-00001.parquet")
        ]
        _write_jsonl(out / "mmlu" / "test" / f"{sub.name}.jsonl", rows)
    rl = raw / "routellm"
    if rl.is_dir():
        shutil.copytree(rl, out / "routellm", dirs_exist_ok=True)


def _read_jsonl(path: Path) -> list[dict[str, Any]]:
    with path.open(encoding="utf-8") as f:
        return [json.loads(line) for line in f if line.strip()]


def _contaminated(path: Path) -> set[str]:
    return {r["eval_prompt"] for r in _read_jsonl(path)}


def _label_prompts(path: Path) -> set[str]:
    csv.field_size_limit(_CSV_FIELD_LIMIT)
    with path.open(encoding="utf-8", newline="") as f:
        return {row["prompt"] for row in csv.DictReader(f)}


def candidates(data: Path) -> tuple[list[str], list[str], list[str]]:
    rl = data / "routellm"
    bad_gsm = _contaminated(rl / "gsm8k" / "contaminated_prompts.jsonl")
    bad_mmlu = _contaminated(rl / "mmlu" / "contaminated_prompts.jsonl")
    gsm = [
        f"gsm8k/test:{i}"
        for i, r in enumerate(_read_jsonl(data / "gsm8k" / "test.jsonl"))
        if r["question"] not in bad_gsm
    ]
    mmlu: list[str] = []
    labelled: list[str] = []
    for path in sorted((data / "mmlu" / "test").glob("*.jsonl")):
        subject = path.stem
        label_file = rl / "mmlu" / "responses" / f"mmlu_{subject}.csv"
        have = _label_prompts(label_file) if label_file.is_file() else set()
        for i, r in enumerate(_read_jsonl(path)):
            prompt = mmlu_prompt(r["question"], r["choices"])
            if prompt in bad_mmlu:
                continue
            item = f"mmlu/test:{subject}:{i}"
            mmlu.append(item)
            if prompt in have:
                labelled.append(item)
    return gsm, mmlu, labelled


def _sort_key(item: str) -> tuple[str, str, int]:
    parts = item.split(":")
    return (parts[0], parts[1] if len(parts) == _MMLU_PARTS else "", int(parts[-1]))


def draw(data: Path, seed: int) -> dict[str, list[str]]:
    """Draw the four item-id sets with a seeded sample."""
    gsm, mmlu, labelled = candidates(data)
    rng = random.Random(seed)  # noqa: S311
    g = rng.sample(gsm, 2 * GSM8K_PER_SET)
    m = rng.sample(mmlu, 2 * MMLU_PER_SET)
    lm = random.Random(seed).sample(labelled, LABELS_MMLU_SIZE)  # noqa: S311
    lg = random.Random(seed).sample(gsm, LABELS_GSM8K_SIZE)  # noqa: S311
    return {
        "live_cal.txt": sorted(g[:GSM8K_PER_SET] + m[:MMLU_PER_SET], key=_sort_key),
        "live_eval.txt": sorted(g[GSM8K_PER_SET:] + m[MMLU_PER_SET:], key=_sort_key),
        "labels_mmlu.txt": sorted(lm, key=_sort_key),
        "labels_gsm8k.txt": sorted(lg, key=_sort_key),
    }


_HEADERS = {
    "live_cal.txt": f"Live calibration set: {GSM8K_PER_SET} GSM8K + {MMLU_PER_SET} MMLU test items",
    "live_eval.txt": f"Live evaluation set: {GSM8K_PER_SET} GSM8K + {MMLU_PER_SET} MMLU test items",
    "labels_mmlu.txt": f"Labels set: {LABELS_MMLU_SIZE} labelled MMLU test items",
    "labels_gsm8k.txt": f"Labels set: {LABELS_GSM8K_SIZE} GSM8K test items",
}


def write_sets(data: Path, out: Path, seed: int) -> None:
    out.mkdir(parents=True, exist_ok=True)
    for name, items in draw(data, seed).items():
        header = (
            f"# {_HEADERS[name]}\n# seed {seed}, contaminated prompts excluded; "
            "regenerate with research/src/triage_research/make_eval_sets.py\n"
        )
        (out / name).write_text(header + "\n".join(items) + "\n", encoding="utf-8")


def main(argv: Sequence[str] | None = None) -> int:
    """Entry point: ``convert`` or ``sample``."""
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0] if __doc__ else None)
    sub = ap.add_subparsers(dest="cmd", required=True)
    c = sub.add_parser("convert", help="Parquet to JSONL")
    c.add_argument("--raw", type=Path, required=True)
    c.add_argument("--out", type=Path, required=True)
    s = sub.add_parser("sample", help="draw the committed item-id sets")
    s.add_argument("--data", type=Path, required=True, help="converted data directory")
    s.add_argument("--out", type=Path, default=Path("../eval/sets"))
    s.add_argument("--seed", type=int, default=DEFAULT_SEED)
    args = ap.parse_args(argv)
    if args.cmd == "convert":
        convert(args.raw, args.out)
    else:
        write_sets(args.data, args.out, args.seed)
    return 0


if __name__ == "__main__":
    sys.exit(main())
