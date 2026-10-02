"""Exercise the guest installer against actual home files, without Ansible."""

import os
from pathlib import Path
import pwd
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "roles/user/files/safe_home_file.py"
USER = pwd.getpwuid(os.getuid()).pw_name


class SafeHomeFileTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.home = Path(self.tmp.name) / "home"
        self.home.mkdir()

    def install(self, rel, content, preserved=False, fresh=False, record=False, extra=None):
        command = [sys.executable, str(SCRIPT), str(self.home), rel, USER, "0644"]
        if preserved:
            command.append("--preserved-home")
        if fresh:
            command.append("--fresh-clone")
        if record:
            command.append("--record-current")
        if extra:
            command.append(extra)
        return subprocess.run(command, input=content.encode(), capture_output=True, check=True)

    def path(self, rel):
        return self.home / rel

    def test_fresh_unchanged_edited_and_rerun(self):
        rel = ".tmux.conf"
        base = self.path(".config/sandbar/home-baselines/.tmux.conf")
        proposal = self.path(".config/sandbar/proposed-defaults/.tmux.conf")
        self.install(rel, "first\n", fresh=True)
        self.assertEqual(self.path(rel).read_text(), "first\n")
        self.assertEqual(base.read_text(), "first\n")
        self.install(rel, "second\n", preserved=True)
        self.assertEqual(self.path(rel).read_text(), "second\n")
        self.assertEqual(base.read_text(), "second\n")
        self.path(rel).write_text("my edit\n")
        self.install(rel, "third\n", preserved=True)
        self.install(rel, "third\n", preserved=True)
        self.assertEqual(self.path(rel).read_text(), "my edit\n")
        self.assertEqual(base.read_text(), "second\n")
        self.assertEqual(proposal.read_text(), "third\n")

    def test_legacy_unknown_and_deleted_file_keep_their_history(self):
        rel = ".gitconfig"
        self.path(rel).write_text("legacy custom\n")
        self.install(rel, "new default\n", preserved=True)
        self.assertEqual(self.path(rel).read_text(), "legacy custom\n")
        self.assertFalse(self.path(".config/sandbar/home-baselines/.gitconfig").exists())
        self.assertEqual(self.path(".config/sandbar/proposed-defaults/.gitconfig").read_text(), "new default\n")
        self.path(rel).unlink()
        self.install(rel, "another default\n", preserved=True)
        self.assertFalse(self.path(rel).exists())
        self.assertEqual(self.path(".config/sandbar/proposed-defaults/.gitconfig").read_text(), "new default\n")
        self.assertTrue(list(self.path(".config/sandbar/proposed-defaults").glob(".gitconfig.*")))

    def test_old_base_default_on_a_fresh_clone_is_replaced(self):
        self.path(".tmux.conf").write_text("old base default\n")
        self.install(".tmux.conf", "new release default\n", fresh=True)
        self.assertEqual(self.path(".tmux.conf").read_text(), "new release default\n")
        self.assertEqual(self.path(".config/sandbar/home-baselines/.tmux.conf").read_text(), "new release default\n")

    def test_absent_default_on_first_full_run_is_installed(self):
        self.install(".gitconfig", "new identity\n")
        self.assertEqual(self.path(".gitconfig").read_text(), "new identity\n")
        self.assertEqual(self.path(".config/sandbar/home-baselines/.gitconfig").read_text(), "new identity\n")

    def test_final_sand_block_is_recorded_only_after_it_is_installed(self):
        self.install(".gitconfig", "identity\n", fresh=True)
        self.path(".gitconfig").write_text("identity\nmanaged include\n")
        self.install(".gitconfig", "", record=True)
        self.assertEqual(self.path(".config/sandbar/home-baselines/.gitconfig").read_text(), "identity\nmanaged include\n")
        self.install(".gitconfig", "next identity\n", preserved=True)
        self.assertEqual(self.path(".gitconfig").read_text(), "next identity\n")

    def test_tracked_gitlab_include_survives_identity_refresh(self):
        managed = "# BEGIN sandbar git-credentials\n[includeIf \"gitdir:~/gitlab.com/team/\"]\n    path = ~/.config/sandbar/gitconfig.d/team\n# END sandbar git-credentials\n"
        self.install(".gitconfig", "old identity\n", fresh=True)
        self.path(".gitconfig").write_text("old identity\n\n" + managed)
        self.install(".gitconfig", "", record=True)
        self.install(".gitconfig", "new identity\n", preserved=True, extra="--preserve-git-credentials-block")
        expected = "new identity\n\n" + managed
        self.assertEqual(self.path(".gitconfig").read_text(), expected)
        self.assertEqual(self.path(".config/sandbar/home-baselines/.gitconfig").read_text(), expected)
        self.path(".gitconfig").write_text(expected + "# user edit\n")
        self.install(".gitconfig", "later identity\n", preserved=True, extra="--preserve-git-credentials-block")
        self.assertEqual(self.path(".gitconfig").read_text(), expected + "# user edit\n")
        self.assertEqual(self.path(".config/sandbar/proposed-defaults/.gitconfig").read_text(), "later identity\n")

    def test_symlink_and_symlink_parent_are_never_followed(self):
        outside = Path(self.tmp.name) / "outside"
        outside.write_text("outside\n")
        self.path(".gitconfig").symlink_to(outside)
        self.install(".gitconfig", "candidate\n", preserved=True)
        self.assertTrue(self.path(".gitconfig").is_symlink())
        self.assertEqual(outside.read_text(), "outside\n")
        self.assertEqual(self.path(".config/sandbar/proposed-defaults/.gitconfig").read_text(), "candidate\n")
        self.path(".codex").symlink_to(Path(self.tmp.name))
        with self.assertRaises(subprocess.CalledProcessError):
            self.install(".codex/config.toml", "candidate\n", preserved=True)
        self.assertFalse((Path(self.tmp.name) / "config.toml").exists())

    def test_in_place_block_requires_unchanged_baseline(self):
        self.install(".bashrc", "base\n", fresh=True)
        self.install(".bashrc", "", extra="--check-managed")
        self.path(".bashrc").write_text("user edit\n")
        with self.assertRaises(subprocess.CalledProcessError) as failure:
            self.install(".bashrc", "", extra="--check-managed")
        self.assertEqual(failure.exception.returncode, 3)
        self.install(".bashrc", "new block\n", extra="--propose-only")
        self.assertEqual(self.path(".bashrc").read_text(), "user edit\n")
        self.assertEqual(self.path(".config/sandbar/proposed-defaults/.bashrc").read_text(), "new block\n")

    def test_direct_rerun_does_not_adopt_edited_or_unknown_files(self):
        self.install(".gitconfig", "base\n", fresh=True)
        self.path(".gitconfig").write_text("user edit\n")
        self.install(".gitconfig", "new\n")
        self.assertEqual(self.path(".gitconfig").read_text(), "user edit\n")
        self.assertEqual(self.path(".config/sandbar/home-baselines/.gitconfig").read_text(), "base\n")
        self.path(".config/sandbar/home-baselines/.gitconfig").unlink()
        self.install(".gitconfig", "later\n")
        self.assertEqual(self.path(".gitconfig").read_text(), "user edit\n")


if __name__ == "__main__":
    unittest.main()
