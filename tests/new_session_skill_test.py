"""Run with PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p '*_test.py'.

The Claude Code new-session skill is installed outside the home, like the
agent launchers, and linked into ~/.claude/skills. These tests run the
production tasks through Ansible with temporary directories standing in for
/usr/local/share and the guest home, then run the skill's cc-new script
against a fake `claude` to check the command line a new session would get.
"""
import copy
import getpass
import grp
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[1]
SKILL = ROOT / "roles/claude-code/files/new-session"
SITE_VARS = yaml.safe_load((ROOT / "site.yml").read_text())[0]["vars"]

USER_TASKS = {
    "Remove launchers of coding agents this VM did not select",
    "Check for sandbar's new-session skill link",
    "Remove sandbar's new-session skill link from a VM without Claude",
}
CLAUDE_TASKS = {
    "Ensure sandbar's skill directories exist",
    "Install the Claude Code new-session skill",
    "Inspect Claude's skills directory",
    "Inspect Claude's skills directory without following a link",
    "Inspect the new-session skill link",
    "Decide whether the new-session skill link is sandbar's to write",
    "Ensure Claude skill directory exists for the new-session skill",
    "Expose the new-session skill to Claude",
    "Report a new-session skill sandbar left in place",
}


def select(path, names):
    tasks = [copy.deepcopy(task) for task in yaml.safe_load(path.read_text()) if task.get("name") in names]
    assert {task["name"] for task in tasks} == names, path
    return tasks


@unittest.skipUnless(shutil.which("ansible-playbook"), "ansible-playbook not installed")
class NewSessionSkillProvisionTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sand-new-session-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.home = self.root / "home"
        (self.home / ".claude").mkdir(parents=True)
        system = self.root / "system/share/sandbar"
        self.skills_dir = system / "skills"
        self.installed = self.skills_dir / "new-session"
        self.link = self.home / ".claude/skills/new-session"
        group = grp.getgrgid(os.getgid()).gr_name

        def rewrite(value):
            if isinstance(value, list):
                return [rewrite(part) for part in value]
            if isinstance(value, dict):
                value = {key: rewrite(part) for key, part in value.items()}
                # Production installs these as root; this test is unprivileged.
                if value.get("owner") == "root":
                    value.update(owner=getpass.getuser(), group=group)
                if "src" in value and "new-session/" in str(value["src"]):
                    value["src"] = str(ROOT / "roles/claude-code/files" / value["src"])
                return value
            return value

        self.user_tasks = rewrite(select(ROOT / "roles/user/tasks/main.yml", USER_TASKS))
        self.claude_tasks = rewrite(select(ROOT / "roles/claude-code/tasks/main.yml", CLAUDE_TASKS))
        self.variables = {
            **SITE_VARS,
            "sand_bashrc_d": str(system / "bashrc.d"),
            "sand_systemd_user_dir": str(self.root / "system/etc/systemd/user"),
            "sand_skills_dir": str(self.skills_dir),
            "user_home": str(self.home),
            "user_name": getpass.getuser(),
            "provision_phase": "finalize",
            "toolset_claude": True,
            "toolset_codex": False,
        }

    def provision(self, **overrides):
        variables = {**self.variables, **overrides}
        # As in site.yml: roles/user always runs, claude-code only when selected.
        tasks = list(self.user_tasks)
        if variables["toolset_claude"]:
            tasks += self.claude_tasks
        playbook = self.root / "play.yml"
        playbook.write_text(yaml.safe_dump([{"hosts": "all", "gather_facts": False, "tasks": tasks}]))
        result = subprocess.run(
            ["ansible-playbook", "-i", "localhost,", "-c", "local", str(playbook), "-e", json.dumps(variables)],
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            env={**os.environ, "ANSIBLE_NOCOLOR": "1"})
        self.assertEqual(result.returncode, 0, result.stdout)
        return result.stdout

    def mode(self, path):
        return stat.S_IMODE(path.lstat().st_mode)

    def assert_installed(self):
        for name, mode in (("SKILL.md", 0o644), ("cc-new", 0o755)):
            path = self.installed / name
            self.assertEqual(path.read_bytes(), (SKILL / name).read_bytes(), name)
            self.assertEqual(self.mode(path), mode, name)
        self.assertEqual(self.mode(self.skills_dir), 0o755)
        self.assertEqual(self.mode(self.installed), 0o755)

    def test_installs_skill_and_links_it_for_claude(self):
        output = self.provision()
        self.assertNotIn("is not sandbar's link", output)
        self.assert_installed()
        self.assertTrue(self.link.is_symlink())
        self.assertEqual(os.readlink(self.link), str(self.installed))
        self.assertEqual((self.link / "SKILL.md").read_bytes(), (SKILL / "SKILL.md").read_bytes())
        self.assertEqual(self.link.lstat().st_uid, os.getuid())
        self.assertRegex(self.provision(), r"changed=0\s")

    def test_creates_missing_claude_skills_directory(self):
        shutil.rmtree(self.home / ".claude")
        self.provision()
        self.assertTrue((self.home / ".claude/skills").is_dir())
        self.assertEqual(os.readlink(self.link), str(self.installed))

    def test_keeps_a_skill_in_a_linked_skills_directory(self):
        dotfiles = self.root / "dotfiles/skills"
        dotfiles.mkdir(parents=True)
        (self.home / ".claude/skills").symlink_to(dotfiles)
        self.provision()
        self.assertEqual(os.readlink(dotfiles / "new-session"), str(self.installed))

    def test_user_content_at_the_link_path_is_left_alone(self):
        elsewhere = self.root / "my-skill"
        elsewhere.mkdir()
        (self.home / ".claude/skills").mkdir()
        cases = {
            "directory": lambda: (self.link.mkdir(), (self.link / "SKILL.md").write_text("mine\n")),
            "foreign link": lambda: self.link.symlink_to(elsewhere),
            "dangling link": lambda: self.link.symlink_to(self.root / "missing"),
            "file": lambda: self.link.write_text("mine\n"),
        }
        for label, create in cases.items():
            with self.subTest(case=label):
                if self.link.is_symlink() or self.link.is_file():
                    self.link.unlink()
                elif self.link.exists():
                    shutil.rmtree(self.link)
                create()
                before = (os.readlink(self.link) if self.link.is_symlink()
                          else (self.link / "SKILL.md").read_text() if self.link.is_dir()
                          else self.link.read_text())
                output = self.provision()
                self.assertIn("is not sandbar's link", output)
                after = (os.readlink(self.link) if self.link.is_symlink()
                         else (self.link / "SKILL.md").read_text() if self.link.is_dir()
                         else self.link.read_text())
                self.assertEqual(after, before)
                # The system copy is still installed; only the link is skipped.
                self.assert_installed()
                # A later deselection does not remove what is not sandbar's.
                self.provision(toolset_claude=False)
                self.assertTrue(os.path.lexists(self.link))

    def test_skills_path_that_is_not_a_directory_is_left_alone(self):
        (self.home / ".claude/skills").write_text("not a directory\n")
        self.assertIn("is not sandbar's link", self.provision())
        self.assertEqual((self.home / ".claude/skills").read_text(), "not a directory\n")

    def test_deselecting_claude_removes_the_skill(self):
        self.provision()
        self.provision(toolset_claude=False)
        self.assertFalse(self.installed.exists())
        self.assertFalse(os.path.lexists(self.link))
        self.assertRegex(self.provision(toolset_claude=False), r"changed=0\s")

    def test_base_phase_never_keeps_the_skill(self):
        self.provision()
        self.provision(provision_phase="base", toolset_claude=False)
        self.assertFalse(self.installed.exists())


class CcNewTest(unittest.TestCase):
    """Run cc-new with a fake `claude` that records the argv and env it got."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="sand-cc-new-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.home = self.root / "home"
        (self.home / ".claude").mkdir(parents=True)
        self.settings = self.home / ".claude/settings.json"
        self.settings.write_text(json.dumps({"remoteControlAtStartup": True}))
        self.record = self.root / "record.json"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.fake = self.bin / "claude"
        self.fake.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, sys\n"
            f"json.dump({{'argv': sys.argv[1:], 'cwd': os.getcwd(), 'env': dict(os.environ)}}, open({str(self.record)!r}, 'w'))\n")
        self.fake.chmod(0o755)
        self.work = self.root / "work"
        self.work.mkdir()
        # Only system tools: the developer's real claude must never be found.
        self.system_path = "/usr/bin:/bin"

    def run_cc_new(self, *args, path=None, **env):
        environment = {"HOME": str(self.home), "PATH": path or f"{self.bin}:{self.system_path}", **env}
        return subprocess.run([str(SKILL / "cc-new"), *args], env=environment,
                              text=True, capture_output=True)

    def recorded(self):
        return json.loads(self.record.read_text())

    def test_starts_a_detached_session_in_the_directory(self):
        result = self.run_cc_new("--here", "--name", "my session!", str(self.work), "fix", "the bug")
        self.assertEqual(result.returncode, 0, result.stderr)
        call = self.recorded()
        self.assertEqual(call["argv"], ["--bg", "-n", "my-session", "--permission-mode", "default", "fix the bug"])
        self.assertEqual(Path(call["cwd"]).resolve(), self.work.resolve())

    def test_git_repository_gets_a_worktree(self):
        subprocess.run(["git", "init", "-q", str(self.work)], check=True)
        result = self.run_cc_new("--name", "feature", str(self.work))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.recorded()["argv"],
                         ["--bg", "-n", "feature", "--permission-mode", "default", "-w", "feature"])

    def test_inherits_the_callers_mode_and_not_its_identity(self):
        transcript = self.home / ".claude/projects/some-project/abc123.jsonl"
        transcript.parent.mkdir(parents=True)
        transcript.write_text('{"permissionMode":"default"}\n{"permissionMode":"acceptEdits"}\n')
        result = self.run_cc_new("--here", "--name", "x", str(self.work),
                                 CLAUDE_CODE_SESSION_ID="abc123", CLAUDECODE="1",
                                 CLAUDE_CODE_MESSAGING_TOKEN="secret")
        self.assertEqual(result.returncode, 0, result.stderr)
        call = self.recorded()
        self.assertEqual(call["argv"][:4], ["--bg", "-n", "x", "--permission-mode"])
        self.assertEqual(call["argv"][4], "acceptEdits")
        for name in ("CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "CLAUDE_CODE_MESSAGING_TOKEN"):
            self.assertNotIn(name, call["env"])

    def test_falls_back_to_local_bin_when_claude_is_not_on_path(self):
        local_bin = self.home / ".local/bin"
        local_bin.mkdir(parents=True)
        shutil.move(self.fake, local_bin / "claude")
        result = self.run_cc_new("--here", "--name", "x", str(self.work), path=self.system_path)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.recorded()["argv"][:3], ["--bg", "-n", "x"])

    def test_refuses_without_remote_control(self):
        for label, content in (("off", {"remoteControlAtStartup": False}), ("unset", {}), ("missing", None)):
            with self.subTest(settings=label):
                if content is None:
                    self.settings.unlink(missing_ok=True)
                else:
                    self.settings.write_text(json.dumps(content))
                result = self.run_cc_new("--here", str(self.work))
                self.assertEqual(result.returncode, 3, result.stderr)
                self.assertIn("remoteControlAtStartup", result.stderr)
                self.assertIn("No session was started", result.stderr)
                self.assertFalse(self.record.exists())

    def test_remote_control_check_can_be_skipped(self):
        self.settings.write_text("{}")
        result = self.run_cc_new("--here", "--name", "x", str(self.work), CC_NEW_SKIP_REMOTE_CHECK="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.record.exists())


if __name__ == "__main__":
    unittest.main()
