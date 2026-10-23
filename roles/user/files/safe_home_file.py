#!/usr/bin/env python3
"""Install a sand home default only while the previous installed bytes survive.

Ansible passes the rendered candidate on stdin. The baseline is copied from the
bytes actually installed, never inferred from today's playbook or a legacy VM.
This script is installed outside the guest home before any role uses it.
"""

import argparse
import hashlib
from itertools import count
import os
from pathlib import Path, PurePosixPath
import pwd
import stat
import sys
import tempfile


def checked_path(home, relative, create=False, uid=None, gid=None):
    parts = PurePosixPath(relative).parts
    if not parts or any(part in ("", ".", "..") for part in parts) or relative.startswith("/"):
        raise ValueError("unsafe home path")
    current = home
    for part in parts[:-1]:
        current = current / part
        try:
            mode = current.lstat().st_mode
        except FileNotFoundError:
            if not create:
                return home.joinpath(*parts)
            current.mkdir(mode=0o700)
            os.chown(current, uid, gid)
            mode = current.lstat().st_mode
        if not stat.S_ISDIR(mode):
            raise ValueError("home path has a symlink or non-directory parent: " + str(current))
    return home.joinpath(*parts)


def regular_bytes(path):
    try:
        mode = path.lstat().st_mode
    except FileNotFoundError:
        return None
    if not stat.S_ISREG(mode):
        return None
    with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as file:
        return file.read()


def put(path, content, mode, uid, gid, exclusive=False):
    fd, temporary = tempfile.mkstemp(prefix=".sand-home-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as file:
            file.write(content)
            file.flush()
            os.fsync(file.fileno())
        os.chmod(temporary, mode)
        os.chown(temporary, uid, gid)
        if exclusive:
            # Publishing a proposal must never replace a file created after
            # the existence check, including a newly added symlink.
            os.link(temporary, path, follow_symlinks=False)
        else:
            os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


GIT_BLOCK_BEGIN = b"# BEGIN sandbar git-credentials"
GIT_BLOCK_END = b"# END sandbar git-credentials"


def carry_git_credentials_block(candidate, previous):
    """Carry only sand's exact include block from a tracked, unchanged file."""
    lines = previous.splitlines(keepends=True)
    starts = [index for index, line in enumerate(lines) if line.rstrip(b"\r\n") == GIT_BLOCK_BEGIN]
    ends = [index for index, line in enumerate(lines) if line.rstrip(b"\r\n") == GIT_BLOCK_END]
    if not starts and not ends:
        return candidate
    if len(starts) != 1 or len(ends) != 1 or starts[0] >= ends[0] or GIT_BLOCK_BEGIN in candidate:
        return None
    block = b"".join(lines[starts[0]:ends[0] + 1]).rstrip(b"\r\n") + b"\n"
    return candidate.rstrip(b"\r\n") + b"\n\n" + block


def install(home, relative, user, mode, candidate, preserved_home, fresh_clone=False, record_current=False, check_managed=False, propose_only=False, preserve_git_credentials_block=False):
    if not home.is_dir() or home.is_symlink():
        raise ValueError("home must be a real directory")
    account = pwd.getpwnam(user)
    uid, gid = account.pw_uid, account.pw_gid
    destination = checked_path(home, relative)
    baseline_rel = ".config/sandbar/home-baselines/" + relative
    proposed_rel = ".config/sandbar/proposed-defaults/" + relative
    baseline = checked_path(home, baseline_rel)
    old = regular_bytes(destination)
    installed = regular_bytes(baseline)
    destination_exists = os.path.lexists(destination)
    baseline_exists = os.path.lexists(baseline)

    if check_managed:
        if (old is not None and installed is not None and old == installed) or (
            not preserved_home and not baseline_exists and
            (not destination_exists or (fresh_clone and old is not None))
        ):
            print("managed")
            return
        print("user-owned")
        sys.exit(3)

    # Finalize may append sand's own credential and launcher blocks after the
    # initial file install. Record their final bytes only on a fresh clone;
    # callers must never use this mode for restored user-owned files.
    if record_current:
        if old is None:
            print("unchanged: no regular file")
            return
        if baseline_exists and installed is None:
            raise ValueError("baseline is not a regular file: " + str(baseline))
        baseline = checked_path(home, baseline_rel, create=True, uid=uid, gid=gid)
        if installed != old:
            put(baseline, old, 0o600, uid, gid)
            print("changed: recorded baseline")
        else:
            print("unchanged: baseline current")
        return

    # A fresh clone may inherit defaults from an old base with no baselines.
    # It has no user edits yet; preserved homes and agent state are explicitly
    # marked by the caller and must never take this branch.
    fresh = not preserved_home and not baseline_exists and (not destination_exists or (fresh_clone and old is not None))
    replaceable = destination_exists and not destination.is_symlink() and old is not None and installed is not None and old == installed
    if preserve_git_credentials_block and replaceable:
        merged = carry_git_credentials_block(candidate, old)
        if merged is None:
            replaceable = False
        else:
            candidate = merged
    elif preserve_git_credentials_block and fresh and old is not None and (GIT_BLOCK_BEGIN in old or GIT_BLOCK_END in old):
        fresh = False
    if not propose_only and (fresh or replaceable):
        destination = checked_path(home, relative, create=True, uid=uid, gid=gid)
        baseline = checked_path(home, baseline_rel, create=True, uid=uid, gid=gid)
        changed = False
        if old != candidate:
            put(destination, candidate, mode, uid, gid)
            changed = True
        if installed != candidate:
            put(baseline, candidate, 0o600, uid, gid)
            changed = True
        print("changed: installed default" if changed else "unchanged: installed default current")
        return

    suffix = hashlib.sha256(candidate).hexdigest()[:12]
    for index in count(-1):
        rel = proposed_rel if index < 0 else proposed_rel + "." + suffix + ("." + str(index) if index else "")
        proposed = checked_path(home, rel, create=True, uid=uid, gid=gid)
        existing = regular_bytes(proposed)
        if existing == candidate:
            print("unchanged: proposed default at " + str(proposed))
            return
        if os.path.lexists(proposed):
            if index < 0 and existing is None:
                raise ValueError("proposed default has a non-regular destination: " + str(proposed))
            continue
        try:
            put(proposed, candidate, mode, uid, gid, exclusive=True)
        except FileExistsError:
            continue
        print("changed: proposed default at " + str(proposed))
        return


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("home", type=Path)
    parser.add_argument("relative")
    parser.add_argument("user")
    parser.add_argument("mode", type=lambda value: int(value, 8))
    parser.add_argument("--preserved-home", action="store_true")
    parser.add_argument("--fresh-clone", action="store_true")
    parser.add_argument("--record-current", action="store_true")
    parser.add_argument("--check-managed", action="store_true")
    parser.add_argument("--propose-only", action="store_true")
    parser.add_argument("--preserve-git-credentials-block", action="store_true")
    args = parser.parse_args()
    try:
        install(args.home, args.relative, args.user, args.mode, sys.stdin.buffer.read(), args.preserved_home, args.fresh_clone, args.record_current, args.check_managed, args.propose_only, args.preserve_git_credentials_block)
    except (OSError, ValueError, KeyError) as error:
        parser.exit(1, "safe home install: " + str(error) + "\n")


if __name__ == "__main__":
    main()
