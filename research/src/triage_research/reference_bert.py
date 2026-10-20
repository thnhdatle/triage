# SPDX-License-Identifier: Apache-2.0
"""Reference bert scores for the Go/Python/PyTorch parity checks.

Two backends, both writing one JSON line per prompt
``{"id", "n_tokens", "logits": [l0, l1, l2], "p_strong"}``:

``torch``
    RouteLLM ``BERTRouter`` verbatim (``research/routellm_ref``, lm-sys/RouteLLM @
    0b64fdaf, Apache-2.0) on the checkpoint directory. The pinned files do not include the
    upstream ``tokenizer_config.json``; its relevant values (``model_max_length`` 512,
    ``XLMRobertaTokenizer``, special tokens) are written into a temporary copy of the
    checkpoint directory so that ``AutoTokenizer`` truncates exactly as upstream.
    ``p_strong`` is ``1 - sum(softmax[-2:])`` as RouteLLM computes it.

``onnx``
    ONNX Runtime (the version pinned to the bundled library) on an ONNX file: the weight-free
    graph placed next to ``model.safetensors`` (FP32) or a self-contained INT8 file. Tokens come
    from HF ``tokenizers`` with right truncation to ``--max-tokens`` including ``<s>``/``</s>``
    (this equals ``hftok.Truncate``). ``p_strong`` is ``softmax(logits)[0]`` in float64, which
    is what the Go strategy computes.

Prompts are read from a JSONL file with ``id`` and ``text`` (default: the 300-prompt tokenizer
corpus, ``testdata/tokenizer/corpus.jsonl``).
"""

from __future__ import annotations

import argparse
import json
import sys
import tempfile
from collections.abc import Iterable, Iterator
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np
import onnx
from tokenizers import Tokenizer

from triage_research import build_bert_graph as bbg

DEFAULT_PROMPTS = bbg.REPO_ROOT / "testdata" / "tokenizer" / "corpus.jsonl"
DEFAULT_TOKENIZER = Path("~/.cache/rayld/triage/tokenizers/bert/tokenizer.json")
TOKENIZER_FILE = "tokenizer.json"
MAX_TOKENS = 512
# Values of tokenizer_config.json in routellm/bert_gpt4_augmented @ 86237e3d (not allow-listed,
# so not downloaded; only the fields AutoTokenizer needs to reproduce upstream truncation).
UPSTREAM_TOKENIZER_CONFIG: dict[str, Any] = {
    "bos_token": "<s>",
    "clean_up_tokenization_spaces": True,
    "cls_token": "<s>",
    "eos_token": "</s>",
    "mask_token": "<mask>",
    "model_max_length": MAX_TOKENS,
    "pad_token": "<pad>",
    "sep_token": "</s>",
    "tokenizer_class": "XLMRobertaTokenizer",
    "unk_token": "<unk>",
}


@dataclass(frozen=True)
class Prompt:
    """One prompt to score."""

    id: str
    text: str


@dataclass(frozen=True)
class Scored:
    """One reference score."""

    id: str
    n_tokens: int
    logits: tuple[float, float, float]
    p_strong: float

    def as_json(self) -> str:
        """Serialise with full float precision."""
        return json.dumps(
            {
                "id": self.id,
                "n_tokens": self.n_tokens,
                "logits": list(self.logits),
                "p_strong": self.p_strong,
            }
        )


def read_prompts(path: Path, limit: int | None = None) -> list[Prompt]:
    """Read ``{"id", "text"}`` lines."""
    out: list[Prompt] = []
    with path.open(encoding="utf-8") as fh:
        for line in fh:
            if line.strip():
                row = json.loads(line)
                out.append(Prompt(id=str(row["id"]), text=str(row["text"])))
    return out[:limit] if limit is not None else out


def encode(tokenizer: Tokenizer, text: str, max_tokens: int = MAX_TOKENS) -> list[int]:
    """Ids with ``<s>``/``</s>``, right-truncated to ``max_tokens`` keeping the final ``</s>``."""
    ids: list[int] = tokenizer.encode(text).ids
    if len(ids) > max_tokens:
        ids = [*ids[: max_tokens - 1], ids[-1]]
    return ids


def load_tokenizer(path: Path) -> Tokenizer:
    """Load ``tokenizer.json`` with its own truncation and padding disabled."""
    tok = Tokenizer.from_file(str(path.expanduser()))
    tok.no_truncation()
    tok.no_padding()
    return tok


def onnx_session(model_path: Path) -> Any:
    """An ONNX Runtime CPU session; deterministic single-threaded execution."""
    import onnxruntime as ort  # noqa: PLC0415

    opts = ort.SessionOptions()
    opts.intra_op_num_threads = 1
    opts.inter_op_num_threads = 1
    return ort.InferenceSession(str(model_path), opts, providers=["CPUExecutionProvider"])


def score_onnx(
    model_path: Path, tokenizer_path: Path, prompts: Iterable[Prompt], max_tokens: int = MAX_TOKENS
) -> Iterator[Scored]:
    """Score prompts with ONNX Runtime, batch 1, all-ones attention mask."""
    tok = load_tokenizer(tokenizer_path)
    sess = onnx_session(model_path)
    for prompt in prompts:
        ids = np.asarray([encode(tok, prompt.text, max_tokens)], dtype=np.int64)
        (logits,) = sess.run(
            [bbg.LOGITS], {bbg.INPUT_IDS: ids, bbg.ATTENTION_MASK: np.ones_like(ids)}
        )
        row = [float(v) for v in logits[0]]
        yield Scored(prompt.id, int(ids.shape[1]), (row[0], row[1], row[2]), bbg.softmax0(row))


def score_onnx_model(
    model_path: Path, checkpoint_dir: Path | None, tokenizer_path: Path, prompts: list[Prompt]
) -> list[Scored]:
    """Score with ``model_path``; a weight-free graph is placed next to the checkpoint first."""
    proto = onnx.load(str(model_path), load_external_data=False)
    external = any(i.data_location == onnx.TensorProto.EXTERNAL for i in proto.graph.initializer)
    if not external:
        return list(score_onnx(model_path, tokenizer_path, prompts))
    if checkpoint_dir is None:
        raise ValueError(f"{model_path} has external data: --checkpoint-dir is required")
    with bbg.placed(proto, checkpoint_dir, "reference") as path:
        return list(score_onnx(path, tokenizer_path, prompts))


def score_torch(
    checkpoint_dir: Path, tokenizer_path: Path, prompts: Iterable[Prompt]
) -> list[Scored]:
    """Score prompts with RouteLLM ``BERTRouter`` (vendored, verbatim)."""
    import torch  # noqa: PLC0415
    from routellm_ref.routers import BERTRouter  # noqa: PLC0415

    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp)
        for name in (bbg.CONFIG_FILE, bbg.WEIGHTS_FILE):
            (root / name).symlink_to((checkpoint_dir / name).resolve())
        (root / TOKENIZER_FILE).symlink_to(tokenizer_path.expanduser().resolve())
        (root / "tokenizer_config.json").write_text(json.dumps(UPSTREAM_TOKENIZER_CONFIG))
        router = BERTRouter(str(root))  # type: ignore[no-untyped-call]
        out: list[Scored] = []
        for prompt in prompts:
            # The same call BERTRouter.calculate_strong_win_rate makes, kept to report logits.
            inputs = router.tokenizer(
                prompt.text, return_tensors="pt", padding=True, truncation=True
            )
            with torch.no_grad():
                row = [float(v) for v in router.model(**inputs).logits.numpy()[0]]
            p = float(router.calculate_strong_win_rate(prompt.text))  # type: ignore[no-untyped-call]
            n = int(inputs["input_ids"].shape[1])
            out.append(Scored(prompt.id, n, (row[0], row[1], row[2]), p))
        return out


def write_jsonl(path: Path | None, rows: Iterable[Scored]) -> None:
    """Write rows to ``path`` or stdout."""
    lines = "".join(row.as_json() + "\n" for row in rows)
    if path is None:
        sys.stdout.write(lines)
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(lines, encoding="utf-8")


def main(argv: list[str] | None = None) -> int:
    """CLI: ``python -m triage_research.reference_bert --backend torch|onnx ...``."""
    parser = argparse.ArgumentParser(prog="triage-reference-bert", description=__doc__)
    parser.add_argument("--backend", choices=("torch", "onnx"), required=True)
    parser.add_argument("--checkpoint-dir", type=Path, default=None)
    parser.add_argument("--model", type=Path, default=None, help="ONNX file (onnx backend)")
    parser.add_argument("--tokenizer", type=Path, default=DEFAULT_TOKENIZER)
    parser.add_argument("--prompts", type=Path, default=DEFAULT_PROMPTS)
    parser.add_argument("--limit", type=int, default=None)
    parser.add_argument("--out", type=Path, default=None)
    args = parser.parse_args(argv)
    prompts = read_prompts(args.prompts, args.limit)
    ckpt = args.checkpoint_dir.expanduser() if args.checkpoint_dir else None
    if args.backend == "torch":
        rows = score_torch(ckpt or bbg.default_checkpoint_dir(), args.tokenizer, prompts)
    else:
        model = args.model or bbg.DEFAULT_OUT
        rows = score_onnx_model(
            model, ckpt or bbg.default_checkpoint_dir(), args.tokenizer, prompts
        )
    write_jsonl(args.out, rows)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
