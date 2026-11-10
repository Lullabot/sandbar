//go:build darwin

package keepawake

import (
	"fmt"
	"sync"

	"github.com/ebitengine/purego"
)

// Framework paths for dlopen. Since Big Sur the binaries live only in the dyld
// shared cache, not on disk, but dlopen resolves these paths against the cache.
const (
	iokitPath          = "/System/Library/Frameworks/IOKit.framework/IOKit"
	coreFoundationPath = "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation"
)

// IOKit / CoreFoundation constants, from IOPMLib.h and CFString.h.
const (
	// kIOPMAssertionTypePreventUserIdleSystemSleep: block the idle timer from
	// sleeping the system, while leaving the display free to sleep and the
	// screen free to lock. (PreventUserIdleDisplaySleep would also keep the
	// screen on, which is not wanted.)
	assertionTypePreventUserIdleSystemSleep = "PreventUserIdleSystemSleep"
	// kIOPMAssertionLevelOn (IOPMAssertionLevel is a uint32_t).
	assertionLevelOn uint32 = 255
	// kCFStringEncodingUTF8.
	cfStringEncodingUTF8 uint32 = 0x08000100
	// kIOReturnSuccess.
	ioReturnSuccess int32 = 0
)

// The C functions, bound once by loadIOKit. Argument types follow purego's
// mapping: CFTypeRef / CFAllocatorRef are pointers (uintptr), a Go string is
// passed as a NUL-terminated char* copy valid for the call, IOPMAssertionID is
// a uint32_t, and IOReturn (kern_return_t) is an int.
var (
	// CFStringRef CFStringCreateWithCString(CFAllocatorRef alloc, const char *cStr, CFStringEncoding encoding);
	cfStringCreateWithCString func(alloc uintptr, cStr string, encoding uint32) uintptr
	// void CFRelease(CFTypeRef cf);
	cfRelease func(cf uintptr)
	// IOReturn IOPMAssertionCreateWithName(CFStringRef type, IOPMAssertionLevel level, CFStringRef name, IOPMAssertionID *id);
	ioPMAssertionCreateWithName func(assertionType uintptr, level uint32, name uintptr, id *uint32) int32
	// IOReturn IOPMAssertionRelease(IOPMAssertionID id);
	ioPMAssertionRelease func(id uint32) int32

	loadOnce sync.Once
	loadErr  error
)

// loadIOKit opens both frameworks and binds the four functions, on first use
// only so a sandbar that never runs a long operation never loads IOKit. It
// resolves symbols with Dlsym rather than purego.RegisterLibFunc because the
// latter panics on a missing symbol, and a keep-awake failure must never take
// sandbar down.
func loadIOKit() error {
	loadOnce.Do(func() {
		cf, err := purego.Dlopen(coreFoundationPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("keepawake: load CoreFoundation: %w", err)
			return
		}
		iokit, err := purego.Dlopen(iokitPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			loadErr = fmt.Errorf("keepawake: load IOKit: %w", err)
			return
		}
		for _, b := range []struct {
			fptr any
			lib  uintptr
			name string
		}{
			{&cfStringCreateWithCString, cf, "CFStringCreateWithCString"},
			{&cfRelease, cf, "CFRelease"},
			{&ioPMAssertionCreateWithName, iokit, "IOPMAssertionCreateWithName"},
			{&ioPMAssertionRelease, iokit, "IOPMAssertionRelease"},
		} {
			sym, err := purego.Dlsym(b.lib, b.name)
			if err != nil {
				loadErr = fmt.Errorf("keepawake: resolve %s: %w", b.name, err)
				return
			}
			purego.RegisterFunc(b.fptr, sym)
		}
	})
	return loadErr
}

// acquireOS creates an IOKit power assertion. The assertion belongs to this
// process — the kernel drops it if sandbar exits or crashes — so, unlike the
// Windows backend, no particular thread has to stay alive to hold it.
func acquireOS(reason string) (func() error, error) {
	if err := loadIOKit(); err != nil {
		return nil, err
	}
	// A NULL allocator means kCFAllocatorDefault.
	typ := cfStringCreateWithCString(0, assertionTypePreventUserIdleSystemSleep, cfStringEncodingUTF8)
	if typ == 0 {
		return nil, fmt.Errorf("keepawake: CFStringCreateWithCString failed for assertion type")
	}
	defer cfRelease(typ)
	name := cfStringCreateWithCString(0, reason, cfStringEncodingUTF8)
	if name == 0 {
		// Invalid UTF-8 in reason. The name is cosmetic; the assertion is
		// not, so fall back to a fixed label rather than give up.
		name = cfStringCreateWithCString(0, "sandbar", cfStringEncodingUTF8)
		if name == 0 {
			return nil, fmt.Errorf("keepawake: CFStringCreateWithCString failed for assertion name")
		}
	}
	// IOPMAssertionCreateWithName retains what it needs from both strings,
	// so ours are released as soon as it returns.
	defer cfRelease(name)

	var id uint32
	if rc := ioPMAssertionCreateWithName(typ, assertionLevelOn, name, &id); rc != ioReturnSuccess {
		return nil, fmt.Errorf("keepawake: IOPMAssertionCreateWithName returned 0x%08x", uint32(rc))
	}
	return func() error {
		if rc := ioPMAssertionRelease(id); rc != ioReturnSuccess {
			return fmt.Errorf("keepawake: IOPMAssertionRelease returned 0x%08x", uint32(rc))
		}
		return nil
	}, nil
}
