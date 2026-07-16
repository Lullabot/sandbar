package ui

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/lullabot/sandbar/internal/lima"
)

// fakeMeminfoFiles is a lima.HostFiles whose ReadFile("/proc/meminfo") returns
// canned content (or an error simulating a host with no /proc — macOS, local
// or remote) — the fixture hostMemAvailBytes is built against. Every other
// method is delegated to the real local filesystem (embedding, exactly the
// blockingHostFiles pattern in form_async_toolset_test.go), since nothing
// else in this file touches them.
type fakeMeminfoFiles struct {
	lima.HostFiles
	data []byte
	err  error
}

func (f fakeMeminfoFiles) ReadFile(path string) ([]byte, error) {
	if path != "/proc/meminfo" {
		return f.HostFiles.ReadFile(path)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.data, nil
}

const sampleMeminfo = `MemTotal:       16384000 kB
MemFree:          500000 kB
MemAvailable:    8000000 kB
Buffers:          100000 kB
`

// hostMemAvailBytes must read MemAvailable (never MemFree — see guestSample's
// own doc on why: MemFree excludes the page cache and reads alarmingly low on
// a host that has simply been up a while), converting kB to bytes.
func TestHostMemAvailBytesParsesMemAvailable(t *testing.T) {
	hf := fakeMeminfoFiles{HostFiles: lima.LocalFiles(), data: []byte(sampleMeminfo)}
	got, ok := hostMemAvailBytes(hf)
	if !ok {
		t.Fatal("expected a reading from valid /proc/meminfo content")
	}
	if want := int64(8000000 * 1024); got != want {
		t.Fatalf("hostMemAvailBytes = %d, want %d", got, want)
	}
}

// A host with no /proc/meminfo at all (a macOS host, local or remote) must
// report NO READING — never guess from nothing.
func TestHostMemAvailBytesNoProcMeminfoIsNoReading(t *testing.T) {
	hf := fakeMeminfoFiles{HostFiles: lima.LocalFiles(), err: &fs.PathError{Op: "open", Path: "/proc/meminfo", Err: errors.New("no such file or directory")}}
	if _, ok := hostMemAvailBytes(hf); ok {
		t.Fatal("expected no reading when /proc/meminfo cannot be read")
	}
}

// A file present but missing the MemAvailable line (an unexpected /proc
// format) must also report no reading rather than a zero.
func TestHostMemAvailBytesMissingLineIsNoReading(t *testing.T) {
	hf := fakeMeminfoFiles{HostFiles: lima.LocalFiles(), data: []byte("MemTotal:  16384000 kB\nMemFree: 500000 kB\n")}
	if _, ok := hostMemAvailBytes(hf); ok {
		t.Fatal("expected no reading when MemAvailable is absent")
	}
}

// A nil HostFiles (a member with no seam at all) must also report no reading,
// never panic.
func TestHostMemAvailBytesNilHostFiles(t *testing.T) {
	if _, ok := hostMemAvailBytes(nil); ok {
		t.Fatal("expected no reading for a nil HostFiles")
	}
}

// checkHostMemWarn/checkHostDiskWarn are edge-triggered, per member per
// resource: warn ONCE on the crossing below lowFreeThreshold, stay silent
// while it remains below, and warn again only after a recovery re-crossing.
// The 5s refresh loop calls these on every successful list of a connected
// member, so without the latch the ring would fill with the same line.
func TestCheckHostMemWarnEdgeTriggered(t *testing.T) {
	m := newTestModel(t)
	mem := &m.members[0]
	const total = int64(32) << 30 // 32 GiB

	// Comfortably above the 10% threshold: nothing logged.
	mem.host.mem = total
	mem.host.memAvail = total / 2 // 50% free
	m.checkHostMemWarn(mem)
	if len(m.messages) != 0 {
		t.Fatalf("above threshold: expected no warning, got %v", m.messages)
	}

	// Cross below the threshold: exactly one warning.
	mem.host.memAvail = total * 4 / 100 // 4% free
	m.checkHostMemWarn(mem)
	if len(m.messages) != 1 {
		t.Fatalf("crossing below the threshold: expected exactly one warning, got %v", m.messages)
	}
	if !strings.Contains(m.messages[0].text, "local") || !strings.Contains(m.messages[0].text, "memory low") {
		t.Fatalf("warning text = %q, want it to name the member and say memory low", m.messages[0].text)
	}

	// STAYS below: the refresh loop calling this again must NOT spam the ring.
	m.checkHostMemWarn(mem)
	m.checkHostMemWarn(mem)
	if len(m.messages) != 1 {
		t.Fatalf("staying below the threshold: expected still exactly one warning, got %d: %v", len(m.messages), m.messages)
	}

	// Recovers to back above the threshold: the latch re-arms, silently (no NEW message for
	// the recovery itself).
	mem.host.memAvail = total / 2
	m.checkHostMemWarn(mem)
	if len(m.messages) != 1 {
		t.Fatalf("recovery itself must not log anything, got %v", m.messages)
	}

	// Re-crossing below the threshold after the recovery: warns again.
	mem.host.memAvail = total * 4 / 100
	m.checkHostMemWarn(mem)
	if len(m.messages) != 2 {
		t.Fatalf("re-crossing after a recovery: expected a second warning, got %d: %v", len(m.messages), m.messages)
	}
}

// Disk is checkHostMemWarn's exact twin.
func TestCheckHostDiskWarnEdgeTriggered(t *testing.T) {
	m := newTestModel(t)
	mem := &m.members[0]
	const total = int64(500) << 30 // 500 GiB

	mem.host.diskTotal = total
	mem.host.diskFree = total / 2
	m.checkHostDiskWarn(mem)
	if len(m.messages) != 0 {
		t.Fatalf("above threshold: expected no warning, got %v", m.messages)
	}

	mem.host.diskFree = total * 2 / 100 // 2% free
	m.checkHostDiskWarn(mem)
	if len(m.messages) != 1 {
		t.Fatalf("crossing below the threshold: expected exactly one warning, got %v", m.messages)
	}
	if !strings.Contains(m.messages[0].text, "disk low") {
		t.Fatalf("warning text = %q, want it to say disk low", m.messages[0].text)
	}

	m.checkHostDiskWarn(mem)
	if len(m.messages) != 1 {
		t.Fatalf("staying below the threshold: expected still exactly one warning, got %d", len(m.messages))
	}

	mem.host.diskFree = total // fully recovers
	m.checkHostDiskWarn(mem)
	mem.host.diskFree = total * 1 / 100
	m.checkHostDiskWarn(mem)
	if len(m.messages) != 2 {
		t.Fatalf("re-crossing after a recovery: expected a second warning, got %d", len(m.messages))
	}
}

// Absent readings (a member whose numbers have never been sampled — 0, per
// the "zero means unsampled" convention every hostSample field follows) must
// NEVER warn, from either side of the ratio missing.
func TestCheckHostWarnAbsentReadingsNeverWarn(t *testing.T) {
	m := newTestModel(t)
	mem := &m.members[0]

	mem.host.mem, mem.host.memAvail = 0, 0
	m.checkHostMemWarn(mem)
	mem.host.mem, mem.host.memAvail = 32<<30, 0
	m.checkHostMemWarn(mem)
	mem.host.mem, mem.host.memAvail = 0, 1<<20
	m.checkHostMemWarn(mem)

	mem.host.diskTotal, mem.host.diskFree = 0, 0
	m.checkHostDiskWarn(mem)
	mem.host.diskTotal, mem.host.diskFree = 500<<30, 0
	m.checkHostDiskWarn(mem)
	mem.host.diskTotal, mem.host.diskFree = 0, 1<<20
	m.checkHostDiskWarn(mem)

	if len(m.messages) != 0 {
		t.Fatalf("absent readings must never warn, got %v", m.messages)
	}
}

// Exactly the threshold (10%) free is NOT "less than 10%" — the boundary itself must not warn.
func TestCheckHostWarnExactBoundaryDoesNotWarn(t *testing.T) {
	m := newTestModel(t)
	mem := &m.members[0]

	mem.host.mem = 100
	mem.host.memAvail = 10 // exactly 10% — the lowFreeThreshold boundary
	m.checkHostMemWarn(mem)

	mem.host.diskTotal = 100
	mem.host.diskFree = 10
	m.checkHostDiskWarn(mem)

	if len(m.messages) != 0 {
		t.Fatalf("exactly 10%% free must not warn (the rule is strictly LESS than the threshold), got %v", m.messages)
	}
}
