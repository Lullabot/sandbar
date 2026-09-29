#!/usr/bin/env python3
"""Verify both built images and write the stable public release manifest."""

import argparse
import datetime
import hashlib
import json
import re
from pathlib import Path


ARCHES = ("amd64", "arm64")
MAX_IMAGE_BYTES = 2 * 1024**3


def write_manifest(tag: str, repo: str, assets: Path, output: Path) -> None:
    match = re.fullmatch(
        r"base-image-(\d{4})\.(\d{2})\.(\d{2})(?:\.(\d{2})(\d{2})(\d{2}))?",
        tag,
    )
    if not match:
        raise ValueError(f"invalid base image tag: {tag}")
    try:
        datetime.date(*(int(part) for part in match.groups()[:3]))
        if match.group(4) is not None:
            datetime.time(*(int(part) for part in match.groups()[3:]))
    except ValueError as error:
        raise ValueError(f"invalid base image tag: {tag}") from error
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repo):
        raise ValueError(f"invalid GitHub repository: {repo}")

    expected = {
        suffix
        for arch in ARCHES
        for suffix in (
            f"sandbar-base-debian-13-{arch}.qcow2",
            f"sandbar-base-debian-13-{arch}.qcow2.sha256",
        )
    }
    actual = {path.name for path in assets.iterdir()}
    if actual != expected:
        raise ValueError(f"missing assets: {sorted(expected - actual)}; unexpected assets: {sorted(actual - expected)}")

    images = {}
    for arch in ARCHES:
        filename = f"sandbar-base-debian-13-{arch}.qcow2"
        image = assets / filename
        size = image.stat().st_size
        if size <= 0 or size >= MAX_IMAGE_BYTES:
            raise ValueError(f"{arch} image must be nonempty and under 2 GiB; got {size} bytes")

        checksum_lines = (assets / f"{filename}.sha256").read_text().splitlines()
        checksum = re.fullmatch(r"([0-9a-f]{64})  (.+)", checksum_lines[0]) if len(checksum_lines) == 1 else None
        if checksum is None:
            raise ValueError(f"{arch} checksum file must have one sha256sum line")
        if checksum.group(2) != filename:
            raise ValueError(f"{arch} checksum filename must be {filename}")

        digest = hashlib.sha256()
        with image.open("rb") as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(block)
        if digest.hexdigest() != checksum.group(1):
            raise ValueError(f"{arch} checksum mismatch: expected {checksum.group(1)}, got {digest.hexdigest()}")

        images[arch] = {
            "url": f"https://github.com/{repo}/releases/download/{tag}/{filename}",
            "sha256": digest.hexdigest(),
            "size": size,
        }

    output.write_text(json.dumps({"version": tag, "images": images}, indent=2) + "\n")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--assets", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    write_manifest(args.tag, args.repo, args.assets, args.output)


if __name__ == "__main__":
    main()
