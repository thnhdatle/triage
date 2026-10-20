# SPDX-License-Identifier: Apache-2.0
"""Unit tests for triage_research.fetch using a local HTTP server (no network)."""

from __future__ import annotations

import hashlib
import http.server
import threading
from collections.abc import Iterator
from pathlib import Path

import pytest
import yaml

from triage_research import fetch

REPO = "org/model"
REV = "a" * 40
GOOD = b"hello weights"
OTHER = b"tampered data"


class _Handler(http.server.BaseHTTPRequestHandler):
    hits: list[str] = []  # noqa: RUF012
    files: dict[str, bytes] = {}  # noqa: RUF012

    def do_GET(self) -> None:
        type(self).hits.append(self.path)
        body = type(self).files.get(self.path)
        if body is None:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: object) -> None:  # noqa: A002, ARG002
        return


@pytest.fixture
def server() -> Iterator[str]:
    _Handler.hits = []
    _Handler.files = {
        f"/{REPO}/resolve/{REV}/model.safetensors": GOOD,
        f"/{REPO}/resolve/{REV}/config.json": OTHER,
        f"/{REPO}/resolve/{REV}/optimizer.pt": b"x",
        f"/{REPO}/resolve/{REV}/extra.bin": b"x",
    }
    httpd = http.server.ThreadingHTTPServer(("127.0.0.1", 0), _Handler)
    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    yield f"http://127.0.0.1:{httpd.server_port}"
    httpd.shutdown()
    httpd.server_close()


def _manifest(tmp_path: Path, extra_files: list[dict[str, object]] | None = None) -> Path:
    files: list[dict[str, object]] = [
        {
            "path": "model.safetensors",
            "sha256": hashlib.sha256(GOOD).hexdigest(),
            "size": len(GOOD),
        },
        {
            "path": "config.json",
            "sha256": hashlib.sha256(b"expected").hexdigest(),  # server serves other bytes
            "size": len(b"expected"),
        },
    ]
    files += extra_files or []
    path = tmp_path / "manifest.yaml"
    path.write_text(
        yaml.safe_dump(
            {"entries": [{"name": "m", "hf_repo": REPO, "revision": REV, "files": files}]}
        ),
        encoding="utf-8",
    )
    return path


def test_fetch_verifies_and_caches(server: str, tmp_path: Path) -> None:
    entry = fetch.load_manifest(_manifest(tmp_path))["m"]
    cache = tmp_path / "cache"
    got = fetch.fetch_file(entry, "model.safetensors", cache, server)
    assert got.read_bytes() == GOOD
    assert got == cache / "m" / REV / "model.safetensors"
    n = len(_Handler.hits)
    again = fetch.fetch_file(entry, "model.safetensors", cache, server)
    assert again == got
    assert len(_Handler.hits) == n  # served from cache


def test_hash_mismatch_is_rejected_and_not_kept(server: str, tmp_path: Path) -> None:
    entry = fetch.load_manifest(_manifest(tmp_path))["m"]
    cache = tmp_path / "cache"
    with pytest.raises(fetch.VerificationError):
        fetch.fetch_file(entry, "config.json", cache, server)
    left = [p for p in cache.rglob("*") if p.is_file()]
    assert left == []


def test_corrupt_cache_is_redownloaded(server: str, tmp_path: Path) -> None:
    entry = fetch.load_manifest(_manifest(tmp_path))["m"]
    cache = tmp_path / "cache"
    dest = fetch.fetch_file(entry, "model.safetensors", cache, server)
    dest.write_bytes(b"corrupt")
    assert fetch.fetch_file(entry, "model.safetensors", cache, server).read_bytes() == GOOD


def test_file_not_in_manifest_is_refused(server: str, tmp_path: Path) -> None:
    entry = fetch.load_manifest(_manifest(tmp_path))["m"]
    with pytest.raises(fetch.NotAllowedError):
        fetch.fetch_file(entry, "extra.bin", tmp_path / "cache", server)
    assert _Handler.hits == []


def test_optimizer_is_refused_even_if_requested(server: str, tmp_path: Path) -> None:
    entry = fetch.load_manifest(_manifest(tmp_path))["m"]
    with pytest.raises(fetch.NotAllowedError):
        fetch.fetch_file(entry, "optimizer.pt", tmp_path / "cache", server)
    assert _Handler.hits == []


def test_manifest_listing_optimizer_is_rejected(tmp_path: Path) -> None:
    bad = _manifest(tmp_path, [{"path": "optimizer.pt", "sha256": "0" * 64, "size": 1}])
    with pytest.raises(fetch.NotAllowedError):
        fetch.load_manifest(bad)


def test_real_manifest_is_well_formed() -> None:
    entries = fetch.load_manifest()
    assert set(entries) == {"bert", "mf", "ner"}
    for entry in entries.values():
        assert len(entry.revision) == 40
        assert all(len(f.sha256) == 64 for f in entry.files)
    assert "optimizer.pt" not in {f.path for f in entries["bert"].files}


def test_cli_fetches_entry(server: str, tmp_path: Path) -> None:
    manifest = _manifest(tmp_path, [])
    # drop the deliberately corrupt file by rewriting the manifest with only the good one
    data = yaml.safe_load(manifest.read_text(encoding="utf-8"))
    data["entries"][0]["files"] = data["entries"][0]["files"][:1]
    manifest.write_text(yaml.safe_dump(data), encoding="utf-8")
    cache = tmp_path / "c"
    rc = fetch.main(
        ["m", "--manifest", str(manifest), "--cache-dir", str(cache), "--base-url", server]
    )
    assert rc == 0
    assert (cache / "m" / REV / "model.safetensors").is_file()
