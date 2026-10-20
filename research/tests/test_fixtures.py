# SPDX-License-Identifier: Apache-2.0
"""The committed testdata/models fixtures are exactly what make_fixtures produces.

Needs the XLM-R tokenizer.json cached by ``make bootstrap``.
"""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np

from triage_research import make_fixtures, record_embeddings, reference_bert, reference_mf

COMMITTED = make_fixtures.DEFAULT_OUT


def _files(root: Path) -> dict[str, bytes]:
    return {
        str(p.relative_to(root)): p.read_bytes()
        for p in sorted(root.rglob("*"))
        if p.is_file() and p.name != "README.md"
    }


def test_fixtures_regenerate_identically(tmp_path: Path) -> None:
    assert make_fixtures.main(["--out", str(tmp_path)]) == 0
    want = _files(COMMITTED)
    got = _files(tmp_path)
    assert sorted(got) == sorted(want)
    differ = [name for name in want if want[name] != got[name]]
    assert differ == []


def test_synthetic_embeddings_are_unit_vectors() -> None:
    vecs, meta = record_embeddings.load(COMMITTED / "embeddings")
    assert vecs.shape == (record_embeddings.COUNT, record_embeddings.DIMENSIONS)
    assert vecs.dtype == np.float32
    np.testing.assert_allclose(np.linalg.norm(vecs.astype(np.float64), axis=1), 1.0, atol=1e-6)
    assert meta["synthetic"] is True
    assert len(meta["ids"]) == record_embeddings.COUNT


def test_mf_reference_parity() -> None:
    doc = json.loads((COMMITTED / "mf-tiny" / "reference.json").read_text())
    assert (doc["strong_row"], doc["weak_row"]) == (24, 36)
    assert len(doc["u"]) == record_embeddings.DIMENSIONS
    worst = max(abs(s["p_mfmodel"] - s["p_strong"]) for s in doc["scores"])
    assert worst <= 1e-6
    vecs, _ = record_embeddings.load(COMMITTED / "embeddings")
    again = reference_mf.p_closed_form(np.asarray(doc["u"]), vecs)
    assert again == [s["p_strong"] for s in doc["scores"]]


def test_bert_reference_parity() -> None:
    rows = [
        json.loads(line)
        for line in (COMMITTED / "bert-tiny" / "reference.jsonl").read_text().splitlines()
    ]
    corpus = reference_bert.read_prompts(reference_bert.DEFAULT_PROMPTS)
    assert [r["id"] for r in rows] == [p.id for p in corpus]
    assert max(abs(r["p_fp32"] - r["p_torch"]) for r in rows) <= 1e-5
    assert max(r["n_tokens"] for r in rows) == reference_bert.MAX_TOKENS
    spread = [r["p_fp32"] for r in rows]
    assert max(spread) - min(spread) > 0.1
