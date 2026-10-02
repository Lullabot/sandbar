"""Exercise the real context role against isolated guest home directories."""

import getpass
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[1]
INSTRUCTIONS = {
    "claude": ".claude/CLAUDE.md",
    "codex": ".codex/AGENTS.md",
    "pi": ".pi/agent/AGENTS.md",
    "opencode": ".config/opencode/AGENTS.md",
}


class AgentContextTest(unittest.TestCase):
    def run_role(self, directory, home, phase="finalize", selected=(), successful=True):
        play = [{
            "hosts": "all", "gather_facts": False, "become": False,
            "roles": ["agent-context"],
        }]
        playbook = directory / "play.yml"
        playbook.write_text(yaml.safe_dump(play))
        variables = {
            "user_home": str(home), "user_name": getpass.getuser(),
            "provision_phase": phase,
            **{"toolset_" + agent: agent in selected for agent in INSTRUCTIONS},
        }
        result = subprocess.run(
            ["ansible-playbook", "-i", "localhost,", "-c", "local", str(playbook),
             "-e", json.dumps(variables)],
            cwd=directory, text=True, capture_output=True,
            env={**os.environ, "ANSIBLE_ROLES_PATH": str(ROOT / "roles"),
                 "ANSIBLE_NOCOLOR": "1"},
        )
        output = result.stdout + result.stderr
        if successful:
            self.assertEqual(result.returncode, 0, output)
        else:
            self.assertNotEqual(result.returncode, 0, output)
        return output

    def test_selected_agents_get_discoverable_context_only_outside_base(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for phase, selected in (("base", tuple(INSTRUCTIONS)),
                                    ("finalize", ()),
                                    ("full", tuple(INSTRUCTIONS)),
                                    *(("finalize", (agent,)) for agent in INSTRUCTIONS)):
                with self.subTest(phase=phase, selected=selected):
                    home = directory / (phase + "-" + "-".join(selected or ("none",)))
                    home.mkdir()
                    self.run_role(directory, home, phase, selected)
                    installed = set(selected) if phase != "base" else set()
                    for agent, relative in INSTRUCTIONS.items():
                        path = home / relative
                        self.assertEqual(path.exists(), agent in installed, str(path))
                        if agent in installed:
                            self.assertIn("Sandbar", path.read_text())
                            self.assertIn("sudo", path.read_text())
                    skill = home / ".agents/skills/sandbar-environment/SKILL.md"
                    self.assertEqual(skill.exists(), bool(installed))
                    claude_skill = home / ".claude/skills/sandbar-environment/SKILL.md"
                    self.assertEqual(claude_skill.exists(), "claude" in installed)
                    if installed:
                        self.assertIn("sandbar-environment", skill.read_text())
                        self.assertTrue((home / ".agents/skills/sandbar-environment/references/terminal.md").exists())
                        self.assertTrue((home / ".agents/skills/sandbar-environment/references/tools.md").exists())
                    if "claude" in installed:
                        self.assertEqual(claude_skill.resolve(), skill.resolve())

    def test_personal_content_modes_and_unrelated_skills_survive_refresh(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            home = directory / "guest"
            home.mkdir()
            personal = home / INSTRUCTIONS["codex"]
            personal.parent.mkdir(parents=True)
            personal.parent.chmod(0o700)
            personal.write_text("Personal instruction: use my editor.\n")
            personal.chmod(0o600)
            unrelated = home / ".agents/skills/my-own/SKILL.md"
            unrelated.parent.mkdir(parents=True)
            unrelated.write_text("My own skill\n")

            output = self.run_role(directory, home, selected=("codex", "claude"))
            first = personal.read_text()
            self.assertIn("Personal instruction: use my editor.", first)
            self.assertIn("Sandbar", first)
            self.assertEqual(personal.stat().st_mode & 0o777, 0o600)
            self.assertEqual(personal.parent.stat().st_mode & 0o777, 0o700)
            output = self.run_role(directory, home, selected=("codex", "claude"))
            self.assertRegex(output, r"changed=0\s")
            self.assertEqual(personal.read_text(), first)
            self.assertEqual(unrelated.read_text(), "My own skill\n")
            self.assertEqual(personal.stat().st_mode & 0o777, 0o600)
            self.assertEqual(personal.parent.stat().st_mode & 0o777, 0o700)

            skill = home / ".agents/skills/sandbar-environment/SKILL.md"
            skill.write_text(skill.read_text().replace("This session runs", "Stale session runs"))
            personal.write_text(personal.read_text().replace("You are working", "You were working"))
            self.run_role(directory, home, selected=("codex", "claude"))
            self.assertIn("This session runs", skill.read_text())
            self.assertIn("You are working", personal.read_text())
            self.assertIn("Personal instruction: use my editor.", personal.read_text())

    def test_user_owned_skill_collision_fails_without_replacement(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            home = directory / "guest"
            own = home / ".agents/skills/sandbar-environment/SKILL.md"
            own.parent.mkdir(parents=True)
            own.write_text("User-owned skill\n")
            output = self.run_role(directory, home, selected=("codex",), successful=False)
            self.assertIn("collision", output.lower())
            self.assertEqual(own.read_text(), "User-owned skill\n")

            own.unlink()
            own.parent.rmdir()
            claude = home / ".claude/skills/sandbar-environment"
            claude.mkdir(parents=True)
            (claude / "SKILL.md").write_text("Claude-owned skill\n")
            output = self.run_role(directory, home, selected=("claude",), successful=False)
            self.assertIn("collision", output.lower())
            self.assertEqual((claude / "SKILL.md").read_text(), "Claude-owned skill\n")

    def test_list_tasks_counts_real_role_banners(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            home = directory / "guest"
            home.mkdir()
            playbook = directory / "play.yml"
            playbook.write_text(yaml.safe_dump([{
                "hosts": "all", "gather_facts": False, "become": False,
                "roles": ["agent-context"],
            }]))
            for phase, selected in (("base", False), ("finalize", True)):
                with self.subTest(phase=phase):
                    variables = {
                        "user_home": str(home), "user_name": getpass.getuser(),
                        "provision_phase": phase, "toolset_codex": selected,
                    }
                    argv = ["ansible-playbook", "-i", "localhost,", "-c", "local",
                            str(playbook), "-e", json.dumps(variables)]
                    env = {**os.environ, "ANSIBLE_ROLES_PATH": str(ROOT / "roles"),
                           "ANSIBLE_NOCOLOR": "1"}
                    listing = subprocess.run([*argv, "--list-tasks"], cwd=directory,
                                             capture_output=True, text=True, env=env)
                    run = subprocess.run(argv, cwd=directory, capture_output=True,
                                         text=True, env=env)
                    self.assertEqual(listing.returncode, 0, listing.stdout + listing.stderr)
                    self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
                    listed = len(re.findall(r"^ {6}\S", listing.stdout, re.MULTILINE))
                    banners = len(re.findall(r"^TASK \[", run.stdout, re.MULTILINE))
                    self.assertGreater(listed, 0)
                    self.assertEqual(listed, banners, listing.stdout + run.stdout)


if __name__ == "__main__":
    unittest.main()
