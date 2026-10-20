# SPDX-License-Identifier: Apache-2.0
"""Unit tests for triage_research.eval_offline (APGR identical to internal/app/eval)."""

from __future__ import annotations

import json
import math
import random
from pathlib import Path

import pytest

from triage_research import eval_offline as ev


def _items(rows: list[tuple[float, bool, bool]]) -> list[ev.Item]:
    return [ev.Item(score=s, weak_correct=w, strong_correct=st) for s, w, st in rows]


def test_apgr_hand_computed() -> None:
    # weak right on 1 of 4, strong right on 3 of 4; gains by descending score: +1, 0, +1, 0.
    items = _items([(0.9, False, True), (0.8, True, True), (0.4, False, True), (0.1, False, False)])
    pts = ev.curve(items)
    assert [p.pgr for p in pts] == [0.0, 0.5, 0.5, 1.0, 1.0]
    # trapezoids over k/4: (0+.5)/2 + (.5+.5)/2 + (.5+1)/2 + (1+1)/2, each times 1/4
    assert ev.apgr(items) == pytest.approx((0.25 + 0.5 + 0.75 + 1.0) / 4, abs=1e-15)


def test_all_tied_scores_give_half() -> None:
    rnd = random.Random(1)
    items = [ev.Item(0.5, rnd.random() < 0.3, rnd.random() < 0.9) for _ in range(101)]
    if sum(i.weak_correct for i in items) == sum(i.strong_correct for i in items):
        pytest.fail("seed produced no gap")
    assert ev.apgr(items) == pytest.approx(0.5, abs=1e-12)


def test_tie_group_is_linear_inside() -> None:
    items = _items([(1.0, False, True), (1.0, True, True), (1.0, False, False), (0.0, False, True)])
    acc = [p.accuracy for p in ev.curve(items)]
    assert acc[1] - acc[0] == pytest.approx(acc[2] - acc[1])
    assert acc[2] - acc[1] == pytest.approx(acc[3] - acc[2])


def test_oracle_beats_random_and_reverse() -> None:
    rnd = random.Random(7)
    base = [(rnd.random() < 0.5, rnd.random() < 0.8) for _ in range(500)]
    oracle = [ev.Item(float(s) - float(w), w, s) for w, s in base]
    reverse = [ev.Item(float(w) - float(s), w, s) for w, s in base]
    assert ev.apgr(oracle) > 0.5 > ev.apgr(reverse)


def test_no_gap_and_empty() -> None:
    with pytest.raises(ev.NoGapError):
        ev.apgr(_items([(0.1, True, True), (0.2, False, False)]))
    with pytest.raises(ValueError, match="no items"):
        ev.apgr([])
    assert ev.curve([]) == []
    assert math.isnan(ev.curve(_items([(0.1, True, True)]))[0].pgr)


def test_routellm_deciles_close_to_exact_on_large_set() -> None:
    rnd = random.Random(3)
    items = [ev.Item(rnd.random(), rnd.random() < 0.55, rnd.random() < 0.85) for _ in range(2000)]
    assert ev.apgr_routellm_deciles(items) == pytest.approx(ev.apgr(items), abs=0.01)


def test_read_jsonl_csv_and_strategy(tmp_path: Path) -> None:
    jl = tmp_path / "s.jsonl"
    jl.write_text(
        "\n".join(
            json.dumps({"score": s, "weak_correct": w, "strong_correct": t, "strategy": g})
            for s, w, t, g in [
                (0.9, False, True, "bert"),
                (0.1, True, True, "bert"),
                (0.5, 0, 1, "x"),
            ]
        )
    )
    assert len(ev.read_items(jl)) == 3
    assert len(ev.read_items(jl, "bert")) == 2
    cs = tmp_path / "s.csv"
    cs.write_text("score,weak_correct,strong_correct\n0.9,false,true\n0.1,1,1\n0.3,no,yes\n")
    items = ev.read_items(cs)
    assert items == _items([(0.9, False, True), (0.1, True, True), (0.3, False, True)])
    bad = tmp_path / "b.csv"
    bad.write_text("score,weak_correct,strong_correct\n0.9,maybe,true\n")
    with pytest.raises(ValueError, match="not a boolean"):
        ev.read_items(bad)


def test_main_prints_summary(tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
    cs = tmp_path / "s.csv"
    cs.write_text(
        "score,weak_correct,strong_correct\n0.9,false,true\n0.1,true,true\n0.5,false,false\n"
    )
    assert ev.main([str(cs), "--curve"]) == 0
    doc = json.loads(capsys.readouterr().out)
    assert doc["n"] == 3
    assert len(doc["curve"]) == 4
    assert doc["apgr"] == ev.apgr(ev.read_items(cs))
    tie = tmp_path / "t.csv"
    tie.write_text("score,weak_correct,strong_correct\n0.9,true,true\n")
    assert ev.main([str(tie)]) == 1
    assert ev.main([str(cs), "--strategy", "none"]) == 1
