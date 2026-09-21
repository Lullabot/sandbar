"""Run with PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 -m unittest discover -s tests -p '*_test.py'.

Execute the production phase expressions with harmless role bodies, then execute
the actual settings and cleanup tasks against temporary guest home directories.
No installer, package manager, or privileged host task is executed.
"""
import getpass
import grp
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[1]
AGENTS = ("claude-code", "codex", "opencode", "pi")


class AgentRolesTest(unittest.TestCase):
    def run_play(self, directory, play, variables):
        path = directory / "site.yml"
        path.write_text(yaml.safe_dump(play))
        result = subprocess.run(
            ["ansible-playbook", "-i", "localhost,", "-c", "local", str(path),
             "-e", json.dumps(variables)],
            cwd=directory, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            env={**os.environ, "ANSIBLE_NOCOLOR": "1"},
        )
        self.assertEqual(result.returncode, 0, result.stdout)
        return result.stdout

    def test_phase_selection(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            play = yaml.safe_load((ROOT / "site.yml").read_text())
            play[0]["become"] = False
            play[0]["gather_facts"] = False
            # Only role bodies are replaced; production role selection and its
            # phase conditions are evaluated by Ansible itself.
            for entry in play[0]["roles"]:
                role = entry if isinstance(entry, str) else entry["role"]
                tasks = directory / "roles" / role / "tasks"
                tasks.mkdir(parents=True)
                (tasks / "main.yml").write_text(yaml.safe_dump([
                    {"ansible.builtin.copy": {"content": role, "dest": str(directory / role)}}
                ]))
            for phase in ("base", "finalize", "full"):
                selections = [set(), set(AGENTS)]
                if phase == "finalize":
                    selections.extend({role} for role in AGENTS)
                for selected in selections:
                    with self.subTest(phase=phase, selected=sorted(selected)):
                        for role in (*AGENTS, "agent-cleanup"):
                            (directory / role).unlink(missing_ok=True)
                        variables = {"provision_phase": phase, **{
                            "toolset_" + ("claude" if role == "claude-code" else role): role in selected
                            for role in AGENTS}}
                        self.run_play(directory, play, variables)
                        for role in AGENTS:
                            self.assertEqual((directory / role).exists(), role in selected and phase != "base", role)
                        self.assertEqual((directory / "agent-cleanup").exists(), phase == "base")

    def test_preserved_settings_and_installer_refresh(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            home = directory / "guest"
            home.mkdir()
            variables = {"user_home": str(home), "user_name": getpass.getuser()}
            for role, relative in (("claude-code", ".claude/settings.json"), ("codex", ".codex/config.toml")):
                target = home / relative
                target.parent.mkdir(parents=True)
                source = ROOT / "roles" / role
                tasks = yaml.safe_load((source / "tasks/main.yml").read_text())
                templates = [task for task in tasks if "ansible.builtin.template" in task]
                for task in templates:
                    task["ansible.builtin.template"]["src"] = str(source / "templates" / task["ansible.builtin.template"]["src"])
                    task["ansible.builtin.template"]["group"] = grp.getgrgid(os.getgid()).gr_name
                play = [{"hosts": "all", "gather_facts": False, "tasks": templates}]
                self.run_play(directory, play, variables)
                self.assertTrue(target.read_text())
                target.write_text("preserved custom settings\n")
                target.chmod(0o600)
                self.run_play(directory, play, variables)
                self.assertEqual(target.read_text(), "preserved custom settings\n")
                self.assertEqual(target.stat().st_mode & 0o777, 0o600)
                # Execute the real installer shell against a fake download
                # boundary. A restored binary must not suppress release fetching.
                installers = [task for task in tasks if "ansible.builtin.shell" in task]
                self.assertTrue(installers)
                fake_bin = directory / "bin"
                fake_bin.mkdir(exist_ok=True)
                curl = fake_bin / "curl"
                curl.write_text('#!/bin/sh\nprintf \'printf refreshed >> "$INSTALLER_TRACE"\\n\'\n')
                curl.chmod(0o755)
                binary = home / ".local/bin" / ("claude" if role == "claude-code" else role)
                binary.parent.mkdir(parents=True, exist_ok=True)
                binary.write_text("restored old binary")
                trace = directory / (role + "-installer-trace")
                for task in installers:
                    task["become"] = False
                    task.setdefault("environment", {}).update({
                        "PATH": str(fake_bin) + os.pathsep + os.defpath,
                        "INSTALLER_TRACE": str(trace),
                    })
                self.run_play(directory, [{"hosts": "all", "gather_facts": False,
                                           "tasks": installers}], variables)
                self.assertEqual(trace.read_text(), "refreshed")

    def test_base_migration_removes_only_agent_artifacts(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            home = directory / "guest"
            home.mkdir()
            old_artifacts = (".local/bin/claude", ".local/share/claude/versions/old",
                             ".claude/settings.json", ".claude.json",
                             ".local/bin/codex", ".codex/packages/standalone/current/codex")
            for relative in (*old_artifacts, ".local/bin/other", ".local/share/other/data",
                             ".config/sandbar/secrets.env"):
                path = home / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("keep unless an agent\n")
            bashrc = home / ".bashrc"
            bashrc.write_text(
                "# user customization\n"
                "# BEGIN ANSIBLE MANAGED BLOCK\n"
                "claude() { command claude; }\n"
                "# END ANSIBLE MANAGED BLOCK\n"
            )
            tasks = yaml.safe_load((ROOT / "roles/user/tasks/main.yml").read_text())
            common = next(task for task in tasks if task["name"] == "Deploy bashrc customizations")
            cleanup = yaml.safe_load((ROOT / "roles/agent-cleanup/tasks/main.yml").read_text())
            cleanup = [task for task in cleanup if "ansible.builtin.getent" not in task
                       and "ansible.builtin.set_fact" not in task]
            variables = {"user_home": str(home), "provision_phase": "base"}
            self.run_play(directory, [{"hosts": "all", "gather_facts": False,
                                       "tasks": [common, *cleanup]}], variables)
            for relative in old_artifacts:
                self.assertFalse((home / relative).exists(), relative)
            for relative in (".local/bin/other", ".local/share/other/data", ".config/sandbar/secrets.env"):
                self.assertEqual((home / relative).read_text(), "keep unless an agent\n")
            self.assertNotIn("claude()", bashrc.read_text())
            for retained in ("# user customization", "direnv hook bash", "secrets.env", "export EDITOR=vim"):
                self.assertIn(retained, bashrc.read_text())

            # Only selecting Claude adds its launcher back to this clean base.
            tasks = yaml.safe_load((ROOT / "roles/claude-code/tasks/main.yml").read_text())
            launcher = next(task for task in tasks if task["name"] == "Configure the Claude Code interactive launcher")
            launcher["ansible.builtin.blockinfile"]["group"] = grp.getgrgid(os.getgid()).gr_name
            variables["user_name"] = getpass.getuser()
            self.run_play(directory, [{"hosts": "all", "gather_facts": False,
                                       "tasks": [launcher]}], variables)
            self.assertIn("claude()", bashrc.read_text())
            syntax = subprocess.run(["bash", "-n", str(bashrc)], capture_output=True, text=True)
            self.assertEqual(syntax.returncode, 0, syntax.stderr)


if __name__ == "__main__":
    unittest.main()
