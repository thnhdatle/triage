# SPDX-License-Identifier: Apache-2.0
"""Write the 200-vector embedding fixture ``testdata/models/embeddings``.

With ``OPENAI_API_KEY`` set and ``--record``, the first 200 prompts of the tokenizer corpus are
embedded once with ``text-embedding-3-small`` (``dimensions: 1536``). Otherwise (the default,
and the state committed in this repository) the vectors are seeded Gaussian vectors normalised
to unit L2 norm, which is enough for the parity checks; ``meta.json`` says so.

Files:

- ``embeddings.safetensors``: one F32 tensor ``embeddings`` of shape ``[200, 1536]``;
- ``meta.json``: ``{"model", "dimensions", "count", "synthetic", "seed", "ids"}``; ``ids[i]``
  names row ``i`` (a corpus id when recorded, ``synthetic-NNN`` otherwise).
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from pathlib import Path
from typing import Any

import httpx
import numpy as np
from safetensors.numpy import save_file

from triage_research import reference_bert as rb

REPO_ROOT = Path(__file__).resolve().parents[3]
DEFAULT_OUT = REPO_ROOT / "testdata" / "models" / "embeddings"
MODEL = "text-embedding-3-small"
DIMENSIONS = 1536
COUNT = 200
SEED = 20261019
TENSOR = "embeddings"
_API = "https://api.openai.com/v1/embeddings"


def synthetic(count: int = COUNT, dim: int = DIMENSIONS, seed: int = SEED) -> np.ndarray:
    """Seeded Gaussian vectors with unit L2 norm (normalised in float64, stored as float32)."""
    rng = np.random.default_rng(seed)
    vecs = rng.standard_normal((count, dim))
    vecs /= np.linalg.norm(vecs, axis=1, keepdims=True)
    return vecs.astype(np.float32)


def record(texts: list[str], api_key: str) -> np.ndarray:
    """Embed ``texts`` with the OpenAI API (one request per text, in order)."""
    rows: list[list[float]] = []
    with httpx.Client(timeout=60.0) as client:
        for text in texts:
            resp = client.post(
                _API,
                headers={"Authorization": f"Bearer {api_key}"},
                json={"model": MODEL, "input": [text], "dimensions": DIMENSIONS},
            )
            resp.raise_for_status()
            rows.append(resp.json()["data"][0]["embedding"])
    return np.asarray(rows, dtype=np.float32)


def write(out_dir: Path, vecs: np.ndarray, meta: dict[str, Any]) -> None:
    """Write ``embeddings.safetensors`` and ``meta.json``."""
    out_dir.mkdir(parents=True, exist_ok=True)
    save_file({TENSOR: np.ascontiguousarray(vecs)}, str(out_dir / "embeddings.safetensors"))
    (out_dir / "meta.json").write_text(json.dumps(meta, indent=2) + "\n", encoding="utf-8")


def write_synthetic(out_dir: Path = DEFAULT_OUT) -> None:
    """Write the synthetic fixture."""
    meta = {
        "model": MODEL,
        "dimensions": DIMENSIONS,
        "count": COUNT,
        "synthetic": True,
        "seed": SEED,
        "ids": [f"synthetic-{i:03d}" for i in range(COUNT)],
    }
    write(out_dir, synthetic(), meta)


def load(out_dir: Path = DEFAULT_OUT) -> tuple[np.ndarray, dict[str, Any]]:
    """Read the fixture back."""
    from safetensors.numpy import load_file  # noqa: PLC0415

    vecs = load_file(str(out_dir / "embeddings.safetensors"))[TENSOR]
    meta: dict[str, Any] = json.loads((out_dir / "meta.json").read_text(encoding="utf-8"))
    return vecs, meta


def main(argv: list[str] | None = None) -> int:
    """CLI: ``python -m triage_research.record_embeddings [--record] [--out DIR]``."""
    parser = argparse.ArgumentParser(prog="triage-record-embeddings", description=__doc__)
    parser.add_argument("--out", type=Path, default=DEFAULT_OUT)
    parser.add_argument("--record", action="store_true", help="call the OpenAI API")
    args = parser.parse_args(argv)
    key = os.environ.get("OPENAI_API_KEY", "")
    if not args.record:
        write_synthetic(args.out)
        sys.stdout.write(f"wrote synthetic embeddings to {args.out}\n")
        return 0
    if not key:
        sys.stderr.write("record_embeddings: --record needs OPENAI_API_KEY\n")
        return 1
    prompts = rb.read_prompts(rb.DEFAULT_PROMPTS, COUNT)
    vecs = record([p.text for p in prompts], key)
    meta = {
        "model": MODEL,
        "dimensions": DIMENSIONS,
        "count": len(prompts),
        "synthetic": False,
        "seed": None,
        "ids": [p.id for p in prompts],
    }
    write(args.out, vecs, meta)
    sys.stdout.write(f"recorded {len(prompts)} embeddings to {args.out}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
