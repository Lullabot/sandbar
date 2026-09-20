// compress.go decides how a reset's staging archives are compressed, and how
// they are read back.
//
// The choice matters more than it looks. Compression runs INSIDE the guest, on
// the critical path of a reset, over data the user asked to keep — routinely a
// whole home directory of several gigabytes. gzip is single-threaded and slow
// enough to dominate the whole operation: on a 1.3 GB source tree it took 54
// seconds where `zstd -T0 -3` took 2.5 and produced a slightly SMALLER archive.
// That is not a micro-optimisation; it is the difference between a reset that
// feels like a rebuild and one that feels like a stall.
//
// zstd is not assumed, it is probed. The archive is created on the SOURCE VM,
// which may have been cloned from a base built before zstd was part of the
// image, and a reset that fails because the compressor is missing would fail
// while holding the only copy of the user's work. So a guest without zstd falls
// back to gzip and the reset proceeds, slowly, exactly as it always did.
package provision

import (
	"bytes"
	"context"
	"io"
)

// zstdCompressProgram is what tar is handed as its compressor on stage-out.
//
// -T0 uses every core the guest has, which is the whole reason a multi-gigabyte
// home stops being a coffee break; a 1-vCPU guest simply gets one worker. -3 is
// zstd's own default level, named here rather than left implicit so the trade
// being made is visible: at this level zstd already matches gzip's ratio on real
// source trees, so there is nothing to buy by compressing harder and a great
// deal to lose in time.
const zstdCompressProgram = "zstd -T0 -3"

// zstdMagic and gzipMagic are the leading bytes of the two formats
// tarDecompressFlag has to tell apart (RFC 1952 §2.3.1 for gzip; the zstd frame
// magic from RFC 8878 §3.1.1, little-endian on disk).
var (
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
	gzipMagic = []byte{0x1f, 0x8b}
)

// tarCompressFlags returns the tar flags that compress a stage-out archive in
// the named guest: zstd when the guest has it, gzip otherwise.
//
// The probe runs under sudo because the tar it is deciding for does too — what
// matters is whether `zstd` resolves on root's PATH, not on the calling user's.
// A probe that could not run at all is treated as "no zstd": the fallback is
// correct for the only cost of being wrong (a slower archive), and a transport
// that is genuinely broken will fail the tar a moment later with an error about
// the transport rather than one about a compressor.
func tarCompressFlags(ctx context.Context, cli guestRunner, name string) []string {
	if _, err := cli.ShellOut(ctx, name, "sudo", "sh", "-c", "command -v zstd"); err == nil {
		return []string{"-I", zstdCompressProgram}
	}
	return []string{"-z"}
}

// tarDecompressFlag returns the tar flag that reads the archive in r, chosen by
// sniffing its magic bytes, or "" when it matches neither format.
//
// The format is read off the ARCHIVE rather than remembered from the stage-out
// that wrote it, because the two halves of a reset are separated by the guest
// being destroyed and rebuilt, and any bookkeeping carried across that gap is a
// chance for the restore to disagree with the file it is restoring. The bytes
// cannot disagree with themselves.
//
// The flag is not optional for zstd, even though it is for gzip: GNU tar
// auto-detects compression when it can seek, and a stage-in streams the archive
// into the guest over stdin, where it cannot ("Archive is compressed. Use --zstd
// option"). "" is returned for an unrecognised archive so tar gets its own
// chance to work it out, which is the right answer for a plain, uncompressed tar
// and produces tar's own diagnostic for anything else.
func tarDecompressFlag(r io.ReaderAt) string {
	var magic [4]byte
	n, err := r.ReadAt(magic[:], 0)
	if n == 0 && err != nil {
		return ""
	}
	head := magic[:n]
	switch {
	case bytes.HasPrefix(head, zstdMagic):
		return "--zstd"
	case bytes.HasPrefix(head, gzipMagic):
		return "-z"
	}
	return ""
}
