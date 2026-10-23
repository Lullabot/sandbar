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
            # This fixture substitutes every role body, so it does not create
            # the user_home fact consumed by the real final baseline task.
            play[0].pop("post_tasks", None)
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
                        for role in (*AGENTS, "agent-cleanup", "agent-clipboard"):
                            (directory / role).unlink(missing_ok=True)
                        variables = {"provision_phase": phase, **{
                            "toolset_" + ("claude" if role == "claude-code" else role): role in selected
                            for role in AGENTS}}
                        self.run_play(directory, play, variables)
                        for role in AGENTS:
                            self.assertEqual((directory / role).exists(), role in selected and phase != "base", role)
                        self.assertEqual((directory / "agent-cleanup").exists(), phase == "base")
                        self.assertEqual((directory / "agent-clipboard").exists(), phase != "finalize")

    def test_opencode_uses_current_official_npm_package(self):
        tasks = yaml.safe_load((ROOT / "roles" / "opencode" / "tasks" / "main.yml").read_text())
        install = next(task for task in tasks if task["name"] == "Install the latest OpenCode release")
        argv = install["ansible.builtin.command"]["argv"]
        self.assertIn("@opencode/cli@latest", argv)
        self.assertNotIn("opencode-ai@latest", argv)

    def test_claude_remote_control_onboarding(self):
        result = subprocess.run(
            ["bash", str(ROOT / "roles/claude-code/tests/test-claude-shell-wrapper.sh")],
            text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        )
        self.assertEqual(result.returncode, 0, result.stdout)

        settings = json.loads(
            (ROOT / "roles/claude-code/templates/claude-settings.json.j2").read_text()
        )
        self.assertIs(settings["remoteControlAtStartup"], False)
        self.assertIs(settings["isolatePeerMachines"], True)

    def test_claude_native_notification_channel(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            home = directory / "guest"
            settings = home / ".claude/settings.json"
            settings.parent.mkdir(parents=True)
            all_tasks = yaml.safe_load((ROOT / "roles/claude-code/tasks/main.yml").read_text())
            names = {"Check for prior Claude remote-control onboarding",
                     "Read Claude settings for safe communication defaults",
                     "Isolate peer machines and keep Remote Control opt-in"}
            tasks = [task for task in all_tasks if task["name"] in names]
            for task in tasks:
                if "ansible.builtin.copy" in task:
                    task["ansible.builtin.copy"]["group"] = grp.getgrgid(os.getgid()).gr_name
            play = [{"hosts": "all", "gather_facts": False, "tasks": tasks}]
            for channel in ("iterm2", "kitty", "ghostty", "", "unknown"):
                with self.subTest(channel=channel):
                    original = {"theme": "dark", "preferredNotifChannel": "terminal_bell"}
                    settings.write_text(json.dumps(original))
                    settings.chmod(0o600)
                    variables = {"user_home": str(home), "user_name": getpass.getuser(),
                                 "claude_notification_channel": channel,
                                  # This fixture exercises the communication merge
                                  # after the installer has accepted a sand default.
                                  "sand_claude_settings_install": {
                                      "stdout": "unchanged: installed default current"}}
                    self.run_play(directory, play, variables)
                    result = json.loads(settings.read_text())
                    self.assertEqual(result["theme"], "dark")
                    self.assertEqual(result["preferredNotifChannel"],
                                     channel if channel in ("iterm2", "kitty", "ghostty") else "terminal_bell")
                    self.assertEqual(settings.stat().st_mode & 0o777, 0o600)
                    output = self.run_play(directory, play, variables)
                    self.assertIn("changed=0", output)

    def test_legacy_claude_settings_are_preserved_with_new_defaults_offered(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            home = directory / "guest"
            settings = home / ".claude/settings.json"
            settings.parent.mkdir(parents=True)
            settings.write_text('{"theme":"dark","remoteControlAtStartup":true}\n')
            settings.chmod(0o600)

            role = ROOT / "roles/claude-code"
            all_tasks = yaml.safe_load((role / "tasks/main.yml").read_text())
            names = {
                "Deploy ~/.claude/settings.json from template",
                "Check for prior Claude remote-control onboarding",
                "Read Claude settings for safe communication defaults",
                "Isolate peer machines and keep Remote Control opt-in",
            }
            tasks = [task for task in all_tasks if task["name"] in names]
            installer = next(task for task in tasks if task["name"] == "Deploy ~/.claude/settings.json from template")
            installer["ansible.builtin.command"]["argv"] = installer["ansible.builtin.command"]["argv"].replace(
                "/usr/local/libexec/sandbar-safe-home-file", str(ROOT / "roles/user/files/safe_home_file.py"))
            installer["ansible.builtin.command"]["stdin"] = installer["ansible.builtin.command"]["stdin"].replace(
                "claude-settings.json.j2", str(role / "templates/claude-settings.json.j2"))
            group = grp.getgrgid(os.getgid()).gr_name
            for task in tasks:
                module = task.get("ansible.builtin.copy")
                if module:
                    module["group"] = group

            variables = {"user_home": str(home), "user_name": getpass.getuser(), "sand_preserved_home": True, "claude_notification_channel": "kitty"}
            play = [{"hosts": "all", "gather_facts": False, "tasks": tasks}]
            self.run_play(directory, play, variables)
            migrated = json.loads(settings.read_text())
            self.assertIs(migrated["remoteControlAtStartup"], True)
            self.assertEqual(migrated["theme"], "dark")
            self.assertNotIn("preferredNotifChannel", migrated)
            self.assertEqual(settings.stat().st_mode & 0o777, 0o600)
            proposal = json.loads((home / ".config/sandbar/proposed-defaults/.claude/settings.json").read_text())
            self.assertIs(proposal["remoteControlAtStartup"], False)
            self.assertIs(proposal["isolatePeerMachines"], True)

            marker = home / ".config/sandbar/claude-remote-control-onboarding-complete"
            marker.parent.mkdir(parents=True, exist_ok=True)
            marker.touch()
            migrated["remoteControlAtStartup"] = True
            migrated["isolatePeerMachines"] = False
            settings.write_text(json.dumps(migrated))
            self.run_play(directory, play, variables)
            opted_in = json.loads(settings.read_text())
            self.assertIs(opted_in["remoteControlAtStartup"], True)
            self.assertIs(opted_in["isolatePeerMachines"], False)

    def test_agent_clipboard_shims_are_image_only(self):
        role = ROOT / "roles" / "agent-clipboard"
        tasks_text = (role / "tasks" / "main.yml").read_text()
        self.assertIn("Xvfb :99", tasks_text)
        self.assertIn("-nolisten tcp", tasks_text)
        self.assertIn("DISPLAY=:99", tasks_text)

        with tempfile.TemporaryDirectory() as temporary:
            home = Path(temporary)
            slot = home / ".sand" / "clip" / "latest.png"
            slot.parent.mkdir(parents=True)
            slot.write_bytes(b"png-image-bytes")
            env = {**os.environ, "HOME": str(home)}

            def shim(name, *args):
                return subprocess.run(
                    ["/bin/sh", str(role / "files" / name), *args],
                    env=env, check=True, capture_output=True,
                ).stdout

            self.assertEqual(shim("sand-xclip", "-t", "TARGETS", "-o"), b"image/png\n")
            self.assertEqual(shim("sand-xclip", "-t", "image/png", "-o"), b"png-image-bytes")
            self.assertEqual(shim("sand-xclip", "-t", "text/plain", "-o"), b"")
            self.assertEqual(shim("sand-wl-paste", "--list-types"), b"image/png\n")
            self.assertEqual(shim("sand-wl-paste", "--type", "image/png"), b"png-image-bytes")
            self.assertEqual(shim("sand-wl-paste", "--type", "text/plain"), b"")

    def test_preserved_settings_and_installer_refresh(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            home = directory / "guest"
            home.mkdir()
            variables = {"user_home": str(home), "user_name": getpass.getuser(), "sand_fresh_clone": True}
            for role, relative in (("claude-code", ".claude/settings.json"), ("codex", ".codex/config.toml")):
                variables.update(sand_fresh_clone=True, sand_preserved_agents=False)
                target = home / relative
                target.parent.mkdir(parents=True)
                source = ROOT / "roles" / role
                tasks = yaml.safe_load((source / "tasks/main.yml").read_text())
                setting_task = next(task for task in tasks if task["name"].startswith("Deploy ~/"))
                command = setting_task["ansible.builtin.command"]
                command["argv"] = command["argv"].replace(
                    "/usr/local/libexec/sandbar-safe-home-file", str(ROOT / "roles/user/files/safe_home_file.py"))
                template_name = "claude-settings.json.j2" if role == "claude-code" else "codex-config.toml.j2"
                command["stdin"] = command["stdin"].replace(template_name, str(source / "templates" / template_name))
                play = [{"hosts": "all", "gather_facts": False, "tasks": [setting_task]}]
                self.run_play(directory, play, variables)
                self.assertTrue(target.read_text())
                target.write_text("preserved custom settings\n")
                target.chmod(0o600)
                variables.update(sand_fresh_clone=False, sand_preserved_agents=True)
                self.run_play(directory, play, variables)
                self.assertEqual(target.read_text(), "preserved custom settings\n")
                self.assertEqual(target.stat().st_mode & 0o777, 0o600)
                self.assertTrue((home / ".config/sandbar/proposed-defaults" / relative).is_file())
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
                             ".config/sandbar/claude-shell-wrapper.sh",
                             ".config/sandbar/claude-remote-control-onboarding-complete",
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
            variables["sand_bashrc_writable"] = True
            self.run_play(directory, [{"hosts": "all", "gather_facts": False,
                                       "tasks": [launcher]}], variables)
            self.assertIn("claude-shell-wrapper.sh", bashrc.read_text())
            syntax = subprocess.run(["bash", "-n", str(bashrc)], capture_output=True, text=True)
            self.assertEqual(syntax.returncode, 0, syntax.stderr)


if __name__ == "__main__":
    unittest.main()
