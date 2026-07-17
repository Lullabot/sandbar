package lima

// provenance.go carries the ONE piece of the provider-package Provenancer
// implementation that has to live in this package: the marker's filename and
// path. The marker's PAYLOAD type (provider.Provenance) and its JSON
// encode/decode live in internal/provider instead, deliberately — package
// provider already imports package lima (see provider/local.go), so lima
// importing provider back to reference Provenance would cycle. Keeping the
// marker generic (a plain named file, read/written as raw bytes through
// HostFiles) is what lets this package stay provenance-payload-agnostic while
// still owning where the marker lives on a Lima host.

import "path/filepath"

// MarkerFilename is the name of the provenance marker sand writes into an
// instance directory it created, e.g. <LimaHome>/<name>/MarkerFilename.
const MarkerFilename = "sandbar.json"

// MarkerPath is the provenance marker path for instance name under hf's Lima
// home. hf.LimaHome() may be RELATIVE (a remote host's default ".lima" — see
// HostFiles.LimaHome); this does not attempt to make it absolute, since a
// relative path is exactly what a remote login shell (cat/stat/mkdir) resolves
// against $HOME on its own.
func MarkerPath(hf HostFiles, name string) string {
	return filepath.Join(hf.LimaHome(), name, MarkerFilename)
}
