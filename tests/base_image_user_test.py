"""Offline account contract for images before either provider first boots them."""

import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
CHECKER = ROOT / "scripts/check-base-image-user-state.sh"


class BaseImageUserStateTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.image_root = Path(self.temporary.name)
        (self.image_root / "home").mkdir()
        etc = self.image_root / "etc"
        etc.mkdir()
        (etc / "passwd").write_text("root:x:0:0:root:/root:/bin/bash\n")
        (etc / "group").write_text("root:x:0:\n")
        skel = etc / "skel"
        for relative in (
            ".local/bin/uv",
            ".local/bin/uvx",
            ".tmux.conf",
            ".ssh/rc",
            ".config/direnv/direnv.toml",
        ):
            target = skel / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.touch()

    def check(self):
        return subprocess.run(
            ["bash", str(CHECKER), str(self.image_root)],
            capture_output=True,
            text=True,
            check=False,
        )

    def test_skeleton_without_baked_login_is_accepted(self):
        result = self.check()
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_baked_account_colliding_with_cloud_init_is_rejected(self):
        (self.image_root / "etc/passwd").write_text(
            "root:x:0:0:root:/root:/bin/bash\n"
            "claude:x:1000:1000::/home/claude:/bin/bash\n"
        )
        (self.image_root / "etc/group").write_text("root:x:0:\nclaude:x:1000:\n")
        result = self.check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("UID 1000", result.stderr)

    def test_relocated_baked_login_is_still_rejected(self):
        (self.image_root / "etc/passwd").write_text(
            "root:x:0:0:root:/root:/bin/bash\n"
            "claude:x:2000:2000::/home/claude:/bin/bash\n"
        )
        (self.image_root / "etc/group").write_text("root:x:0:\nclaude:x:2000:\n")
        result = self.check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("baked login claude", result.stderr)

    def test_leftover_primary_group_is_rejected(self):
        (self.image_root / "etc/group").write_text("root:x:0:\nclaude:x:1000:\n")
        result = self.check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("GID 1000", result.stderr)

    def test_relocated_primary_group_is_still_rejected(self):
        (self.image_root / "etc/group").write_text("root:x:0:\nclaude:x:2000:\n")
        result = self.check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("baked group claude", result.stderr)

    def test_missing_reusable_home_setup_is_rejected(self):
        (self.image_root / "etc/skel/.local/bin/uv").unlink()
        result = self.check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(".local/bin/uv", result.stderr)

    def test_transient_uv_install_prefix_is_rejected(self):
        receipt = self.image_root / "etc/skel/.config/uv/uv-receipt.json"
        receipt.parent.mkdir(parents=True)
        receipt.write_text('{"install_prefix":"/home/claude/.local/bin"}')
        result = self.check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("uv install receipt", result.stderr)


if __name__ == "__main__":
    unittest.main()
