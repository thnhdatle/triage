# SPDX-License-Identifier: Apache-2.0
"""Create the seeded random-init model fixtures in ``testdata/models``.

Nothing here is derived from real weights. Output (contract in ``testdata/models/README.md``):

``bert-tiny/``
    ``config.json`` (XLM-R, 1 layer, hidden size 8, 2 heads, intermediate 16, full 250,002
    vocabulary), ``model.safetensors`` (seeded N(0, 0.5) values for every parameter),
    ``graph.onnx`` (weight-free graph built exactly like ``models/bert/graph.onnx``, external
    data into ``model.safetensors``), ``model.int8.onnx`` (``export_bert``, default scheme) and
    ``reference.jsonl`` (one line per prompt of ``testdata/tokenizer/corpus.jsonl``:
    ``{"id", "n_tokens", "p_torch", "p_fp32", "p_int8", "logits_fp32"}``).
``mf-tiny/``
    ``model.safetensors`` with the real tensor names and shapes (``P.weight`` [64, 128],
    ``text_proj.0.weight`` [128, 1536], ``classifier.0.weight`` [1, 128]; seeded N(0, 1)) and
    ``reference.json`` (``reference_mf`` on ``embeddings/``).
``embeddings/``
    ``record_embeddings`` output; written (synthetic) only if absent.

The XLM-R ``tokenizer.json`` is not committed (17 MB): it is the file cached by
``make bootstrap`` (``~/.cache/rayld/triage/tokenizers/bert/tokenizer.json``), checked against
``fixtures.xlmr_tokenizer_sha256`` in ``models/manifest.yaml``.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

import numpy as np
import yaml
from safetensors.numpy import save_file

from triage_research import build_bert_graph as bbg
from triage_research import export_bert, fetch, reference_mf
from triage_research import record_embeddings as rec
from triage_research import reference_bert as rb

DEFAULT_OUT = bbg.REPO_ROOT / "testdata" / "models"
BERT_SEED = 7
BERT_STD = 1.5
MF_SEED = 11
MF_SHAPES = {
    reference_mf.P_KEY: (64, 128),
    reference_mf.PROJ_KEY: (128, 1536),
    reference_mf.CLS_KEY: (1, 128),
}
TINY_CONFIG: dict[str, Any] = {
    "architectures": ["XLMRobertaForSequenceClassification"],
    "attention_probs_dropout_prob": 0.1,
    "bos_token_id": 0,
    "classifier_dropout": None,
    "eos_token_id": 2,
    "hidden_act": "gelu",
    "hidden_dropout_prob": 0.1,
    "hidden_size": 8,
    "id2label": {"0": "LABEL_0", "1": "LABEL_1", "2": "LABEL_2"},
    "initializer_range": 0.02,
    "intermediate_size": 16,
    "label2id": {"LABEL_0": 0, "LABEL_1": 1, "LABEL_2": 2},
    "layer_norm_eps": 1e-05,
    "max_position_embeddings": 514,
    "model_type": "xlm-roberta",
    "num_attention_heads": 2,
    "num_hidden_layers": 1,
    "pad_token_id": 1,
    "position_embedding_type": "absolute",
    "problem_type": "single_label_classification",
    "torch_dtype": "float32",
    "type_vocab_size": 1,
    "vocab_size": 250002,
}


def tokenizer_path() -> Path:
    """The cached XLM-R tokenizer.json, verified against the manifest."""
    path = rb.DEFAULT_TOKENIZER.expanduser()
    raw = yaml.safe_load(fetch.DEFAULT_MANIFEST.read_text(encoding="utf-8"))
    want = raw["fixtures"]["xlmr_tokenizer_sha256"]
    if not path.is_file() or fetch.sha256_file(path) != want:
        raise SystemExit(f"{path}: missing or not sha256 {want}; run make bootstrap")
    return path


def make_bert_tiny(out: Path, tok: Path) -> None:
    """Write the bert-tiny fixture."""
    out.mkdir(parents=True, exist_ok=True)
    (out / bbg.CONFIG_FILE).write_text(json.dumps(TINY_CONFIG, indent=2) + "\n", encoding="utf-8")
    model = bbg.skeleton(bbg.load_config(out / bbg.CONFIG_FILE), seed=BERT_SEED, std=BERT_STD)
    state = {name: p.detach().numpy().copy() for name, p in model.named_parameters()}
    save_file(state, str(out / bbg.WEIGHTS_FILE))
    source = f"testdata bert-tiny (seeded random init, seed {BERT_SEED}, std {BERT_STD})"
    graph = bbg.build_graph(out, source).model
    (out / "graph.onnx").write_bytes(graph.SerializeToString())
    bbg.verify(graph, out)
    export_bert.quantize(graph, out, out / export_bert.DEFAULT_OUT_NAME)

    prompts = rb.read_prompts(rb.DEFAULT_PROMPTS)
    torch_rows = rb.score_torch(out, tok, prompts)
    fp32 = rb.score_onnx_model(out / "graph.onnx", out, tok, prompts)
    int8 = rb.score_onnx_model(out / export_bert.DEFAULT_OUT_NAME, out, tok, prompts)
    lines = []
    for t, f, q in zip(torch_rows, fp32, int8, strict=True):
        if not t.id == f.id == q.id or not t.n_tokens == f.n_tokens == q.n_tokens:
            raise AssertionError(f"reference rows disagree on {t.id}")
        row = {
            "id": t.id,
            "n_tokens": t.n_tokens,
            "p_torch": t.p_strong,
            "p_fp32": f.p_strong,
            "p_int8": q.p_strong,
            "logits_fp32": list(f.logits),
        }
        lines.append(json.dumps(row) + "\n")
    (out / "reference.jsonl").write_text("".join(lines), encoding="utf-8")


def make_mf_tiny(out: Path, embeddings: Path) -> None:
    """Write the mf-tiny fixture."""
    out.mkdir(parents=True, exist_ok=True)
    rng = np.random.default_rng(MF_SEED)
    state = {k: rng.standard_normal(shape).astype(np.float32) for k, shape in MF_SHAPES.items()}
    save_file(state, str(out / reference_mf.WEIGHTS_FILE))
    doc = reference_mf.reference(out, embeddings)
    (out / "reference.json").write_text(json.dumps(doc, indent=1) + "\n", encoding="utf-8")


def main(argv: list[str] | None = None) -> int:
    """CLI: ``python -m triage_research.make_fixtures [--out DIR]``."""
    parser = argparse.ArgumentParser(prog="triage-make-fixtures", description=__doc__)
    parser.add_argument("--out", type=Path, default=DEFAULT_OUT)
    args = parser.parse_args(argv)
    tok = tokenizer_path()
    emb = args.out / "embeddings"
    if not (emb / "embeddings.safetensors").is_file():
        rec.write_synthetic(emb)
    make_bert_tiny(args.out / "bert-tiny", tok)
    make_mf_tiny(args.out / "mf-tiny", emb)
    for path in sorted(p for p in args.out.rglob("*") if p.is_file()):
        sys.stdout.write(f"{fetch.sha256_file(path)}  {path.relative_to(args.out)}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
