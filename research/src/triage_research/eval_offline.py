# SPDX-License-Identifier: Apache-2.0
"""Offline APGR, the Python reference for ``internal/app/eval``.

``apgr`` is the same computation as Go ``eval.APGR``, operation for operation, so the two agree
far inside the 1e-9 cross-check tolerance on the same score file:

1. sort items by score descending (stable); equal scores form a tie group that is routed
   together, valued at the expected gain of a random j-subset (j/m of the group's summed
   ``strong - weak``);
2. ``acc(k) = (sum w + gain of the top k) / N`` for k = 0..N;
3. ``PGR(k) = (acc(k) - acc_weak) / (acc_strong - acc_weak)`` (undefined when equal);
4. APGR = trapezoid area under PGR over k/N in [0, 1] using all N + 1 points.

The paper (arXiv 2406.18665, eq. 8) averages PGR over ten call percentages. RouteLLM's own
``evaluate.py`` (pinned commit 0b64fdaf) takes the 11 decile edges of the scores as
thresholds (``pd.qcut``), routes ``score >= t`` to strong (``>`` for the last edge) and
integrates with the trapezoid rule. That variant is reported as ``apgr_routellm_deciles`` for
information only; it is not what Triage gates on (Triage integrates over every ``k``).

Input: JSONL or CSV (by extension) with ``score``, ``weak_correct``, ``strong_correct``
(booleans as ``true/false``, ``1/0``); with ``--strategy S`` only rows whose ``strategy``
column equals S are used. Output: one JSON object on stdout.
"""

from __future__ import annotations

import argparse
import csv
import json
import math
import sys
from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np

_TRUE = frozenset({"true", "1", "yes", "t", "y"})
_FALSE = frozenset({"false", "0", "no", "f", "n", ""})
_DECILES = 10


class NoGapError(ValueError):
    """acc_strong == acc_weak, so PGR is undefined."""


@dataclass(frozen=True)
class Item:
    """One evaluated prompt."""

    score: float
    weak_correct: bool
    strong_correct: bool


@dataclass(frozen=True)
class Point:
    """One point of the curve."""

    strong_fraction: float
    accuracy: float
    pgr: float


def curve(items: Sequence[Item]) -> list[Point]:
    """The N + 1 points k = 0..N, as Go ``eval.Curve``."""
    n = len(items)
    if n == 0:
        return []
    order = sorted(items, key=lambda it: -it.score)  # stable, like sort.SliceStable
    weak = sum(int(it.weak_correct) for it in order)
    strong = sum(int(it.strong_correct) for it in order)
    fn = float(n)
    acc_w, acc_s = float(weak) / fn, float(strong) / fn

    def point(k: int, acc: float) -> Point:
        pgr = math.nan if acc_s == acc_w else (acc - acc_w) / (acc_s - acc_w)
        return Point(float(k) / fn, acc, pgr)

    pts = [point(0, acc_w)]
    base = 0
    start = 0
    while start < n:
        end = start
        gain = 0
        while end < n and order[end].score == order[start].score:
            gain += int(order[end].strong_correct) - int(order[end].weak_correct)
            end += 1
        m = end - start
        for j in range(1, m + 1):
            acc = (float(weak) + float(base) + float(gain) * float(j) / float(m)) / fn
            pts.append(point(start + j, acc))
        base += gain
        start = end
    return pts


def apgr(items: Sequence[Item]) -> float:
    """Trapezoid area under PGR over k/N, as Go ``eval.APGR``."""
    pts = curve(items)
    if not pts:
        raise ValueError("no items")
    if math.isnan(pts[0].pgr):
        raise NoGapError("weak and strong accuracy are equal; PGR is undefined")
    area = 0.0
    for i in range(1, len(pts)):
        area += (
            (pts[i].strong_fraction - pts[i - 1].strong_fraction)
            * (pts[i].pgr + pts[i - 1].pgr)
            / 2
        )
    return area


def apgr_routellm_deciles(items: Sequence[Item]) -> float:
    """RouteLLM ``evaluate.py`` APGR: decile-edge thresholds, trapezoid over 11 points."""
    scores = np.asarray([it.score for it in items], dtype=np.float64)
    weak = np.asarray([it.weak_correct for it in items], dtype=np.float64)
    strong = np.asarray([it.strong_correct for it in items], dtype=np.float64)
    acc_w, acc_s = float(weak.mean()), float(strong.mean())
    if acc_w == acc_s:
        raise NoGapError("weak and strong accuracy are equal; PGR is undefined")
    edges = np.quantile(scores, np.linspace(0.0, 1.0, _DECILES + 1))
    pts: list[tuple[float, float]] = []
    for i, thr in enumerate(edges):
        routed = scores >= thr if i != len(edges) - 1 else scores > thr
        acc = float(np.where(routed, strong, weak).mean())
        pts.append((float(routed.mean()), acc))
    pts.sort()
    xs = np.asarray([p[0] for p in pts])
    ys = np.asarray([p[1] for p in pts])
    auc = float(np.trapezoid(ys, xs))
    span = float(xs[-1] - xs[0])
    return (auc - acc_w * span) / (acc_s * span - acc_w * span)


def _bool(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    text = str(value).strip().lower()
    if text in _TRUE:
        return True
    if text in _FALSE:
        return False
    raise ValueError(f"not a boolean: {value!r}")


def _rows(path: Path) -> Iterable[dict[str, Any]]:
    with path.open(encoding="utf-8", newline="") as fh:
        if path.suffix == ".csv":
            yield from csv.DictReader(fh)
            return
        for line in fh:
            if line.strip():
                yield json.loads(line)


def read_items(path: Path, strategy: str | None = None) -> list[Item]:
    """Read a score file; see the module documentation for the format."""
    out: list[Item] = []
    for row in _rows(path):
        if strategy is not None and row.get("strategy") != strategy:
            continue
        out.append(
            Item(
                score=float(row["score"]),
                weak_correct=_bool(row["weak_correct"]),
                strong_correct=_bool(row["strong_correct"]),
            )
        )
    return out


def summary(items: Sequence[Item], with_curve: bool = False) -> dict[str, Any]:
    """APGR (Triage definition), RouteLLM decile APGR and the single-model accuracies."""
    n = len(items)
    doc: dict[str, Any] = {
        "n": n,
        "acc_weak": sum(it.weak_correct for it in items) / n,
        "acc_strong": sum(it.strong_correct for it in items) / n,
        "apgr": apgr(items),
        "apgr_routellm_deciles": apgr_routellm_deciles(items),
    }
    if with_curve:
        doc["curve"] = [[p.strong_fraction, p.accuracy, p.pgr] for p in curve(items)]
    return doc


def main(argv: list[str] | None = None) -> int:
    """CLI: ``python -m triage_research.eval_offline SCORES [--strategy S] [--curve]``."""
    parser = argparse.ArgumentParser(
        prog="triage-eval-offline",
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("scores", type=Path)
    parser.add_argument("--strategy", default=None)
    parser.add_argument("--curve", action="store_true", help="include the N+1 curve points")
    args = parser.parse_args(argv)
    items = read_items(args.scores, args.strategy)
    if not items:
        sys.stderr.write("eval_offline: no items\n")
        return 1
    try:
        doc = summary(items, args.curve)
    except NoGapError as err:
        sys.stderr.write(f"eval_offline: {err}\n")
        return 1
    sys.stdout.write(json.dumps(doc, allow_nan=True) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
