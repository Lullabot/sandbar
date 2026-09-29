"""Contract tests for the release manifest consumed by the Go image pin."""

import hashlib
import json
import tempfile
import unittest
from pathlib import Path

from scripts import write_base_image_manifest


class BaseImageManifestTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.assets = Path(self.temporary.name)
        for arch in ("amd64", "arm64"):
            name = f"sandbar-base-debian-13-{arch}.qcow2"
            payload = f"image-{arch}".encode()
            (self.assets / name).write_bytes(payload)
            digest = hashlib.sha256(payload).hexdigest()
            (self.assets / f"{name}.sha256").write_text(f"{digest}  {name}\n")

    def write_manifest(self):
        output = self.assets / "manifest.json"
        write_base_image_manifest.write_manifest(
            "base-image-2026.09.29", "Lullabot/sandbar", self.assets, output
        )
        return json.loads(output.read_text())

    def test_manifest_has_verified_urls_digests_and_byte_sizes(self):
        manifest = self.write_manifest()
        self.assertEqual(manifest["version"], "base-image-2026.09.29")
        self.assertEqual(set(manifest["images"]), {"amd64", "arm64"})
        for arch in ("amd64", "arm64"):
            name = f"sandbar-base-debian-13-{arch}.qcow2"
            image = manifest["images"][arch]
            self.assertEqual(
                image["url"],
                f"https://github.com/Lullabot/sandbar/releases/download/"
                f"base-image-2026.09.29/{name}",
            )
            self.assertEqual(
                image["sha256"], hashlib.sha256((self.assets / name).read_bytes()).hexdigest()
            )
            self.assertEqual(image["size"], (self.assets / name).stat().st_size)

    def test_rejects_checksum_mismatch_before_writing_manifest(self):
        path = self.assets / "sandbar-base-debian-13-arm64.qcow2.sha256"
        path.write_text(f"{'0' * 64}  sandbar-base-debian-13-arm64.qcow2\n")
        with self.assertRaisesRegex(ValueError, "arm64.*checksum mismatch"):
            self.write_manifest()
        self.assertFalse((self.assets / "manifest.json").exists())

    def test_rejects_wrong_checksum_filename(self):
        path = self.assets / "sandbar-base-debian-13-arm64.qcow2.sha256"
        path.write_text(f"{'0' * 64}  wrong.qcow2\n")
        with self.assertRaisesRegex(ValueError, "arm64.*checksum filename"):
            self.write_manifest()

    def test_rejects_extra_asset(self):
        (self.assets / "unexpected.txt").write_text("unexpected")
        with self.assertRaisesRegex(ValueError, "unexpected assets"):
            self.write_manifest()

    def test_rejects_oversize_image_before_hashing(self):
        with (self.assets / "sandbar-base-debian-13-arm64.qcow2").open("r+b") as file:
            file.truncate(2 * 1024**3)
        with self.assertRaisesRegex(ValueError, "arm64.*2 GiB"):
            self.write_manifest()

    def test_rejects_tag_outside_release_namespace(self):
        with self.assertRaisesRegex(ValueError, "tag"):
            write_base_image_manifest.write_manifest(
                "v1.0.0", "Lullabot/sandbar", self.assets, self.assets / "manifest.json"
            )


if __name__ == "__main__":
    unittest.main()
