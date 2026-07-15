package lima

import (
	"bytes"
	"io"
	"regexp"
)

// scpNoiseLine matches ssh/scp's connection chatter — everything -v prints that
// is NOT the transfer progress the -v was asked for — with or without the
// "/usr/bin/scp: " prefix Lima tags the child's stderr with. Beyond the "debug1:"
// lines, a SFTP-mode scp (the default in current OpenSSH, e.g. macOS) announces
// the ssh subprocess it spawns ("Executing: program /usr/bin/ssh host …, command
// sftp"), the client version banner, the host-key TOFU notice, and a byte
// summary at the end — all of which would otherwise leak into a create's build
// pane or a file-transfer window.
var scpNoiseLine = regexp.MustCompile(`^(?:\S+: )?(?:` +
	`debug[0-9]+: ` + // ssh debug, any level
	`|Executing: program ` + // scp's SFTP-mode "spawning ssh" banner
	`|OpenSSH_` + // the client version line
	`|Authenticated to ` +
	`|Transferred: ` + // scp -v transfer summary
	`|Bytes per second: ` +
	`|Warning: Permanently added ` + // first-connect host-key notice
	`|Connection to \S+ closed` +
	`)`)

// scpDebugFilter drops those lines from a copy's output stream.
//
// Copy passes -v so the TUI can stream transfer progress, but Lima hands the -v
// to scp, which turns on ssh's verbose logging as well: every file arriving emits
// a "truncating at <size>" line (scp pre-sizes the destination with ftruncate),
// and a SFTP-mode scp adds the connection banner above. Harvesting the apt cache
// copies a directory of .debs, so that is one line per package — noise that
// buries the progress the -v was asked for.
//
// Chunks off the pipe are not line-aligned, so partial lines are held back until
// their newline arrives; Flush emits whatever is left when the copy ends.
type scpDebugFilter struct {
	w   io.Writer
	buf []byte
}

// Write forwards complete non-debug lines and buffers any trailing partial line.
// It always reports the full length as written: dropping a line is the point, not
// a short write, and a short write is an error to the caller.
func (f *scpDebugFilter) Write(p []byte) (int, error) {
	f.buf = append(f.buf, p...)
	for {
		i := bytes.IndexByte(f.buf, '\n')
		if i < 0 {
			break
		}
		line := f.buf[:i+1]
		f.buf = f.buf[i+1:]
		if scpNoiseLine.Match(line) {
			continue
		}
		if _, err := f.w.Write(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush writes back a held partial line — output that ended without a newline,
// which scp does for its final progress line.
func (f *scpDebugFilter) Flush() error {
	if len(f.buf) == 0 || scpNoiseLine.Match(f.buf) {
		f.buf = nil
		return nil
	}
	_, err := f.w.Write(f.buf)
	f.buf = nil
	return err
}
