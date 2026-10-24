# SPDX-License-Identifier: Apache-2.0
"""Tests for the eval set converter on synthetic data."""

from __future__ import annotations

import csv
import json
from pathlib import Path

import pyarrow as pa
import pyarrow.parquet as pq
import pytest

from triage_research import make_eval_sets as m


def _jsonl(path: Path, rows: list[dict[str, object]]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(json.dumps(r) + "\n" for r in rows), encoding="utf-8")


def _data(tmp_path: Path) -> Path:
    d = tmp_path / "data"
    _jsonl(
        d / "gsm8k" / "test.jsonl",
        [{"question": f"q{i}", "answer": f"#### {i}"} for i in range(30)],
    )
    _jsonl(d / "routellm" / "gsm8k" / "contaminated_prompts.jsonl", [{"eval_prompt": "q3"}])
    for subject in ("anatomy", "virology"):
        rows: list[dict[str, object]] = [
            {
                "subject": subject,
                "question": f"{subject} {i}",
                "choices": ["a", "b", "c", "d"],
                "answer": i % 4,
            }
            for i in range(20)
        ]
        _jsonl(d / "mmlu" / "test" / f"{subject}.jsonl", rows)
    bad = m.mmlu_prompt("anatomy 0", ["a", "b", "c", "d"])
    _jsonl(d / "routellm" / "mmlu" / "contaminated_prompts.jsonl", [{"eval_prompt": bad}])
    label = d / "routellm" / "mmlu" / "responses" / "mmlu_anatomy.csv"
    label.parent.mkdir(parents=True, exist_ok=True)
    with label.open("w", encoding="utf-8", newline="") as f:
        w = csv.writer(f)
        w.writerow(["prompt", "weak", "strong"])
        for i in range(15):
            w.writerow([m.mmlu_prompt(f"anatomy {i}", ["a", "b", "c", "d"]), True, True])
    return d


def test_mmlu_prompt_matches_label_format() -> None:
    assert m.mmlu_prompt("Q?", ["w", "x", "y", "z"]) == "Q?\nA. w\nB. x\nC. y\nD. z\nAnswer:"


def test_candidates_exclude_contaminated(tmp_path: Path) -> None:
    gsm, mmlu, labelled = m.candidates(_data(tmp_path))
    assert "gsm8k/test:3" not in gsm
    assert len(gsm) == 29
    assert "mmlu/test:anatomy:0" not in mmlu
    assert len(mmlu) == 39
    assert set(labelled) == {f"mmlu/test:anatomy:{i}" for i in range(1, 15)}


def test_draw_is_seeded_disjoint_and_sized(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(m, "GSM8K_PER_SET", 4)
    monkeypatch.setattr(m, "MMLU_PER_SET", 3)
    monkeypatch.setattr(m, "LABELS_MMLU_SIZE", 6)
    monkeypatch.setattr(m, "LABELS_GSM8K_SIZE", 5)
    d = _data(tmp_path)
    a = m.draw(d, 7)
    assert a == m.draw(d, 7)
    assert a != m.draw(d, 8)
    cal, ev = a["live_cal.txt"], a["live_eval.txt"]
    assert len(cal) == len(ev) == 7
    assert not set(cal) & set(ev)
    assert sum(i.startswith("gsm8k/") for i in cal) == 4
    assert len(a["labels_mmlu.txt"]) == 6
    assert all(i.startswith("mmlu/test:anatomy:") for i in a["labels_mmlu.txt"])
    assert len(a["labels_gsm8k.txt"]) == 5
    assert all(i.startswith("gsm8k/test:") for i in a["labels_gsm8k.txt"])


def test_write_sets_files(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(m, "GSM8K_PER_SET", 2)
    monkeypatch.setattr(m, "MMLU_PER_SET", 2)
    monkeypatch.setattr(m, "LABELS_MMLU_SIZE", 3)
    monkeypatch.setattr(m, "LABELS_GSM8K_SIZE", 3)
    out = tmp_path / "sets"
    m.write_sets(_data(tmp_path), out, 7)
    text = (out / "live_cal.txt").read_text(encoding="utf-8").splitlines()
    assert text[0].startswith("# ")
    assert next(line for line in text if not line.startswith("#")).startswith("gsm8k/test:")


def test_convert_parquet(tmp_path: Path) -> None:
    raw = tmp_path / "raw"
    g = raw / m.GSM8K_FILE
    g.parent.mkdir(parents=True)
    pq.write_table(pa.table({"question": ["q"], "answer": ["a #### 1"]}), g)
    s = raw / "hf-mmlu" / "anatomy" / "test-00000-of-00001.parquet"
    s.parent.mkdir(parents=True)
    pq.write_table(
        pa.table(
            {
                "question": ["Q"],
                "subject": ["anatomy"],
                "choices": [["w", "x", "y", "z"]],
                "answer": [2],
            }
        ),
        s,
    )
    out = tmp_path / "out"
    m.convert(raw, out)
    assert json.loads((out / "gsm8k" / "test.jsonl").read_text("utf-8")) == {
        "question": "q",
        "answer": "a #### 1",
    }
    row = json.loads((out / "mmlu" / "test" / "anatomy.jsonl").read_text("utf-8"))
    assert row["choices"] == ["w", "x", "y", "z"]
    assert row["answer"] == 2
