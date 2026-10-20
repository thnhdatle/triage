# SPDX-License-Identifier: Apache-2.0
"""Reference mf scores for the Go/PyTorch parity check.

Two computations on the same embeddings:

- ``p_mfmodel``: RouteLLM ``MatrixFactorizationRouter`` verbatim (``research/routellm_ref``,
  lm-sys/RouteLLM @ 0b64fdaf, Apache-2.0; strong ``gpt-4-1106-preview`` = row 24, weak
  ``mixtral-8x7b-instruct-v0.1`` = row 36, ``hidden_size`` 128, ``num_models`` 64,
  ``text_dim`` 1536), with the OpenAI embedding call replaced by the recorded vectors;
- ``p_strong``: the closed form the Go strategy uses, ``sigmoid(u . e)`` with
  ``u = W_proj^T (c * (P^[24] - P^[36]))`` where ``P^`` are the L2-normalised rows of
  ``P.weight``, ``c = classifier.0.weight[0]`` and ``W_proj = text_proj.0.weight``
  (computed in float64).

Output JSON: ``{"strong_row", "weak_row", "u": [1536 floats], "scores": [{"id", "p_mfmodel",
"p_strong"}]}``.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

import numpy as np
from safetensors.numpy import load_file

from triage_research import build_bert_graph as bbg
from triage_research import record_embeddings as rec

STRONG_MODEL = "gpt-4-1106-preview"
WEAK_MODEL = "mixtral-8x7b-instruct-v0.1"
STRONG_ROW = 24
WEAK_ROW = 36
P_KEY = "P.weight"
PROJ_KEY = "text_proj.0.weight"
CLS_KEY = "classifier.0.weight"
WEIGHTS_FILE = "model.safetensors"
_EPS = 1e-12  # torch.nn.functional.normalize default


def derive_u(state: dict[str, np.ndarray], strong: int = STRONG_ROW, weak: int = WEAK_ROW) -> Any:
    """u = W_proj^T (c * (P^[strong] - P^[weak])) in float64."""
    p = state[P_KEY].astype(np.float64)
    rows = p / np.maximum(np.linalg.norm(p, axis=1, keepdims=True), _EPS)
    c = state[CLS_KEY].astype(np.float64)[0]
    proj = state[PROJ_KEY].astype(np.float64)
    return proj.T @ (c * (rows[strong] - rows[weak]))


def p_closed_form(u: Any, embeddings: np.ndarray) -> list[float]:
    """sigmoid(u . e) per row, float64."""
    z = embeddings.astype(np.float64) @ u
    return [float(v) for v in 1.0 / (1.0 + np.exp(-z))]


def p_mfmodel(checkpoint_dir: Path, embeddings: np.ndarray) -> list[float]:
    """RouteLLM MatrixFactorizationRouter on recorded embeddings."""
    from routellm_ref.matrix_factorization import model as mf_model  # noqa: PLC0415
    from routellm_ref.routers import MatrixFactorizationRouter  # noqa: PLC0415

    router = MatrixFactorizationRouter(  # type: ignore[no-untyped-call]
        str(checkpoint_dir), strong_model=STRONG_MODEL, weak_model=WEAK_MODEL
    )
    if (router.strong_model_id, router.weak_model_id) != (STRONG_ROW, WEAK_ROW):
        raise AssertionError("RouteLLM MODEL_IDS rows changed")
    out: list[float] = []
    for i, vec in enumerate(embeddings):
        key = f"embedding-{i}"
        mf_model.RECORDED_EMBEDDINGS[key] = [float(v) for v in vec]
        out.append(float(router.calculate_strong_win_rate(key)))  # type: ignore[no-untyped-call]
        del mf_model.RECORDED_EMBEDDINGS[key]
    return out


def reference(checkpoint_dir: Path, embeddings_dir: Path) -> dict[str, Any]:
    """Compute the reference document."""
    vecs, meta = rec.load(embeddings_dir)
    u = derive_u(load_file(str(checkpoint_dir / WEIGHTS_FILE)))
    closed = p_closed_form(u, vecs)
    torch_p = p_mfmodel(checkpoint_dir, vecs)
    scores = [
        {"id": meta["ids"][i], "p_mfmodel": torch_p[i], "p_strong": closed[i]}
        for i in range(len(vecs))
    ]
    return {
        "strong_row": STRONG_ROW,
        "weak_row": WEAK_ROW,
        "u": [float(v) for v in u],
        "scores": scores,
    }


def main(argv: list[str] | None = None) -> int:
    """CLI: ``python -m triage_research.reference_mf [--checkpoint-dir D] [--out F]``."""
    parser = argparse.ArgumentParser(prog="triage-reference-mf", description=__doc__)
    parser.add_argument("--checkpoint-dir", type=Path, default=None)
    parser.add_argument("--embeddings", type=Path, default=rec.DEFAULT_OUT)
    parser.add_argument("--out", type=Path, default=None)
    args = parser.parse_args(argv)
    ckpt = (args.checkpoint_dir or bbg.default_checkpoint_dir("mf")).expanduser()
    doc = reference(ckpt, args.embeddings)
    worst = max(abs(s["p_mfmodel"] - s["p_strong"]) for s in doc["scores"])
    sys.stderr.write(f"closed form vs MFModel: max |dp| = {worst:.3g}\n")
    text = json.dumps(doc, indent=1) + "\n"
    if args.out is None:
        sys.stdout.write(text)
    else:
        args.out.write_text(text, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
