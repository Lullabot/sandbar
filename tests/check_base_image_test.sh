#!/usr/bin/env bash
# Run manually with a clean baked image and a disposable image containing a
# deliberately introduced hygiene violation.
set -euo pipefail

if (($# != 3)); then
  echo 'usage: tests/check_base_image_test.sh CLEAN_IMAGE DIRTY_IMAGE EXPECTED_FAILURE' >&2
  exit 2
fi

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
checker="$repo/scripts/check-base-image.sh"
clean=$1
dirty=$2
expected=$3

clean_output=$(sudo -n "$checker" "$clean" 2>&1) || {
  echo "clean image unexpectedly failed: $clean_output" >&2
  exit 1
}
printf '%s\n' "$clean_output"

if dirty_output=$(sudo -n "$checker" "$dirty" 2>&1); then
  echo "dirty image unexpectedly passed: $dirty_output" >&2
  exit 1
fi
if ! grep -Fq -- "$expected" <<<"$dirty_output"; then
  echo "dirty image failed for the wrong reason: $dirty_output" >&2
  exit 1
fi
printf '%s\n' "$dirty_output"
