"""Run the real user-role writes against a temporary home through Ansible."""

import copy
import os
from pathlib import Path
import pwd
import shutil
import subprocess
import tempfile
import unittest

import yaml


REPO = Path(__file__).resolve().parents[1]
TASKS = {
    "Check whether preserved shell configuration is unchanged",
    "Set preserved shell write permissions",
    "Deploy ~/.tmux.conf",
    "Deploy ~/.gitconfig",
    "Deploy ~/.config/direnv/direnv.toml",
    "Deploy bashrc customizations",
    "Source the sandbar secrets file from ~/.profile (login shells)",
    "Offer shell defaults for edited or unknown preserved files",
}


@unittest.skipUnless(shutil.which("ansible-playbook"), "ansible-playbook not installed")
class HomeRoleTest(unittest.TestCase):
    def test_ansible_install_preserve_and_idempotent_rerun(self):
        role_tasks = yaml.safe_load((REPO / "roles/user/tasks/main.yml").read_text())
        selected = [copy.deepcopy(task) for task in role_tasks if task.get("name") in TASKS]
        self.assertEqual({task["name"] for task in selected}, TASKS)
        selected.extend(copy.deepcopy(yaml.safe_load((REPO / "site.yml").read_text())[0]["post_tasks"]))

        # Production deploys the helper under /usr/local/libexec. Point these
        # same tasks at its source path while running against an unprivileged
        # temporary home; no VM or host state is touched.
        helper = str(REPO / "roles/user/files/safe_home_file.py")

        def rewrite(value):
            if isinstance(value, str):
                return value.replace("/usr/local/libexec/sandbar-safe-home-file", helper)
            if isinstance(value, list):
                return [rewrite(part) for part in value]
            if isinstance(value, dict):
                return {key: rewrite(part) for key, part in value.items()}
            return value

        selected = rewrite(selected)
        with tempfile.TemporaryDirectory(prefix="sand-home-role-") as directory:
            root = Path(directory)
            home = root / "home"
            home.mkdir()
            (root / "templates").symlink_to(REPO / "roles/user/templates", target_is_directory=True)
            for name, body in ((".bashrc", "# base shell\n"), (".profile", "# base login\n"), (".tmux.conf", "# inherited old default\n")):
                (home / name).write_text(body)
            variables = {
                **yaml.safe_load((REPO / "roles/user/defaults/main.yml").read_text()),
                "user_name": pwd.getpwuid(os.getuid()).pw_name,
                "user_home": str(home),
                "provision_phase": "finalize",
                "sand_fresh_clone": True,
                "sand_preserved_home": False,
                "sand_preserved_agents": False,
            }

            def run(label):
                play = [{"name": label, "hosts": "localhost", "gather_facts": False, "vars": variables, "tasks": selected}]
                playbook = root / "check.yml"
                playbook.write_text(yaml.safe_dump(play, sort_keys=False))
                result = subprocess.run(
                    ["ansible-playbook", "-i", "localhost,", "--connection=local", str(playbook)],
                    capture_output=True, text=True,
                )
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                return result.stdout

            run("fresh clone installs defaults")
            self.assertIn("set -g prefix C-a", (home / ".tmux.conf").read_text())
            self.assertIn("[user]", (home / ".gitconfig").read_text())
            names = [".tmux.conf", ".gitconfig", ".bashrc", ".profile", ".config/direnv/direnv.toml"]
            for name in names:
                self.assertTrue((home / ".config/sandbar/home-baselines" / name).is_file(), name)

            # The project role appends this managed GitLab include after the
            # user role. Model its final baseline and ensure a preserve-home
            # finalize carries the include even when the identity is refreshed.
            managed_include = "# BEGIN sandbar git-credentials\n[includeIf \"gitdir:~/gitlab.com/team/\"]\n    path = ~/.config/sandbar/gitconfig.d/team\n# END sandbar git-credentials\n"
            gitconfig = home / ".gitconfig"
            gitconfig.write_text(gitconfig.read_text().rstrip("\n") + "\n\n" + managed_include)
            subprocess.run(
                ["python3", helper, str(home), ".gitconfig", variables["user_name"], "0644", "--record-current"],
                check=True, capture_output=True,
            )
            variables.update(sand_fresh_clone=False, sand_preserved_home=True)
            run("tracked GitLab include survives preserved home")
            self.assertIn(managed_include, gitconfig.read_text())

            for name in names:
                (home / name).write_text("USER EDIT " + name + "\n")

            run("preserved home keeps edits")
            for name in names:
                self.assertEqual((home / name).read_text(), "USER EDIT " + name + "\n")
                self.assertTrue((home / ".config/sandbar/proposed-defaults" / name).is_file(), name)
            rerun = run("preserved home rerun")
            self.assertRegex(rerun, r"changed=0\s+unreachable=0\s+failed=0")


if __name__ == "__main__":
    unittest.main()
