"""Run with PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p '*_test.py'.

The Claude and Codex launchers and Codex's boot unit are installed outside the
home, and the copies earlier releases left in a preserved home are retired.
These tests run the production tasks through Ansible, with temporary
directories standing in for /etc, /usr/local/share and the guest home, and
then check the result in a real bash: which `claude` an interactive shell ends
up with is the thing a user notices, not which files exist.
"""
import copy
import getpass
import grp
import hashlib
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
HELPER = str(ROOT / "roles/user/files/safe_home_file.py")
SITE_VARS = yaml.safe_load((ROOT / "site.yml").read_text())[0]["vars"]
RETIRED = yaml.safe_load((ROOT / "roles/user/vars/main.yml").read_text())

USER_TASKS = {
    "Ensure sandbar's shell launcher directory exists",
    "Load sandbar's shell launchers in every interactive bash",
    "Remove launchers of coding agents this VM did not select",
    "Retire home copies of sandbar's agent launchers",
    "Check for ~/.bashrc before removing retired launcher lines",
    "Stop ~/.bashrc loading the retired Claude launcher",
    "Stop ~/.bashrc loading the retired Codex launcher",
    "Check the Codex boot unit's enablement link",
    "Drop the enablement link left pointing at the retired Codex unit",
}
AGENT_TASKS = {
    "claude-code": {"Install the Claude Code interactive launcher"},
    "codex": {"Install the Codex interactive launcher", "Ensure the system-wide user unit directory exists",
              "Install disabled Codex boot service"},
}

# What earlier releases put in ~/.bashrc to load the home copies.
LEGACY_BASHRC = (
    "# user customization\n"
    "# BEGIN ANSIBLE MANAGED BLOCK - Claude Code\n"
    '. "$HOME/.config/sandbar/claude-shell-wrapper.sh"\n'
    "# END ANSIBLE MANAGED BLOCK - Claude Code\n"
    '. "$HOME/.config/sandbar/codex-shell-wrapper.sh"\n'
)
HOME_UNIT = ".config/systemd/user/sandbar-codex-app-server.service"
WANTS_LINK = ".config/systemd/user/default.target.wants/sandbar-codex-app-server.service"


def select(path, names):
    tasks = [copy.deepcopy(task) for task in yaml.safe_load(path.read_text()) if task.get("name") in names]
    assert {task["name"] for task in tasks} == names, path
    return tasks


@unittest.skipUnless(shutil.which("ansible-playbook"), "ansible-playbook not installed")
class AgentLaunchersTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sand-launchers-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.home = self.root / "home"
        self.home.mkdir()
        system = self.root / "system"
        self.bashrc_d = system / "share/sandbar/bashrc.d"
        self.system_bashrc = system / "etc/bash.bashrc"
        self.unit_dir = system / "etc/systemd/user"
        group = grp.getgrgid(os.getgid()).gr_name

        def rewrite(value, role=None):
            if isinstance(value, str):
                return value.replace("/usr/local/libexec/sandbar-safe-home-file", HELPER)
            if isinstance(value, list):
                return [rewrite(part, role) for part in value]
            if isinstance(value, dict):
                value = {key: rewrite(part, role) for key, part in value.items()}
                # Production installs these as root; this test is unprivileged.
                if value.get("owner") == "root":
                    value.update(owner=getpass.getuser(), group=group)
                if role and "src" in value:
                    value["src"] = str(ROOT / "roles" / role / "files" / value["src"])
                return value
            return value

        self.user_tasks = rewrite(select(ROOT / "roles/user/tasks/main.yml", USER_TASKS))
        self.agent_tasks = {role: rewrite(select(ROOT / "roles" / role / "tasks/main.yml", names), role)
                            for role, names in AGENT_TASKS.items()}
        self.variables = {
            **SITE_VARS, **RETIRED,
            "sand_bashrc_d": str(self.bashrc_d),
            "sand_system_bashrc": str(self.system_bashrc),
            "sand_systemd_user_dir": str(self.unit_dir),
            "sand_skills_dir": str(system / "share/sandbar/skills"),
            "user_home": str(self.home),
            "user_name": getpass.getuser(),
            "provision_phase": "finalize",
            "toolset_claude": True,
            "toolset_codex": True,
            "sand_bashrc_writable": False,
        }

    def provision(self, **overrides):
        variables = {**self.variables, **overrides}
        # As in site.yml: roles/user always runs, an agent role only when selected.
        tasks = list(self.user_tasks)
        for role, selected in (("claude-code", "toolset_claude"), ("codex", "toolset_codex")):
            if variables[selected]:
                tasks += self.agent_tasks[role]
        playbook = self.root / "play.yml"
        playbook.write_text(yaml.safe_dump([{"hosts": "all", "gather_facts": False, "tasks": tasks}]))
        result = subprocess.run(
            ["ansible-playbook", "-i", "localhost,", "-c", "local", str(playbook), "-e", json.dumps(variables)],
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            env={**os.environ, "ANSIBLE_NOCOLOR": "1"})
        self.assertEqual(result.returncode, 0, result.stdout)
        return result.stdout

    def shell(self):
        """Start bash the way a guest login does: system rc, then ~/.bashrc."""
        script = '. "$1"; [ -f "$HOME/.bashrc" ] && . "$HOME/.bashrc"; declare -f claude; declare -f codex'
        return subprocess.run(
            ["bash", "--noprofile", "--norc", "-c", script, "bash", str(self.system_bashrc)],
            env={"HOME": str(self.home), "PATH": os.environ["PATH"]},
            text=True, capture_output=True)

    def legacy_home(self, claude_version="4434d0e"):
        for relative, fixture in ((".config/sandbar/claude-shell-wrapper.sh", "claude-shell-wrapper.sh." + claude_version),
                                  (".config/sandbar/codex-shell-wrapper.sh", "codex-shell-wrapper.sh.7caf5b4"),
                                  (HOME_UNIT, "sandbar-codex-app-server.service.7caf5b4")):
            path = self.home / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(FIXTURES / fixture, path)
        link = self.home / WANTS_LINK
        link.parent.mkdir(parents=True)
        link.symlink_to(self.home / HOME_UNIT)
        (self.home / ".bashrc").write_text(LEGACY_BASHRC)

    def assert_current_launchers(self):
        shell = self.shell()
        self.assertEqual(shell.returncode, 0, shell.stderr)
        self.assertEqual(shell.stderr, "")
        # Only the current Claude launcher asks the permission-mode question.
        self.assertIn("_sandbar_claude_permission_arg", shell.stdout)
        self.assertIn("codex ()", shell.stdout)

    def test_fresh_clone_loads_launchers_from_the_system_bashrc(self):
        self.provision(sand_bashrc_writable=True, sand_fresh_clone=True)
        self.assertEqual((self.bashrc_d / "claude.sh").read_bytes(),
                         (ROOT / "roles/claude-code/files/claude-shell-wrapper.sh").read_bytes())
        self.assertTrue((self.unit_dir / "sandbar-codex-app-server.service").is_file())
        self.assertFalse((self.home / ".config/sandbar/claude-shell-wrapper.sh").exists())
        self.assert_current_launchers()
        self.assertRegex(self.provision(sand_bashrc_writable=True), r"changed=0\s")

        # Deselecting an agent on a later full run takes its launcher away.
        self.provision(toolset_codex=False)
        self.assertTrue((self.bashrc_d / "claude.sh").exists())
        self.assertFalse((self.bashrc_d / "codex.sh").exists())
        self.assertFalse((self.unit_dir / "sandbar-codex-app-server.service").exists())
        shell = self.shell()
        self.assertNotIn("codex ()", shell.stdout)

    def test_legacy_home_with_unmanaged_bashrc_gets_stubs(self):
        self.legacy_home()
        self.provision(sand_preserved_home=True)
        for name in ("claude-shell-wrapper.sh", "codex-shell-wrapper.sh"):
            stub = (self.home / ".config/sandbar" / name).read_text()
            self.assertTrue(stub.startswith("# Sandbar now installs"), stub)
        # ~/.bashrc is the user's, so its lines stay and source the stubs.
        self.assertEqual((self.home / ".bashrc").read_text(), LEGACY_BASHRC)
        self.assertFalse((self.home / HOME_UNIT).exists())
        self.assertFalse(os.path.lexists(self.home / WANTS_LINK))
        self.assert_current_launchers()
        self.assertRegex(self.provision(sand_preserved_home=True), r"changed=0\s")

    def test_legacy_home_with_managed_bashrc_is_cleaned_up(self):
        self.legacy_home(claude_version="a65b56c")
        self.provision(sand_preserved_home=True, sand_bashrc_writable=True)
        for name in ("claude-shell-wrapper.sh", "codex-shell-wrapper.sh"):
            self.assertFalse((self.home / ".config/sandbar" / name).exists(), name)
        self.assertEqual((self.home / ".bashrc").read_text(), "# user customization\n")
        self.assertFalse((self.home / HOME_UNIT).exists())
        self.assert_current_launchers()

    def test_edited_launcher_is_kept_and_still_overrides(self):
        self.legacy_home()
        edited = self.home / ".config/sandbar/claude-shell-wrapper.sh"
        edited.write_text(edited.read_text() + "# my tweak\n")
        self.provision(sand_preserved_home=True, sand_bashrc_writable=True)
        self.assertTrue(edited.read_text().endswith("# my tweak\n"))
        bashrc = (self.home / ".bashrc").read_text()
        self.assertIn("claude-shell-wrapper.sh", bashrc)
        self.assertNotIn("codex-shell-wrapper.sh", bashrc)
        shell = self.shell()
        self.assertEqual(shell.stderr, "")
        self.assertNotIn("_sandbar_claude_permission_arg", shell.stdout)

    def test_retired_hashes_match_the_released_files(self):
        # The list is frozen; each fixture is a released version's exact bytes.
        for entry in RETIRED["sand_retired_home_files"]:
            name = Path(entry["path"]).name
            with self.subTest(path=entry["path"]):
                fixtures = [path for path in FIXTURES.iterdir() if path.name.rsplit(".", 1)[0] == name]
                self.assertTrue(fixtures)
                self.assertEqual(sorted(entry["sha256"]), sorted(
                    hashlib.sha256(path.read_bytes().rstrip()).hexdigest() for path in fixtures))


if __name__ == "__main__":
    unittest.main()
