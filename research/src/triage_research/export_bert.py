# SPDX-License-Identifier: Apache-2.0
"""Derive the INT8 bert ONNX model.

This is the converter that ``triage models pull bert --int8`` runs (through ``uvx`` or the
``triage-models`` image). It never trains and never downloads anything:

1. take the weight-free FP32 graph (``--graph``, default the committed
   ``models/bert/graph.onnx`` when present, otherwise the same graph rebuilt in memory from
   ``config.json`` by ``build_bert_graph``; opset 17, ``ir_version`` <= 10);
2. check that its ``triage.weights.sha256`` equals the SHA-256 of
   ``<checkpoint-dir>/model.safetensors`` (so every external-data offset is valid);
3. load the weights and fold each ``Transpose(weight)`` into a pre-transposed initializer
   (exact; the graph is exported without constant folding, so without this no MatMul has a
   constant weight);
4. quantize with ``--scheme``:

   ``blockwise`` (default)
       every weight MatMul becomes ``com.microsoft.MatMulNBits`` with 8-bit symmetric weights
       in blocks of 32 along K and ``accuracy_level`` 4 (activations quantized to int8 per
       block at run time); the embedding Gathers are quantized with ``quantize_dynamic``
       (INT8, per tensor);
   ``dynamic``
       ``quantize_dynamic`` on every weight MatMul and embedding Gather (INT8 weights per
       tensor, uint8 activations per tensor). Kept for comparison: on the real checkpoint it
       agrees with PyTorch on only 91.7% / 88.7% of routing decisions at strong ratios
       0.3 / 0.5 (the parity check needs 97%);

5. write ``--out`` atomically (``ir_version`` <= 10; the graph's metadata plus
   ``triage.variant=int8`` and ``triage.int8.scheme``).

The output is a single self-contained ONNX file with the same input/output contract as the
FP32 graph. The last stdout line is ``<sha256>  <path>``.
"""

from __future__ import annotations

import argparse
import logging
import os
import sys
import tempfile
from pathlib import Path

import numpy as np
import onnx
from onnx import numpy_helper
from onnx.external_data_helper import load_external_data_for_model
from onnxruntime.quantization import QuantType, quantize_dynamic

from triage_research import build_bert_graph as bbg
from triage_research import fetch

DEFAULT_OUT_NAME = "model.int8.onnx"
CONVERTER = "export_bert.py (triage-research 0.1.0)"
SCHEMES = ("blockwise", "dynamic")
DEFAULT_SCHEME = "blockwise"
BLOCK_SIZE = 32
ACCURACY_LEVEL = 4


class ExportError(Exception):
    """The graph does not belong to the checkpoint, or the output is invalid."""


def load_graph(graph: Path | None, checkpoint_dir: Path) -> onnx.ModelProto:
    """Return the FP32 graph: ``graph`` if given or committed, else a fresh in-memory build."""
    path = graph if graph is not None else bbg.DEFAULT_OUT
    if path.is_file():
        return onnx.load(str(path), load_external_data=False)
    if graph is not None:
        raise ExportError(f"graph {graph} not found")
    return bbg.build_graph(checkpoint_dir, source="rebuilt by export_bert").model


def fold_weight_transposes(model: onnx.ModelProto) -> int:
    """Replace ``Transpose(initializer)`` by a pre-transposed initializer; return the count.

    The new initializer is named ``<weight>.T`` and every consumer of the Transpose output is
    rewired to it. An initializer left without consumers is removed.
    """
    graph = model.graph
    inits = {init.name: init for init in graph.initializer}
    rename: dict[str, str] = {}
    keep: list[onnx.NodeProto] = []
    for node in graph.node:
        src = inits.get(node.input[0]) if node.op_type == "Transpose" and node.input else None
        if src is None:
            keep.append(node)
            continue
        perm = next((list(a.ints) for a in node.attribute if a.name == "perm"), None)
        arr = np.ascontiguousarray(np.transpose(numpy_helper.to_array(src), perm))
        name = f"{src.name}.T"
        graph.initializer.append(numpy_helper.from_array(arr, name))
        rename[node.output[0]] = name
    for node in keep:
        for i, name in enumerate(node.input):
            if name in rename:
                node.input[i] = rename[name]
    del graph.node[:]
    graph.node.extend(keep)
    used = {name for node in graph.node for name in node.input}
    used |= {out.name for out in graph.output}
    for init in [i for i in graph.initializer if i.name not in used]:
        graph.initializer.remove(init)
    return len(rename)


def _weight_nodes(model: onnx.ModelProto, op_type: str, arg: int) -> list[str]:
    inits = {init.name for init in model.graph.initializer}
    return [
        n.name
        for n in model.graph.node
        if n.op_type == op_type and len(n.input) > arg and n.input[arg] in inits
    ]


def _quantize_blockwise(src: Path, dst: Path, tmp: Path) -> None:
    from onnxruntime.quantization.matmul_nbits_quantizer import (  # noqa: PLC0415
        MatMulNBitsQuantizer,
    )

    logging.getLogger("onnxruntime.quantization.matmul_nbits_quantizer").setLevel(logging.WARNING)
    quant = MatMulNBitsQuantizer(
        onnx.load(str(src)),
        bits=8,
        block_size=BLOCK_SIZE,
        is_symmetric=True,
        accuracy_level=ACCURACY_LEVEL,
        op_types_to_quantize=("MatMul",),
    )
    quant.process()
    mid = tmp / "nbits.onnx"
    quant.model.save_model_to_file(str(mid), False)
    model = onnx.load(str(mid))
    quantize_dynamic(
        mid,
        dst,
        op_types_to_quantize=["Gather"],
        nodes_to_quantize=_weight_nodes(model, "Gather", 0),
        per_channel=False,
        reduce_range=False,
        weight_type=QuantType.QInt8,
    )


def _quantize_dynamic(src: Path, dst: Path) -> None:
    model = onnx.load(str(src), load_external_data=False)
    nodes = _weight_nodes(model, "MatMul", 1) + _weight_nodes(model, "Gather", 0)
    quantize_dynamic(
        src,
        dst,
        op_types_to_quantize=["MatMul", "Gather"],
        nodes_to_quantize=nodes,
        per_channel=False,
        reduce_range=False,
        weight_type=QuantType.QInt8,
    )


def quantize(
    fp32: onnx.ModelProto, checkpoint_dir: Path, out: Path, scheme: str = DEFAULT_SCHEME
) -> onnx.ModelProto:
    """Quantize ``fp32`` (external data in ``checkpoint_dir``) to a self-contained INT8 model."""
    if scheme not in SCHEMES:
        raise ExportError(f"unknown scheme {scheme!r}; known: {', '.join(SCHEMES)}")
    weights = checkpoint_dir / bbg.WEIGHTS_FILE
    meta = bbg.metadata(fp32)
    want = meta.get("triage.weights.sha256")
    got = fetch.sha256_file(weights)
    if want != got:
        raise ExportError(f"graph expects {bbg.WEIGHTS_FILE} sha256 {want}, found {got}")
    full = onnx.ModelProto()
    full.CopyFrom(fp32)
    load_external_data_for_model(full, str(checkpoint_dir))
    fold_weight_transposes(full)
    out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=out.parent) as tmp_name:
        tmp = Path(tmp_name)
        src = tmp / "fp32.onnx"
        onnx.save(full, str(src))
        del full
        q_path = tmp / "q.onnx"
        if scheme == "blockwise":
            _quantize_blockwise(src, q_path, tmp)
        else:
            _quantize_dynamic(src, q_path)
        model = onnx.load(str(q_path))
    model.ir_version = min(model.ir_version, bbg.MAX_IR_VERSION)
    meta.update(
        {
            "triage.variant": "int8",
            "triage.int8.scheme": scheme,
            "triage.int8.converter": CONVERTER,
        }
    )
    bbg.set_metadata(model, meta)
    onnx.checker.check_model(model)
    part = out.with_name(out.name + f".{os.getpid()}.part")
    part.write_bytes(model.SerializeToString())
    part.replace(out)
    return model


def main(argv: list[str] | None = None) -> int:
    """CLI: ``python -m triage_research.export_bert [--checkpoint-dir D] [--out F]``."""
    parser = argparse.ArgumentParser(
        prog="triage-export-bert",
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("--checkpoint-dir", type=Path, default=None)
    parser.add_argument("--graph", type=Path, default=None)
    parser.add_argument("--out", type=Path, default=None)
    parser.add_argument("--scheme", choices=SCHEMES, default=DEFAULT_SCHEME)
    args = parser.parse_args(argv)
    ckpt = (args.checkpoint_dir or bbg.default_checkpoint_dir()).expanduser()
    out = (args.out or ckpt / DEFAULT_OUT_NAME).expanduser()
    try:
        quantize(load_graph(args.graph, ckpt), ckpt, out, args.scheme)
    except ExportError as err:
        sys.stderr.write(f"export_bert: {err}\n")
        return 1
    sys.stdout.write(f"{fetch.sha256_file(out)}  {out}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
