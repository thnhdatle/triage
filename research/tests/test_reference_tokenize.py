# SPDX-License-Identifier: Apache-2.0
"""Unit tests for triage_research.reference_tokenize (no network, no cached artifacts)."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest
from tokenizers import Tokenizer

from triage_research import reference_tokenize as rt

ADDED_DEFAULTS = {
    "single_word": False,
    "lstrip": False,
    "rstrip": False,
    "normalized": False,
    "special": True,
}


def _template(first: str, last: str, first_id: int, last_id: int) -> dict[str, Any]:
    seq = [
        {"SpecialToken": {"id": first, "type_id": 0}},
        {"Sequence": {"id": "A", "type_id": 0}},
        {"SpecialToken": {"id": last, "type_id": 0}},
    ]
    return {
        "type": "TemplateProcessing",
        "single": seq,
        "pair": seq,
        "special_tokens": {
            first: {"id": first, "ids": [first_id], "tokens": [first]},
            last: {"id": last, "ids": [last_id], "tokens": [last]},
        },
    }


def _tokenizer_json(
    model: dict[str, Any],
    added: list[dict[str, Any]],
    normalizer: dict[str, Any] | None,
    pre: dict[str, Any] | None,
    post: dict[str, Any],
) -> str:
    return json.dumps(
        {
            "version": "1.0",
            # Truncation is enabled on purpose: the reference must ignore it.
            "truncation": {
                "direction": "Right",
                "max_length": 4,
                "strategy": "LongestFirst",
                "stride": 0,
            },
            "padding": None,
            "added_tokens": [{**ADDED_DEFAULTS, **a} for a in added],
            "normalizer": normalizer,
            "pre_tokenizer": pre,
            "post_processor": post,
            "decoder": None,
            "model": model,
        }
    )


BERT_NORMALIZER = {
    "type": "BertNormalizer",
    "clean_text": True,
    "handle_chinese_chars": True,
    "strip_accents": None,
    "lowercase": False,
}


@pytest.fixture
def tokenizer_dir(tmp_path: Path) -> Path:
    unigram = {
        "type": "Unigram",
        "unk_id": 3,
        "byte_fallback": False,
        "vocab": [["<s>", 0], ["<pad>", 0], ["</s>", 0], ["<unk>", 0], ["▁a", -1], ["b", -2]],
    }
    bert = _tokenizer_json(
        unigram,
        [{"id": 0, "content": "<s>"}, {"id": 2, "content": "</s>"}],
        None,
        {"type": "Metaspace", "replacement": "▁", "prepend_scheme": "always", "split": True},
        _template("<s>", "</s>", 0, 2),
    )
    wordpiece = {
        "type": "WordPiece",
        "unk_token": "[UNK]",
        "continuing_subword_prefix": "##",
        "max_input_chars_per_word": 100,
        "vocab": {"[UNK]": 0, "[CLS]": 1, "[SEP]": 2, "a": 3, "##b": 4},
    }
    ner = _tokenizer_json(
        wordpiece,
        [{"id": 1, "content": "[CLS]"}, {"id": 2, "content": "[SEP]"}],
        BERT_NORMALIZER,
        {"type": "BertPreTokenizer"},
        _template("[CLS]", "[SEP]", 1, 2),
    )
    for name, body in (("bert", bert), ("ner", ner)):
        (tmp_path / name).mkdir()
        (tmp_path / name / "tokenizer.json").write_text(body, encoding="utf-8")
    return tmp_path


def test_build_corpus_shape() -> None:
    corpus = rt.build_corpus()
    assert len(corpus) == rt.CORPUS_SIZE
    assert len({p.id for p in corpus}) == len(corpus)
    categories = {p.category for p in corpus}
    assert categories == {
        "en",
        "code",
        "ko",
        "vi",
        "cjk",
        "ar",
        "emoji",
        "zero_width",
        "nfkc",
        "whitespace",
        "mixed",
        "long",
    }
    text = "".join(p.text for p in corpus)
    for ch in ("\u200d", "\u200b", "\ufeff", "\u0301", "\uff21", "\ufb01", "\t", "\n"):
        assert ch in text
    assert any(p.lang == rt.NER_LANG for p in corpus)


def test_build_corpus_is_deterministic() -> None:
    assert rt.build_corpus() == rt.build_corpus()


def test_committed_corpus_matches_generator(tmp_path: Path) -> None:
    out = tmp_path / rt.CORPUS_FILE
    rt.write_corpus(out, rt.build_corpus())
    committed = rt.DEFAULT_OUT_DIR / rt.CORPUS_FILE
    assert committed.read_bytes() == out.read_bytes()


def test_corpus_round_trip_escapes_invisible_characters(tmp_path: Path) -> None:
    text = "a\u2028b\u200dc\u00a0d \U000e0067"
    prompts = [rt.Prompt(id="x-1", category="c", text=text, lang="en")]
    path = tmp_path / "c.jsonl"
    rt.write_corpus(path, prompts)
    raw = path.read_text(encoding="utf-8")
    assert "\u2028" not in raw
    assert "\u200d" not in raw
    assert "\\udb40\\udc67" in raw
    assert rt.read_corpus(path) == prompts


def test_run_reference_writes_full_ids_with_specials(tmp_path: Path, tokenizer_dir: Path) -> None:
    prompts = [
        rt.Prompt(id="en-1", category="en", text="a a a a a ab", lang="en"),
        rt.Prompt(id="ko-1", category="ko", text="b", lang="ko"),
    ]
    rt.write_corpus(tmp_path / rt.CORPUS_FILE, prompts)
    n_bert, n_ner = rt.run_reference(tmp_path / rt.CORPUS_FILE, tokenizer_dir, tmp_path)
    assert (n_bert, n_ner) == (2, 1)
    bert = [json.loads(x) for x in (tmp_path / "reference_bert.jsonl").read_text().splitlines()]
    ner = [json.loads(x) for x in (tmp_path / "reference_ner.jsonl").read_text().splitlines()]
    # Not truncated to the file's max_length of 4.
    assert bert[0] == {"id": "en-1", "ids": [0, 4, 4, 4, 4, 4, 4, 5, 2]}
    assert bert[1] == {"id": "ko-1", "ids": [0, 3, 5, 2]}  # "\u2581" unknown, then "b"
    assert ner == [{"id": "en-1", "ids": [1, 3, 3, 3, 3, 3, 3, 4, 2]}]


def test_check_long_rejects_short_long_prompt() -> None:
    corpus = [rt.Prompt(id="long-1", category="long", text="x", lang="en")]
    with pytest.raises(ValueError, match="long-1"):
        rt._check_long(corpus, [{"id": "long-1", "ids": [0, 5, 2]}])


def test_ranges() -> None:
    assert rt._ranges([5, 1, 2, 3, 7, 8]) == [(1, 3), (5, 5), (7, 8)]
    assert rt._ranges([]) == []


def test_committed_go_tables_match_probe(tmp_path: Path) -> None:
    # The classes depend only on BertNormalizer/BertPreTokenizer code, not on the vocab, so a
    # tiny tokenizer with the pinned ner configuration reproduces the committed tables.
    body = _tokenizer_json(
        {
            "type": "WordPiece",
            "unk_token": "[UNK]",
            "continuing_subword_prefix": "##",
            "max_input_chars_per_word": 100,
            "vocab": {"[UNK]": 0},
        },
        [],
        BERT_NORMALIZER,
        {"type": "BertPreTokenizer"},
        _template("[UNK]", "[UNK]", 0, 0),
    )
    removed, punct = rt.bert_char_classes(Tokenizer.from_str(body))
    out = tmp_path / "tables.go"
    rt.write_go_tables(out, removed, punct)
    assert out.read_text(encoding="utf-8") == rt.DEFAULT_GO_TABLES.read_text(encoding="utf-8")
