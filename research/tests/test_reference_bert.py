# SPDX-License-Identifier: Apache-2.0
"""Unit tests for triage_research.reference_bert helpers (no cached artifacts needed)."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from triage_research import build_bert_graph as bbg
from triage_research import reference_bert as rb


class _Enc:
    def __init__(self, ids: list[int]) -> None:
        self.ids = ids


class _Tok:
    def __init__(self, ids: list[int]) -> None:
        self._ids = ids

    def encode(self, text: str) -> _Enc:  # noqa: ARG002
        return _Enc(self._ids)


def test_encode_truncates_keeping_eos() -> None:
    ids = [0, *range(10, 20), 2]
    assert rb.encode(_Tok(ids), "x", 5) == [0, 10, 11, 12, 2]  # type: ignore[arg-type]
    assert rb.encode(_Tok(ids), "x", 512) == ids  # type: ignore[arg-type]


def test_softmax0() -> None:
    assert bbg.softmax0([0.0, 0.0, 0.0]) == pytest.approx(1 / 3)
    assert bbg.softmax0([1000.0, 0.0, 0.0]) == pytest.approx(1.0)


def test_read_prompts_and_jsonl(tmp_path: Path) -> None:
    src = tmp_path / "p.jsonl"
    src.write_text('{"id": "a", "text": "x"}\n\n{"id": "b", "text": "y"}\n')
    prompts = rb.read_prompts(src)
    assert [p.id for p in prompts] == ["a", "b"]
    assert rb.read_prompts(src, 1) == prompts[:1]
    out = tmp_path / "o" / "s.jsonl"
    rb.write_jsonl(out, [rb.Scored("a", 3, (0.1, 0.2, 0.3), 0.25)])
    assert json.loads(out.read_text()) == {
        "id": "a",
        "n_tokens": 3,
        "logits": [0.1, 0.2, 0.3],
        "p_strong": 0.25,
    }


def test_external_graph_needs_checkpoint_dir() -> None:
    with pytest.raises(ValueError, match="checkpoint-dir"):
        rb.score_onnx_model(bbg.DEFAULT_OUT, None, rb.DEFAULT_TOKENIZER, [])
