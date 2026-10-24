# SPDX-License-Identifier: Apache-2.0
"""Fetch allow-listed model files at a pinned revision and verify them by SHA-256."""

from __future__ import annotations

import argparse
import hashlib
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import httpx
import yaml

DEFAULT_CACHE_DIR = Path("~/.cache/rayld/triage/models")
DEFAULT_MANIFEST = Path(__file__).resolve().parents[3] / "models" / "manifest.yaml"
DEFAULT_BASE_URL = "https://huggingface.co"
FORBIDDEN_NAMES = frozenset({"optimizer.pt"})
_CHUNK = 1 << 20


class FetchError(Exception):
    """Base class for fetch failures."""


class NotAllowedError(FetchError):
    """The requested file is not allow-listed or is forbidden."""


class VerificationError(FetchError):
    """A downloaded file does not match the manifest size or SHA-256."""


@dataclass(frozen=True)
class FileSpec:
    path: str
    sha256: str
    size: int


@dataclass(frozen=True)
class Entry:
    name: str
    hf_repo: str
    revision: str
    files: tuple[FileSpec, ...]


def _check_name(path: str) -> None:
    if Path(path).name in FORBIDDEN_NAMES:
        raise NotAllowedError(f"refusing forbidden file: {path}")


def load_manifest(manifest_path: Path = DEFAULT_MANIFEST) -> dict[str, Entry]:
    """Parse the manifest and return its entries by name."""
    raw: Any = yaml.safe_load(manifest_path.read_text(encoding="utf-8"))
    entries: dict[str, Entry] = {}
    for item in raw["entries"]:
        files = tuple(
            FileSpec(path=f["path"], sha256=f["sha256"], size=int(f["size"])) for f in item["files"]
        )
        for spec in files:
            _check_name(spec.path)
        entries[item["name"]] = Entry(
            name=item["name"],
            hf_repo=item["hf_repo"],
            revision=item["revision"],
            files=files,
        )
    return entries


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as fh:
        while chunk := fh.read(_CHUNK):
            digest.update(chunk)
    return digest.hexdigest()


def _dest(entry: Entry, spec: FileSpec, cache_dir: Path) -> Path:
    return cache_dir.expanduser() / entry.name / entry.revision / spec.path


def fetch_file(
    entry: Entry,
    path: str,
    cache_dir: Path = DEFAULT_CACHE_DIR,
    base_url: str = DEFAULT_BASE_URL,
) -> Path:
    """Download one allow-listed file, verify it and return its cached path."""
    _check_name(path)
    spec = next((f for f in entry.files if f.path == path), None)
    if spec is None:
        raise NotAllowedError(f"{path} is not in the manifest for {entry.name}")
    dest = _dest(entry, spec, cache_dir)
    if dest.is_file() and dest.stat().st_size == spec.size and sha256_file(dest) == spec.sha256:
        return dest
    dest.parent.mkdir(parents=True, exist_ok=True)
    part = dest.with_name(dest.name + ".part")
    url = f"{base_url.rstrip('/')}/{entry.hf_repo}/resolve/{entry.revision}/{spec.path}"
    digest = hashlib.sha256()
    size = 0
    with httpx.stream("GET", url, follow_redirects=True, timeout=60.0) as resp:
        resp.raise_for_status()
        with part.open("wb") as out:
            for chunk in resp.iter_bytes(_CHUNK):
                digest.update(chunk)
                size += len(chunk)
                out.write(chunk)
    if size != spec.size or digest.hexdigest() != spec.sha256:
        part.unlink(missing_ok=True)
        raise VerificationError(
            f"{entry.name}/{path}: expected {spec.size} bytes sha256 {spec.sha256}, "
            f"got {size} bytes sha256 {digest.hexdigest()}"
        )
    part.replace(dest)
    return dest


def fetch_entry(
    entry: Entry,
    cache_dir: Path = DEFAULT_CACHE_DIR,
    base_url: str = DEFAULT_BASE_URL,
) -> list[Path]:
    return [fetch_file(entry, f.path, cache_dir, base_url) for f in entry.files]


def main(argv: list[str] | None = None) -> int:
    """CLI: ``python -m triage_research.fetch bert|mf|ner|all``."""
    parser = argparse.ArgumentParser(prog="triage-fetch", description=__doc__)
    parser.add_argument("name", help="manifest entry name, or 'all'")
    parser.add_argument("--cache-dir", type=Path, default=DEFAULT_CACHE_DIR)
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL)
    args = parser.parse_args(argv)
    entries = load_manifest(args.manifest)
    if args.name != "all" and args.name not in entries:
        parser.error(f"unknown entry {args.name!r}; known: {', '.join(entries)}")
    names = list(entries) if args.name == "all" else [args.name]
    for name in names:
        for path in fetch_entry(entries[name], args.cache_dir, args.base_url):
            sys.stdout.write(f"{path}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
