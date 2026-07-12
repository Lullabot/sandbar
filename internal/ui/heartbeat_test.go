package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// The fixture is REAL text: the output of the REAL guestScript, run over a real
// `limactl shell` against a live Lima guest (Debian 13, 2 vCPU, 2GiB) with one
// `yes > /dev/null` burner pegging exactly one of its two cores. Three records, two
// seconds apart, captured off the wire. The numbers below are not invented; they are
// what that guest reported.
//
//	record 1: cpu  1681 0 2734 155860 277 0 98 42 0 0
//	record 2: cpu  1809 0 2806 156059 277 0 98 42 0 0
//	record 3: cpu  1939 0 2879 156257 277 0 98 42 0 0
//
// Between records 1 and 2: Δtotal = 399 jiffies, Δ(idle+iowait) = 199, so Δbusy =
// 200 and the guest was 200/399 = 50.1% busy — one of two cores, which is precisely
// what the burner was doing. That the arithmetic lands on the answer the experiment
// was rigged to produce is the point of using real text: a hand-written fixture
// agrees with whatever parser wrote it.
const (
	fixtureCPUPct2 = 200.0 / 399 * 100 // 50.125…%
	fixtureCPUPct3 = 203.0 / 401 * 100 // 50.623…%

	fixtureMemTotal = 2015488 * 1024 // MemTotal: 2015488 kB
	// used = MemTotal - MemAvailable. NOT MemFree: the same guest, in the same
	// records, reported MemFree: 1133248 kB against MemAvailable: 1656984 kB — a
	// 512 MB gap that is nothing but page cache. Compute "used" from MemFree and an
	// idle VM's tile reports 862 MiB in use instead of 350 MiB, and climbs toward
	// full as the guest merely reads files.
	fixtureMemUsed1 = (2015488 - 1656984) * 1024
	fixtureMemUsed2 = (2015488 - 1657028) * 1024
)

func readFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "guest_stream.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// close reports whether two percentages agree to within a hundredth of a point.
func close2(a, b float64) bool { return a-b < 0.01 && b-a < 0.01 }

// The parser, against real captured /proc text: three records in, three samples
// out, with the cpu percentage coming from the DELTA between consecutive readings
// and the FIRST sample carrying no percentage at all.
func TestParseRealGuestStream(t *testing.T) {
	var p sampleParser
	got := p.feed(readFixture(t))

	if len(got) != 3 {
		t.Fatalf("got %d samples from a 3-record stream, want 3", len(got))
	}

	// THE FIRST SAMPLE HAS NO CPU PERCENTAGE. /proc/stat is cumulative since boot,
	// so one reading is a total, not a rate. Reporting 0% here would be a lie the
	// user cannot distinguish from a genuinely idle VM.
	if got[0].HasCPU {
		t.Fatalf("the first sample must carry NO cpu reading (a single /proc/stat is cumulative, not a rate), got %.2f%%", got[0].CPUPct)
	}
	// Memory, by contrast, is absolute: it is valid on the very first sample.
	if !got[0].HasMem() {
		t.Fatal("memory is an absolute reading and must be present on the first sample")
	}
	if got[0].MemTotal != fixtureMemTotal || got[0].MemUsed != fixtureMemUsed1 {
		t.Fatalf("sample 1 mem = %d/%d, want %d/%d (used = MemTotal - MemAvailable)",
			got[0].MemUsed, got[0].MemTotal, fixtureMemUsed1, fixtureMemTotal)
	}

	// The second and third samples have a predecessor, so they have a rate.
	if !got[1].HasCPU || !close2(got[1].CPUPct, fixtureCPUPct2) {
		t.Fatalf("sample 2 cpu = %.3f%% (has=%v), want %.3f%% — Δbusy/Δtotal across the two readings",
			got[1].CPUPct, got[1].HasCPU, fixtureCPUPct2)
	}
	if !got[2].HasCPU || !close2(got[2].CPUPct, fixtureCPUPct3) {
		t.Fatalf("sample 3 cpu = %.3f%%, want %.3f%%", got[2].CPUPct, fixtureCPUPct3)
	}
	if got[1].MemUsed != fixtureMemUsed2 {
		t.Fatalf("sample 2 mem used = %d, want %d", got[1].MemUsed, fixtureMemUsed2)
	}

	// MemFree is 1133248 kB in this fixture. If anything ever swaps MemAvailable for
	// it, used jumps by half a gigabyte of page cache and the tile cries wolf. Pin
	// the distinction so no one can "simplify" one into the other.
	memFreeUsed := uint64((2015488 - 1133248) * 1024)
	if got[0].MemUsed == memFreeUsed {
		t.Fatal("used was computed from MemFree — it must come from MemAvailable, or an idle VM's page cache reads as near-OOM")
	}
}

// THE STREAM ARRIVES IN ARBITRARY CHUNKS. This is not a theoretical worry: reading
// a real `limactl shell` gave 2642-byte chunks that sometimes split as 2631 + 11,
// tearing a record — and once, a line — across two reads. A parser that assumed a
// chunk was a whole record would drop samples in production and never in a test
// that fed it whole records.
func TestParserSurvivesEveryBufferBoundary(t *testing.T) {
	raw := readFixture(t)

	var whole sampleParser
	want := whole.feed(raw)

	for _, chunk := range []int{1, 2, 7, 11, 64, 512, 2631, 2642, 4096, len(raw)} {
		t.Run(chunkName(chunk), func(t *testing.T) {
			var p sampleParser
			var got []guestSample
			for i := 0; i < len(raw); i += chunk {
				end := min(i+chunk, len(raw))
				got = append(got, p.feed(raw[i:end])...)
			}
			if len(got) != len(want) {
				t.Fatalf("%d-byte chunks produced %d samples, want %d — a record was lost across a read boundary", chunk, len(got), len(want))
			}
			for i := range want {
				if got[i].HasCPU != want[i].HasCPU || !close2(got[i].CPUPct, want[i].CPUPct) ||
					got[i].MemUsed != want[i].MemUsed || got[i].MemTotal != want[i].MemTotal {
					t.Fatalf("%d-byte chunks: sample %d = %+v, want %+v", chunk, i, got[i], want[i])
				}
			}
		})
	}
}

func chunkName(n int) string { return "chunk_" + strconv.Itoa(n) }

// The parser must ignore anything it does not recognise rather than choke on it.
// `limactl shell` runs the command through a LOGIN shell ($SHELL -l -c …), so a
// motd, a profile's banner, or a stray warning can land in the stream; none of it
// may corrupt a sample or fabricate one.
func TestParserIgnoresNoise(t *testing.T) {
	stream := "Welcome to Debian GNU/Linux 13!\n" +
		"bash: cd: /nonexistent: No such file or directory\n" +
		"cpu  100 0 100 800 0 0 0 0 0 0\n" +
		"cpu0 50 0 50 400 0 0 0 0 0 0\n" + // per-core lines are NOT the aggregate
		"intr 1234 5 6\n" +
		"MemTotal:        1000 kB\n" +
		"MemFree:          100 kB\n" +
		"MemAvailable:     600 kB\n" +
		"SwapTotal:          0 kB\n" +
		heartbeatDelim + "\n" +
		"cpu  200 0 100 900 0 0 0 0 0 0\n" +
		"MemTotal:        1000 kB\n" +
		"MemAvailable:     600 kB\n" +
		heartbeatDelim + "\n"

	var p sampleParser
	got := p.feed([]byte(stream))
	if len(got) != 2 {
		t.Fatalf("got %d samples, want 2 — noise must be ignored, not counted", len(got))
	}
	// Aggregate deltas only: total 1000 -> 1200 (Δ200), idle 800 -> 900 (Δ100), so
	// busy = 100/200 = 50%. If the per-core `cpu0` line were mistaken for the
	// aggregate, this comes out wrong.
	if !close2(got[1].CPUPct, 50) {
		t.Fatalf("cpu = %.2f%%, want 50%% — only the aggregate `cpu ` line may feed the delta", got[1].CPUPct)
	}
	if got[1].MemUsed != 400*1024 || got[1].MemTotal != 1000*1024 {
		t.Fatalf("mem = %d/%d, want %d/%d", got[1].MemUsed, got[1].MemTotal, 400*1024, 1000*1024)
	}
}

// A counter that goes BACKWARDS means the guest rebooted under the stream. Signed
// arithmetic would give a negative percentage; unsigned would wrap to something
// astronomical. Either way the tile lies. It must simply report no reading and
// re-baseline.
func TestParserRebaselinesOnACounterReset(t *testing.T) {
	rec := func(user, idle int) string {
		return "cpu  " + strconv.Itoa(user) + " 0 0 " + strconv.Itoa(idle) + " 0 0 0 0 0 0\n" +
			"MemTotal:        1000 kB\nMemAvailable:     600 kB\n" + heartbeatDelim + "\n"
	}
	var p sampleParser
	got := p.feed([]byte(rec(1000, 9000) + rec(1100, 9900) + rec(5, 40) + rec(105, 940)))
	if len(got) != 4 {
		t.Fatalf("got %d samples, want 4", len(got))
	}
	if !got[1].HasCPU {
		t.Fatal("sample 2 should have a reading")
	}
	if got[2].HasCPU {
		t.Fatalf("a backwards counter (the guest rebooted) must yield NO reading, got %.2f%%", got[2].CPUPct)
	}
	if !got[3].HasCPU {
		t.Fatal("the parser must re-baseline after a reset, so the NEXT sample reads again")
	}
	if !close2(got[3].CPUPct, 10) { // Δbusy 100, Δtotal 1000
		t.Fatalf("post-reset cpu = %.2f%%, want 10%%", got[3].CPUPct)
	}
}
