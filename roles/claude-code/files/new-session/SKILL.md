---
name: new-session
description: Start a new, separate Claude Code session on this machine that appears in Remote Control (claude.ai/code and the iOS/iPadOS app). Use when the user asks to open, start, or spawn a new session/chat, optionally in a given directory with a first prompt.
---

The mobile apps can only start cloud sessions. This skill starts a local one
in the background; it registers with Remote Control on its own as long as
`remoteControlAtStartup` is `true` in `~/.claude/settings.json`.

Run the `cc-new` script that sits in this skill's base directory:

```
<skill base directory>/cc-new [--here] [--name NAME] DIR [PROMPT...]
```

- **DIR**: the directory the user named. If they named a repo loosely (e.g.
  "sandbar"), look for it in the checkout layout `~/<host>/<group>/<repo>`,
  where the group can have subgroups: for example
  `~/github.com/<org>/<repo>`, `~/gitlab.com/<group>/<subgroup>/<repo>` or
  `~/git.drupalcode.org/project/<repo>`. If more than one checkout matches,
  ask which one. If none was given, use the current working directory.
- **PROMPT**: the task they want the new session to start on, passed as the
  remaining arguments. Omit it if they gave none.
- **--name**: pass one only if the user chose a name; otherwise the script
  derives `<dir>-<HHMM>`.
- **--here**: only when the user wants to work in the directory itself (for
  example, to continue the branch checked out there). By default a git repo
  gets a fresh worktree branched from `origin/main`, so the new session
  doesn't collide with this one.

The new session inherits this session's permission mode. Set
`CC_NEW_PERMISSION_MODE` only if the user asks for a different one.

`cc-new` refuses to start a session when `remoteControlAtStartup` is not
`true`, because that session would run but never appear in the app. When it
exits with that message, do not retry or edit the setting yourself: pass the
message on. It tells the user how to turn Remote Control on (in a sandbar VM,
the `claude` launcher asks once; after that, Claude Code's `/config` screen or
the settings file).
Set `CC_NEW_SKIP_REMOTE_CHECK=1` only if the user says Remote Control is
enabled some other way, such as a managed or project settings file.

Run it once with Bash, then reply with the session name from its output (what
the user looks for in the app), its permission mode, and the short id for
`claude attach` / `claude stop`. Do not wait on or drive the new session; it is
independent. If the command fails, report the error rather than retrying with
different arguments.

To see what is running, use `claude agents --json`.
