# SPDX-License-Identifier: Apache-2.0
"""Build the weight-free bert graph from the checkpoint."""

from __future__ import annotations

import argparse
import contextlib
import io
import json
import os
import struct
import sys
import warnings
from collections.abc import Iterator, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np
import onnx
import torch
from onnx import TensorProto
from transformers import XLMRobertaConfig, XLMRobertaForSequenceClassification

from triage_research import fetch

REPO_ROOT = Path(__file__).resolve().parents[3]
DEFAULT_OUT = REPO_ROOT / "models" / "bert" / "graph.onnx"
CONVERTER = "build_bert_graph.py (triage-research 0.1.0)"
OPSET = 17
MAX_IR_VERSION = 10
WEIGHTS_FILE = "model.safetensors"
CONFIG_FILE = "config.json"
INPUT_IDS = "input_ids"
ATTENTION_MASK = "attention_mask"
LOGITS = "logits"
EXPORT_SEED = 1
_HEADER_LEN = 8
_MAX_HEADER = 100 << 20
_ONNX_FLOAT = {"F32": TensorProto.FLOAT}
_P_STRONG = "softmax(logits)[0]"
_VERIFY_LENGTHS = (2, 3, 17, 128, 512)
_PARITY_TOL = 1e-5


class MappingError(Exception):
    """An initializer does not match the checkpoint tensor of the same name."""


@dataclass(frozen=True)
class TensorInfo:
    dtype: str
    shape: tuple[int, ...]
    offset: int
    length: int


@dataclass(frozen=True)
class BuildResult:
    model: onnx.ModelProto
    mapped: tuple[str, ...]
    embedded: tuple[str, ...]
    unused: tuple[str, ...]


def read_safetensors_header(path: Path) -> dict[str, TensorInfo]:
    with path.open("rb") as fh:
        raw_len = fh.read(_HEADER_LEN)
        if len(raw_len) != _HEADER_LEN:
            raise MappingError(f"{path}: truncated safetensors header")
        (header_len,) = struct.unpack("<Q", raw_len)
        if header_len > _MAX_HEADER:
            raise MappingError(f"{path}: safetensors header of {header_len} bytes")
        header: dict[str, Any] = json.loads(fh.read(header_len))
    base = _HEADER_LEN + header_len
    out: dict[str, TensorInfo] = {}
    for name, meta in header.items():
        if name == "__metadata__":
            continue
        begin, end = (int(v) for v in meta["data_offsets"])
        out[name] = TensorInfo(
            dtype=str(meta["dtype"]),
            shape=tuple(int(d) for d in meta["shape"]),
            offset=base + begin,
            length=end - begin,
        )
    return out


def load_config(config_path: Path) -> XLMRobertaConfig:
    raw = json.loads(config_path.read_text(encoding="utf-8"))
    config = XLMRobertaConfig.from_dict(raw, attn_implementation="eager")
    if not isinstance(config, XLMRobertaConfig):
        raise TypeError("unexpected XLMRobertaConfig.from_dict result")
    return config


def skeleton(config: XLMRobertaConfig, seed: int = EXPORT_SEED, std: float = 1.0) -> Any:
    model = XLMRobertaForSequenceClassification(config).eval()  # type: ignore[no-untyped-call]
    gen = torch.Generator().manual_seed(seed)
    with torch.no_grad():
        for _, param in model.named_parameters():
            param.copy_(torch.randn(param.shape, generator=gen) * std)
    return model


def export(model: Any) -> onnx.ModelProto:
    ids = torch.tensor([[0, 6, 5, 2]], dtype=torch.int64)
    mask = torch.ones_like(ids)
    buf = io.BytesIO()
    dyn = {0: "batch", 1: "sequence"}
    with warnings.catch_warnings():
        warnings.simplefilter("ignore")
        torch.onnx.export(
            model,
            (ids, mask),
            buf,  # type: ignore[arg-type]
            dynamo=False,
            input_names=[INPUT_IDS, ATTENTION_MASK],
            output_names=[LOGITS],
            dynamic_axes={INPUT_IDS: dyn, ATTENTION_MASK: dyn, LOGITS: {0: "batch"}},
            opset_version=OPSET,
            do_constant_folding=False,
            export_params=True,
        )
    proto = onnx.load_from_string(buf.getvalue())
    proto.ir_version = min(proto.ir_version, MAX_IR_VERSION)
    return proto


def _initializer_nbytes(init: TensorProto) -> int:
    if init.raw_data:
        return len(init.raw_data)
    return 4 * len(init.float_data)


def externalize(
    proto: onnx.ModelProto,
    header: dict[str, TensorInfo],
    parameters: Sequence[str],
    location: str = WEIGHTS_FILE,
) -> BuildResult:
    params = set(parameters)
    mapped: list[str] = []
    embedded: list[str] = []
    for init in proto.graph.initializer:
        info = header.get(init.name)
        if info is None:
            if init.name in params:
                raise MappingError(f"parameter {init.name} has no checkpoint tensor")
            embedded.append(init.name)
            continue
        if _ONNX_FLOAT.get(info.dtype) != init.data_type:
            raise MappingError(f"{init.name}: dtype {info.dtype} vs ONNX type {init.data_type}")
        if tuple(init.dims) != info.shape:
            raise MappingError(f"{init.name}: shape {info.shape} vs ONNX {tuple(init.dims)}")
        if _initializer_nbytes(init) != info.length:
            raise MappingError(
                f"{init.name}: {info.length} bytes vs ONNX {_initializer_nbytes(init)}"
            )
        init.ClearField("raw_data")
        init.ClearField("float_data")
        init.data_location = TensorProto.EXTERNAL
        del init.external_data[:]
        for key, value in (
            ("location", location),
            ("offset", info.offset),
            ("length", info.length),
        ):
            entry = init.external_data.add()
            entry.key = key
            entry.value = str(value)
        mapped.append(init.name)
    missing = params - set(mapped)
    if missing:
        raise MappingError(f"parameters not exported as named initializers: {sorted(missing)}")
    unused = tuple(sorted(set(header) - set(mapped)))
    return BuildResult(proto, tuple(mapped), tuple(embedded), unused)


def set_metadata(proto: onnx.ModelProto, values: dict[str, str]) -> None:
    del proto.metadata_props[:]
    for key in sorted(values):
        prop = proto.metadata_props.add()
        prop.key = key
        prop.value = values[key]


def metadata(proto: onnx.ModelProto) -> dict[str, str]:
    return {p.key: p.value for p in proto.metadata_props}


def build_graph(checkpoint_dir: Path, source: str) -> BuildResult:
    weights = checkpoint_dir / WEIGHTS_FILE
    header = read_safetensors_header(weights)
    model = skeleton(load_config(checkpoint_dir / CONFIG_FILE))
    names = [name for name, _ in model.named_parameters()]
    result = externalize(export(model), header, names)
    set_metadata(
        result.model,
        {
            "triage.converter": CONVERTER,
            "triage.p_strong": _P_STRONG,
            "triage.source": source,
            "triage.weights.file": WEIGHTS_FILE,
            "triage.weights.sha256": fetch.sha256_file(weights),
            "triage.weights.size": str(weights.stat().st_size),
        },
    )
    return result


@contextlib.contextmanager
def placed(proto: onnx.ModelProto, checkpoint_dir: Path, tag: str) -> Iterator[Path]:
    path = checkpoint_dir / f".{tag}-{os.getpid()}.onnx"
    try:
        path.write_bytes(proto.SerializeToString())
        yield path
    finally:
        path.unlink(missing_ok=True)


def softmax0(logits: Sequence[float]) -> float:
    arr = np.asarray(logits, dtype=np.float64)
    exp = np.exp(arr - arr.max())
    return float(exp[0] / exp.sum())


def verify(proto: onnx.ModelProto, checkpoint_dir: Path) -> float:
    """Compare ONNX Runtime on the placed graph with PyTorch on the checkpoint weights."""
    import onnxruntime as ort  # noqa: PLC0415

    model = XLMRobertaForSequenceClassification.from_pretrained(
        str(checkpoint_dir), attn_implementation="eager"
    ).eval()  # type: ignore[no-untyped-call]
    vocab = int(model.config.vocab_size)
    rng = np.random.default_rng(0)
    worst = 0.0
    with placed(proto, checkpoint_dir, "graph-verify") as path:
        sess = ort.InferenceSession(str(path), providers=["CPUExecutionProvider"])
        for length in _VERIFY_LENGTHS:
            ids = rng.integers(3, vocab, size=(2, length), dtype=np.int64)
            ids[:, 0], ids[:, -1] = 0, 2
            mask = np.ones_like(ids)
            short = max(2, length // 2)
            ids[1, short:] = 1
            ids[1, short - 1] = 2
            mask[1, short:] = 0
            (got,) = sess.run([LOGITS], {INPUT_IDS: ids, ATTENTION_MASK: mask})
            with torch.no_grad():
                want = model(torch.from_numpy(ids), attention_mask=torch.from_numpy(mask)).logits
            for row in range(2):
                diff = abs(softmax0(got[row]) - softmax0(want[row].tolist()))
                worst = max(worst, diff)
    if worst > _PARITY_TOL:
        raise AssertionError(f"ONNX Runtime vs PyTorch max |dp| {worst:.3g} > {_PARITY_TOL}")
    return worst


def default_checkpoint_dir(name: str = "bert") -> Path:
    entry = fetch.load_manifest()[name]
    return (fetch.DEFAULT_CACHE_DIR / entry.name / entry.revision).expanduser()


def default_source(name: str = "bert") -> str:
    entry = fetch.load_manifest()[name]
    return f"{entry.hf_repo}@{entry.revision}"


def main(argv: list[str] | None = None) -> int:
    """CLI: build (or ``--check``) ``models/bert/graph.onnx`` from a fetched checkpoint."""
    parser = argparse.ArgumentParser(prog="triage-build-bert-graph", description=__doc__)
    parser.add_argument("--checkpoint-dir", type=Path, default=None)
    parser.add_argument("--source", default=None, help="triage.source metadata value")
    parser.add_argument("--out", type=Path, default=DEFAULT_OUT)
    parser.add_argument("--no-verify", action="store_true", help="skip the ORT vs torch check")
    parser.add_argument(
        "--check", action="store_true", help="fail if --out differs from a fresh build"
    )
    args = parser.parse_args(argv)
    ckpt = (args.checkpoint_dir or default_checkpoint_dir()).expanduser()
    result = build_graph(ckpt, args.source or default_source())
    data = result.model.SerializeToString()
    sys.stdout.write(
        f"mapped {len(result.mapped)} initializers, embedded {len(result.embedded)} constants, "
        f"unused checkpoint tensors: {list(result.unused)}\n"
    )
    if args.check:
        same = args.out.is_file() and args.out.read_bytes() == data
        sys.stdout.write(f"{args.out}: {'up to date' if same else 'DIFFERS'}\n")
        return 0 if same else 1
    if not args.no_verify:
        worst = verify(result.model, ckpt)
        sys.stdout.write(f"ORT vs torch on the checkpoint: max |dp| = {worst:.3g}\n")
    args.out.parent.mkdir(parents=True, exist_ok=True)
    tmp = args.out.with_name(args.out.name + ".part")
    tmp.write_bytes(data)
    tmp.replace(args.out)
    sys.stdout.write(f"wrote {args.out} ({len(data)} bytes)\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
