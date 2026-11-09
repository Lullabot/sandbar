"""Run with PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p '*_test.py'.

Sandbar's tmux configuration is installed as /etc/tmux.conf, and the
~/.tmux.conf earlier releases left in a preserved home is retired. These tests
run the production tasks through Ansible, with temporary directories standing
in for /etc and the guest home, and then load the result in a real tmux
server when one is installed: which settings a session ends up with is what a
user notices, not which files exist.
"""
import copy
import getpass
import grp
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[1]
FIXTURES = ROOT / "tests/fixtures/legacy-home-launchers"
TEMPLATE = ROOT / "roles/user/templates/tmux.conf.j2"
HELPER = str(ROOT / "roles/user/files/safe_home_file.py")
SITE_VARS = yaml.safe_load((ROOT / "site.yml").read_text())[0]["vars"]
RETIRED = yaml.safe_load((ROOT / "roles/user/vars/main.yml").read_text())
DEFAULTS = yaml.safe_load((ROOT / "roles/user/defaults/main.yml").read_text())

TASKS = {
    "Deploy sandbar's tmux configuration",
    "Retire home copies of files sandbar now installs outside the home",
}
OLDEST, NEWEST = "4e27cb9", "b51f1d8"


@unittest.skipUnless(shutil.which("ansible-playbook"), "ansible-playbook not installed")
class SystemTmuxConfTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sand-tmux-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.home = self.root / "home"
        self.home.mkdir()
        (self.root / "etc").mkdir()
        self.system_conf = self.root / "etc/tmux.conf"
        # The template module looks for src beside the playbook.
        (self.root / "templates").symlink_to(ROOT / "roles/user/templates", target_is_directory=True)
        group = grp.getgrgid(os.getgid()).gr_name

        def rewrite(value):
            if isinstance(value, str):
                return value.replace("/usr/local/libexec/sandbar-safe-home-file", HELPER)
            if isinstance(value, list):
                return [rewrite(part) for part in value]
            if isinstance(value, dict):
                value = {key: rewrite(part) for key, part in value.items()}
                # Production installs this as root; this test is unprivileged.
                if value.get("owner") == "root":
                    value.update(owner=getpass.getuser(), group=group)
                return value
            return value

        tasks = [copy.deepcopy(task) for task in yaml.safe_load((ROOT / "roles/user/tasks/main.yml").read_text())
                 if task.get("name") in TASKS]
        self.assertEqual({task["name"] for task in tasks}, TASKS)
        self.tasks = rewrite(tasks)
        self.variables = {
            **DEFAULTS, **SITE_VARS, **RETIRED,
            "sand_system_tmux_conf": str(self.system_conf),
            "user_home": str(self.home),
            "user_name": getpass.getuser(),
            "provision_phase": "finalize",
            "sand_bashrc_writable": True,
        }

    def provision(self, **overrides):
        playbook = self.root / "play.yml"
        playbook.write_text(yaml.safe_dump([{"hosts": "all", "gather_facts": False, "tasks": self.tasks}]))
        result = subprocess.run(
            ["ansible-playbook", "-i", "localhost,", "-c", "local", str(playbook),
             "-e", json.dumps({**self.variables, **overrides})],
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            env={**os.environ, "ANSIBLE_NOCOLOR": "1"})
        self.assertEqual(result.returncode, 0, result.stdout)
        return result.stdout

    def legacy_home(self, version, baseline=False):
        """A home restored from a VM an earlier release provisioned."""
        released = (FIXTURES / (".tmux.conf." + version)).read_bytes()
        (self.home / ".tmux.conf").write_bytes(released)
        if baseline:
            # Since the safe home writer, Ansible's file lookup stripped the
            # final newline and the writer recorded those bytes as the baseline.
            for relative in (".config/sandbar/home-baselines/.tmux.conf",
                             ".config/sandbar/proposed-defaults/.tmux.conf"):
                path = self.home / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(released.rstrip(b"\n"))
            (self.home / ".tmux.conf").write_bytes(released.rstrip(b"\n"))

    def assert_no_home_copy(self):
        for relative in (".tmux.conf", ".config/sandbar/home-baselines/.tmux.conf",
                         ".config/sandbar/proposed-defaults/.tmux.conf"):
            self.assertFalse(os.path.lexists(self.home / relative), relative)

    def tmux_options(self, *configs):
        """Start a private tmux server on these configs and return its options."""
        socket = self.root / "tmux.sock"
        argv = ["tmux", "-S", str(socket)]
        for config in configs:
            argv += ["-f", str(config)]
        env = {"HOME": str(self.home), "PATH": os.environ["PATH"], "TERM": "xterm-256color"}
        self.addCleanup(subprocess.run, ["tmux", "-S", str(socket), "kill-server"], env=env, capture_output=True)
        result = subprocess.run(
            argv + ["start-server", ";", "show-options", "-g", ";", "list-keys", "-T", "prefix", "r", ";", "kill-server"],
            env=env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return result.stdout

    def test_fresh_guest_gets_the_system_file_and_nothing_in_the_home(self):
        out = self.provision(sand_fresh_clone=True)
        self.assertEqual(self.system_conf.read_text().rstrip("\n"), TEMPLATE.read_text().rstrip("\n"))
        self.assertEqual(self.system_conf.stat().st_mode & 0o777, 0o644)
        self.assertNotIn("FAILED", out)
        self.assert_no_home_copy()
        self.assertRegex(self.provision(), r"changed=0\s")

    def test_released_home_copies_are_removed(self):
        for version, baseline in ((OLDEST, False), (NEWEST, False), (NEWEST, True)):
            with self.subTest(version=version, baseline=baseline):
                self.legacy_home(version, baseline)
                self.provision(sand_preserved_home=True)
                self.assert_no_home_copy()
                self.assertTrue(self.system_conf.is_file())
                self.assertRegex(self.provision(sand_preserved_home=True), r"changed=0\s")

    def test_edited_home_copy_is_kept(self):
        self.legacy_home(NEWEST, baseline=True)
        edited = self.home / ".tmux.conf"
        edited.write_text(edited.read_text() + "\nset -g history-limit 1234\n")
        before = edited.read_bytes()
        self.provision(sand_preserved_home=True)
        self.assertEqual(edited.read_bytes(), before)
        # Its baseline described a file sandbar no longer writes there.
        self.assertFalse((self.home / ".config/sandbar/home-baselines/.tmux.conf").exists())
        self.assertRegex(self.provision(sand_preserved_home=True), r"changed=0\s")

    @unittest.skipUnless(shutil.which("tmux"), "tmux not installed")
    def test_tmux_loads_the_system_file_and_a_user_file_still_wins(self):
        self.provision(sand_fresh_clone=True)
        # tmux reads /etc/tmux.conf and then ~/.tmux.conf; -f twice reproduces
        # that order without writing to the real /etc.
        options = self.tmux_options(self.system_conf)
        self.assertIn("prefix C-a", options)
        self.assertIn("history-limit 50000", options)
        self.assertIn("mouse on", options)
        # The reload binding names both files, system first; tmux expands ~
        # for the user running the server.
        self.assertIn("source-file -q /etc/tmux.conf " + str(self.home / ".tmux.conf") + " ", options)

        user = self.home / ".tmux.conf"
        user.write_text("set -g history-limit 1234\n")
        options = self.tmux_options(self.system_conf, user)
        self.assertIn("history-limit 1234", options)
        self.assertIn("prefix C-a", options)


if __name__ == "__main__":
    unittest.main()
