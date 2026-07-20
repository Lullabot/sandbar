# sand repo-profile e2e fixture

This directory is a checked-in fixture repository (strikethroo plan 18, task
05). It is turned into a bare git repository and served locally by
`git daemon` at test time — see `cmd/sand/repoprofile_e2e_test.go` — and
exercised end to end via `sand create --clone-url <served-url>`.

Its committed `.sandbar/profile.yml` declares one item from every manifest
group so the finalize stage's repo-profile role (`roles/repo-profile/`) has
something concrete to apply and the e2e test has something concrete to
assert on inside the guest:

| Group      | Declared                          | Asserted in the guest                                   |
|------------|------------------------------------|-----------------------------------------------------------|
| `packages` | `cowsay`                           | `dpkg -l cowsay` shows it installed                        |
| `services` | `sand-fixture-marker.service`      | `systemctl is-enabled` reports `enabled`                    |
| `roles`    | `marker-role`                      | `/etc/sand-fixture-role-marker` exists                     |
| `toolset`  | `go`                                | `go version` succeeds in the clone                          |
| `seed`     | `.sandbar/seed.yml`                | `seed-marker.txt` exists in the cloned project tree          |

Do not add real-world dependencies here — everything this fixture declares
must be safe, idempotent, and cheap to apply on every e2e run.
