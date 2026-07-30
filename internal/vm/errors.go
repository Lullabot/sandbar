package vm

import "errors"

// ErrNotFound reports that the backend knows no VM by that name. It is the ONE
// absence sentinel every backend reports and every caller tests for.
//
// It lives here, in the domain-model package, rather than in whichever backend
// happened to define it first, because "this VM does not exist" is a fact about
// the fleet and not about a transport: `sand create`'s exists-guard, `sand
// shell`, the Landing pane, and the TUI all branch on it without knowing (or
// wanting to know) whether the name was missing from `limactl list`, from a
// Proxmox pool, or from anything added later. A backend that invented its own
// sentinel would silently fall through every one of those branches to the
// generic error path — which is why an implementation must wrap THIS error
// (`fmt.Errorf("%w: %s", vm.ErrNotFound, name)`) rather than report its own.
var ErrNotFound = errors.New("no such VM")
