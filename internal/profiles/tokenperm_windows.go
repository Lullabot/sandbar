//go:build windows

package profiles

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no POSIX mode bits: os.Stat synthesizes 0666 for every readable
// file (0444 when the read-only attribute is set), so the unix `mode&0o077`
// test would refuse every token file on Windows regardless of how well
// protected it actually is. The question that check is really asking -- "can
// anyone but me read this?" -- has to be asked of the file's DACL instead.
//
// The rule enforced here mirrors the unix one as closely as Windows allows:
// every access-allowed ACE that grants read must name the file's OWNER,
// LocalSystem, or the built-in Administrators group. Those last two can read
// anything on the machine by other means (SeBackupPrivilege, taking
// ownership), so refusing them would fail every file on a normal Windows
// install without buying any real protection -- the same reason the unix
// check ignores root.
//
// This matters beyond ticking a box: %USERPROFILE% carries a restrictive
// default ACL, but nothing stops a profile pointing token_file at C:\temp,
// which is world-readable.

// Read-ish bits. An ACE granting any of these lets the grantee read the
// token, so any one of them on a foreign SID is disqualifying.
const (
	fileReadData = 0x0001

	readishMask = fileReadData |
		windows.GENERIC_READ |
		windows.GENERIC_ALL |
		windows.FILE_GENERIC_READ |
		windows.STANDARD_RIGHTS_ALL
)

func checkTokenPerms(path string) error {
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("proxmox token file %s: reading its security descriptor: %w", path, err)
	}

	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("proxmox token file %s: reading its owner: %w", path, err)
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("proxmox token file %s: reading its DACL: %w", path, err)
	}
	// A NULL DACL is not "no permissions", it is "everyone gets everything" --
	// the worst case, and easy to misread as the safest.
	if dacl == nil {
		return fmt.Errorf("proxmox token file %s has a NULL DACL, which grants every user full access; restrict it to your own account", path)
	}

	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("proxmox token file %s: resolving the SYSTEM SID: %w", path, err)
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("proxmox token file %s: resolving the Administrators SID: %w", path, err)
	}

	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("proxmox token file %s: reading ACE %d: %w", path, i, err)
		}
		// Deny ACEs cannot widen access, so only allow-ACEs are interesting.
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		if uint32(ace.Mask)&readishMask == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(uintptr(unsafe.Pointer(ace)) + unsafe.Offsetof(ace.SidStart)))
		if sid.Equals(owner) || sid.Equals(system) || sid.Equals(admins) {
			continue
		}
		return fmt.Errorf("proxmox token file %s grants read access to %s; it must be readable only by your own account", path, sidLabel(sid))
	}
	return nil
}

// sidLabel renders a SID for the error message, preferring the account name a
// human will recognize and falling back to the raw S-1-... form when the SID
// cannot be resolved (a deleted account, or a domain that is not reachable).
func sidLabel(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	if err != nil {
		return sid.String()
	}
	if domain == "" {
		return account
	}
	return domain + `\` + account
}
