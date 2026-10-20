# SPDX-License-Identifier: Apache-2.0
"""Tests for build_bert_graph and export_bert on a small random model (no downloads)."""

from __future__ import annotations

import itertools
import json
from pathlib import Path
from typing import Any

import numpy as np
import onnx
import onnxruntime as ort
import pytest
import yaml
from safetensors.numpy import save_file

from triage_research import build_bert_graph as bbg
from triage_research import export_bert, fetch, make_fixtures

SMALL = {**make_fixtures.TINY_CONFIG, "vocab_size": 64}


def _checkpoint(root: Path, config: dict[str, Any] = SMALL, seed: int = 3) -> Path:
    root.mkdir(parents=True, exist_ok=True)
    (root / bbg.CONFIG_FILE).write_text(json.dumps(config))
    model = bbg.skeleton(bbg.load_config(root / bbg.CONFIG_FILE), seed=seed, std=1.0)
    save_file(
        {n: p.detach().numpy().copy() for n, p in model.named_parameters()},
        str(root / bbg.WEIGHTS_FILE),
    )
    return root


def test_build_maps_every_parameter_and_runs(tmp_path: Path) -> None:
    ckpt = _checkpoint(tmp_path / "ckpt")
    result = bbg.build_graph(ckpt, "test")
    proto = result.model
    assert proto.ir_version <= bbg.MAX_IR_VERSION
    assert [o.version for o in proto.opset_import if o.domain in ("", "ai.onnx")] == [bbg.OPSET]
    assert result.unused == ()
    header = bbg.read_safetensors_header(ckpt / bbg.WEIGHTS_FILE)
    assert set(result.mapped) == set(header)
    for init in proto.graph.initializer:
        if init.name in header:
            assert init.data_location == onnx.TensorProto.EXTERNAL
            assert not init.raw_data
            ext = {e.key: e.value for e in init.external_data}
            assert ext["location"] == bbg.WEIGHTS_FILE
            assert int(ext["offset"]) == header[init.name].offset
    meta = bbg.metadata(proto)
    assert meta["triage.weights.sha256"] == fetch.sha256_file(ckpt / bbg.WEIGHTS_FILE)
    assert bbg.verify(proto, ckpt) <= 1e-5
    assert not list(ckpt.glob(".*.onnx")), "temporary graph left behind"


def test_build_is_deterministic(tmp_path: Path) -> None:
    ckpt = _checkpoint(tmp_path / "ckpt")
    one = bbg.build_graph(ckpt, "test").model.SerializeToString()
    two = bbg.build_graph(ckpt, "test").model.SerializeToString()
    assert one == two


def test_mapping_errors(tmp_path: Path) -> None:
    ckpt = _checkpoint(tmp_path / "ckpt")
    header = bbg.read_safetensors_header(ckpt / bbg.WEIGHTS_FILE)
    model = bbg.skeleton(bbg.load_config(ckpt / bbg.CONFIG_FILE))
    names = [n for n, _ in model.named_parameters()]
    name = "classifier.out_proj.weight"
    wrong_shape = {**header, name: bbg.TensorInfo("F32", (8, 3), 0, 96)}
    with pytest.raises(bbg.MappingError, match="shape"):
        bbg.externalize(bbg.export(model), wrong_shape, names)
    wrong_dtype = {**header, name: bbg.TensorInfo("F16", (3, 8), 0, 48)}
    with pytest.raises(bbg.MappingError, match="dtype"):
        bbg.externalize(bbg.export(model), wrong_dtype, names)
    missing = {k: v for k, v in header.items() if k != name}
    with pytest.raises(bbg.MappingError, match="no checkpoint tensor"):
        bbg.externalize(bbg.export(model), missing, names)


def test_truncated_header(tmp_path: Path) -> None:
    bad = tmp_path / "x.safetensors"
    bad.write_bytes(b"\x01")
    with pytest.raises(bbg.MappingError, match="truncated"):
        bbg.read_safetensors_header(bad)


@pytest.mark.parametrize("scheme", export_bert.SCHEMES)
def test_export_int8(tmp_path: Path, scheme: str) -> None:
    ckpt = _checkpoint(tmp_path / "ckpt")
    graph = bbg.build_graph(ckpt, "test").model
    out = tmp_path / "out" / "model.int8.onnx"
    model = export_bert.quantize(graph, ckpt, out, scheme)
    assert out.is_file()
    assert model.ir_version <= bbg.MAX_IR_VERSION
    meta = bbg.metadata(model)
    assert meta["triage.variant"] == "int8"
    assert meta["triage.int8.scheme"] == scheme
    ops = {n.op_type for n in model.graph.node}
    assert ("MatMulNBits" in ops) == (scheme == "blockwise")
    inits = {i.name for i in model.graph.initializer}
    assert not [n for n in model.graph.node if n.op_type == "Transpose" and n.input[0] in inits]
    sess = ort.InferenceSession(str(out), providers=["CPUExecutionProvider"])
    ids = np.asarray([[0, 5, 9, 2]], dtype=np.int64)
    (logits,) = sess.run(["logits"], {"input_ids": ids, "attention_mask": np.ones_like(ids)})
    assert logits.shape == (1, 3)


def test_export_refuses_other_checkpoint(tmp_path: Path) -> None:
    ckpt = _checkpoint(tmp_path / "a")
    other = _checkpoint(tmp_path / "b", seed=4)
    graph = bbg.build_graph(ckpt, "test").model
    with pytest.raises(export_bert.ExportError, match="sha256"):
        export_bert.quantize(graph, other, tmp_path / "x.onnx")
    with pytest.raises(export_bert.ExportError, match="scheme"):
        export_bert.quantize(graph, ckpt, tmp_path / "x.onnx", "fp16")


def test_committed_graph_matches_manifest() -> None:
    proto = onnx.load(str(bbg.DEFAULT_OUT), load_external_data=False)
    raw = yaml.safe_load(fetch.DEFAULT_MANIFEST.read_text(encoding="utf-8"))
    bert = next(e for e in raw["entries"] if e["name"] == "bert")
    weights = next(f for f in bert["files"] if f["path"] == bbg.WEIGHTS_FILE)
    meta = bbg.metadata(proto)
    assert meta["triage.weights.sha256"] == weights["sha256"]
    assert int(meta["triage.weights.size"]) == weights["size"]
    assert meta["triage.source"] == f"{bert['hf_repo']}@{bert['revision']}"
    assert proto.ir_version <= bbg.MAX_IR_VERSION
    spans = []
    for init in proto.graph.initializer:
        assert init.data_location == onnx.TensorProto.EXTERNAL, init.name
        ext = {e.key: e.value for e in init.external_data}
        assert ext["location"] == bbg.WEIGHTS_FILE
        spans.append((int(ext["offset"]), int(ext["length"])))
    spans.sort()
    assert all(a[0] + a[1] <= b[0] for a, b in itertools.pairwise(spans))
    assert spans[-1][0] + spans[-1][1] <= weights["size"]
    assert [i.name for i in proto.graph.input] == [bbg.INPUT_IDS, bbg.ATTENTION_MASK]
    assert [o.name for o in proto.graph.output] == [bbg.LOGITS]
