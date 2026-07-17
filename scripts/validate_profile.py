#!/usr/bin/env python3
"""Validate a `.sandbar/` provisioning-profile manifest.

This is the fast, per-PR guard against the most common authoring mistake in a
repo-checked-in provisioning profile: a typo'd key or a malformed value. It is
a standalone script with no dependency beyond the Python 3 standard library
(the guest already requires Python 3 for Ansible's own module execution, and
this script must not require anything installed beyond what's already
provisioned) so it runs identically here in CI, against static fixtures on a
runner with no VM, and in production inside the guest, against a cloned
checkout's `.sandbar/` manifest.

MANIFEST SCHEMA
================

The manifest declares exactly five top-level groups. No other top-level key is
permitted, there is no per-OS/conditional logic, and there is no profile
inheritance — see the plan's "Profile Schema and Guest-Side Validation"
section and its Notes/scope guard.

  packages:  list of apt package names to install in the clone.
             Each name must be a syntactically valid Debian package name:
             lowercase letters, digits, '+', '-', '.', starting with an
             alphanumeric character, at least two characters long.

  services:  list of systemd unit names to enable/start in the clone.
             Each name must look like a valid systemd unit: unit-name
             characters (letters, digits, ':', '_', '.', '-', an optional
             "@instance" for templated units) followed by a recognized unit
             suffix (.service, .socket, .device, .mount, .automount, .swap,
             .target, .path, .timer, .slice, .scope).

  roles:     list of role names expected to exist under
             `.sandbar/roles/<name>/` in the cloned checkout. Each name must
             be a bare identifier (letters, digits, '_', '-' only) — no
             slashes or '.' segments, so a role name can never escape
             `.sandbar/roles/` via path traversal.

  seed:      a single relative path (string, not a list) to a repo-supplied
             Ansible tasks file, conventionally under `.sandbar/`, run last.
             The path must be relative (no leading '/'), must not contain a
             '..' path segment, and must end in `.yml` or `.yaml`.

  toolset:   list of shipped provisioning-profile names to reconcile into the
             clone (e.g. "claude", "ddev", "go", "java"). Each name must be
             one of the names in KNOWN_TOOLSETS below.

Every group is optional; an omitted group means "declare nothing for that
group". An empty list is fine and validates. Unknown top-level keys are
always a hard error naming the offending key.

MANIFEST FILE FORMAT
=====================

The manifest is YAML-*like*, but this validator (and the finalize-stage
Ansible that later reads the same file) only ever needs a tiny, unambiguous
subset of YAML's block style, so rather than take on a PyYAML dependency this
script implements that subset directly:

  - Blank lines and full-line '#' comments are ignored.
  - A top-level key starts at column 0: either
      key:
        - item
        - item
    (a block sequence of scalar strings), or
      key: value
    (a single scalar value, used for `seed`).
  - Scalars may be bare or wrapped in matching single/double quotes.
  - Nested mappings, flow style (`[a, b]`, `{a: b}`), multi-line scalars,
    and anchors/aliases are not supported and are a syntax error.

Author-facing manifests are simple enough (five flat groups, each a list of
short strings or a single path) that this restricted grammar covers every
legitimate manifest while keeping the validator dependency-free.

INVOCATION CONTRACT
====================

    python3 validate_profile.py <path-to-manifest>

Exit codes:
  0  the manifest is well-formed and satisfies the schema.
  1  the manifest is present and readable but fails validation (syntax error
     or schema violation). Every failure reason is printed to stderr, one
     per line, each naming the specific offending top-level key (and, where
     applicable, the specific offending item) — never a generic parse error.
  2  usage error: wrong number of arguments, or the manifest path could not
     be opened/read.

This is the validator's entire production invocation surface: in the guest,
the finalize stage runs it against the cloned checkout's `.sandbar/` manifest
before applying anything it declares. There is no host-side invocation path —
the host never reads, templates, or executes profile content.
"""

import re
import sys

# KNOWN_TOOLSETS is the list of shipped provisioning-profile names a `toolset`
# entry may reference. Keep this in sync with whatever the shipped-profiles
# task (see the plan's "Shipped Provisioning Profiles" component) calls its
# profiles — it is deliberately a single, trivially-editable constant so that
# work can extend it without touching any other validator logic.
KNOWN_TOOLSETS = ("claude", "ddev", "go", "java")

# ALLOWED_KEYS are the only five top-level manifest groups. Nothing else is a
# valid key, ever — see the module docstring's schema section.
ALLOWED_KEYS = ("packages", "services", "roles", "seed", "toolset")

# LIST_KEYS are the groups whose value must be a YAML block sequence of
# scalar strings. `seed` is the one group that takes a single scalar instead.
LIST_KEYS = ("packages", "services", "roles", "toolset")

# Debian package names: lowercase letters, digits, '+', '-', '.', starting
# with an alphanumeric, at least two characters (Debian Policy Manual §5.6.7).
PACKAGE_NAME_RE = re.compile(r"^[a-z0-9][a-z0-9+.-]+$")

# systemd unit names: NAME[@INSTANCE].SUFFIX, where NAME/INSTANCE may contain
# letters, digits, ':', '_', '.', '-' and SUFFIX is one of the unit types
# systemd recognizes.
UNIT_SUFFIXES = (
    "service",
    "socket",
    "device",
    "mount",
    "automount",
    "swap",
    "target",
    "path",
    "timer",
    "slice",
    "scope",
)
SERVICE_NAME_RE = re.compile(
    r"^[A-Za-z0-9:_.-]+(@[A-Za-z0-9:_.-]*)?\.(" + "|".join(UNIT_SUFFIXES) + r")$"
)

# Role names are bare identifiers: no '/' or '.' segments, so a declared role
# can never resolve outside `.sandbar/roles/<name>/`.
ROLE_NAME_RE = re.compile(r"^[A-Za-z0-9_-]+$")


class ManifestSyntaxError(Exception):
    """Raised when the manifest does not parse as the restricted YAML subset
    this validator supports. Always carries a message naming the offending
    line/content rather than a bare "parse error"."""


def _unquote(value):
    """Strip one layer of matching single/double quotes from a scalar, if
    present. Bare (unquoted) scalars are returned unchanged."""
    if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
        return value[1:-1]
    return value


def parse_manifest(text):
    """Parse the restricted YAML-subset grammar documented in the module
    docstring into a dict of key -> (list[str] | str). Raises
    ManifestSyntaxError on anything outside that subset."""
    data = {}
    current_key = None

    for lineno, raw in enumerate(text.splitlines(), start=1):
        stripped = raw.strip()
        if not stripped or stripped.startswith("#"):
            continue

        if raw[:1] not in (" ", "\t") and not raw.startswith("-"):
            # Top-level key line: "key:" or "key: value".
            if ":" not in raw:
                raise ManifestSyntaxError(
                    f"line {lineno}: expected 'key:' or 'key: value', got: {raw!r}"
                )
            key, _, rest = raw.partition(":")
            key = key.strip()
            rest = rest.strip()
            if not key:
                raise ManifestSyntaxError(f"line {lineno}: empty key before ':'")
            if key in data:
                raise ManifestSyntaxError(
                    f"line {lineno}: duplicate top-level key '{key}'"
                )
            if rest:
                data[key] = _unquote(rest)
                current_key = None
            else:
                data[key] = []
                current_key = key
            continue

        if stripped.startswith("-"):
            if current_key is None or not isinstance(data.get(current_key), list):
                raise ManifestSyntaxError(
                    f"line {lineno}: list item outside of a list-valued key: {raw!r}"
                )
            item = stripped[1:].strip()
            if not item:
                raise ManifestSyntaxError(f"line {lineno}: empty list item")
            data[current_key].append(_unquote(item))
            continue

        raise ManifestSyntaxError(
            f"line {lineno}: unexpected indentation or content: {raw!r}"
        )

    return data


def _seed_errors(path):
    """Return a list of human-readable error strings for a malformed `seed`
    path, or an empty list if it is well-formed. `path` is already known to
    be a non-empty string by the caller."""
    errors = []
    if path.startswith("/"):
        errors.append(f"'seed': path '{path}' must be relative, not absolute")
    if ".." in path.split("/"):
        errors.append(f"'seed': path '{path}' must not contain a '..' path segment")
    if not (path.endswith(".yml") or path.endswith(".yaml")):
        errors.append(
            f"'seed': path '{path}' must end in .yml or .yaml (an Ansible tasks file)"
        )
    return errors


def validate(data):
    """Validate a parsed manifest dict against the schema. Returns a list of
    human-readable error strings (empty if the manifest is valid), each
    naming the specific offending top-level key and, where applicable, the
    specific offending item."""
    errors = []

    for key in sorted(set(data) - set(ALLOWED_KEYS)):
        errors.append(
            f"unknown top-level key '{key}' (allowed: {', '.join(ALLOWED_KEYS)})"
        )

    for key in LIST_KEYS:
        if key not in data:
            continue
        value = data[key]
        if not isinstance(value, list):
            errors.append(f"'{key}' must be a list of items, not a single value")
            continue
        for item in value:
            errors.extend(_item_errors(key, item))

    if "seed" in data:
        value = data["seed"]
        if not isinstance(value, str) or not value.strip():
            errors.append("'seed' must be a single non-empty path")
        else:
            errors.extend(_seed_errors(value))

    return errors


def _item_errors(key, item):
    """Return error strings for one malformed item within a list-valued
    group, or an empty list if the item is well-formed."""
    if key == "packages":
        if not PACKAGE_NAME_RE.match(item):
            return [f"'packages': '{item}' is not a valid Debian package name"]
    elif key == "services":
        if not SERVICE_NAME_RE.match(item):
            return [f"'services': '{item}' is not a valid systemd unit name"]
    elif key == "roles":
        if not ROLE_NAME_RE.match(item):
            return [
                f"'roles': '{item}' is not a valid role name "
                "(letters, digits, '-', '_' only)"
            ]
    elif key == "toolset":
        if item not in KNOWN_TOOLSETS:
            return [
                f"'toolset': '{item}' is not a known toolset "
                f"(known: {', '.join(KNOWN_TOOLSETS)})"
            ]
    return []


def main(argv):
    if len(argv) != 2:
        print(f"usage: {argv[0]} <path-to-manifest>", file=sys.stderr)
        return 2

    path = argv[1]
    try:
        with open(path, "r", encoding="utf-8") as f:
            text = f.read()
    except OSError as exc:
        print(f"error: cannot read manifest '{path}': {exc}", file=sys.stderr)
        return 2

    try:
        data = parse_manifest(text)
    except ManifestSyntaxError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1

    errors = validate(data)
    if errors:
        for message in errors:
            print(f"error: {message}", file=sys.stderr)
        return 1

    print(f"OK: {path} is a valid .sandbar/ manifest")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
