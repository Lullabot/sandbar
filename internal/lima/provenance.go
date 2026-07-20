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
	return filepath.Join(InstanceDir(hf, name), MarkerFilename)
}

// InstanceDir is instance name's directory under hf's Lima home — the directory
// Lima itself creates for an instance, and the one the marker lives inside.
// Relative for the same reason MarkerPath is.
//
// It is exported so a marker write can CHECK that the instance exists before
// writing. That check is not fussiness: a directory under LIMA_HOME holding a
// sandbar.json but no lima.yaml makes every later `limactl list` FATAL (see
// provision/cleanup.go), so a marker write that creates its own parent does not
// merely write a useless file — it wedges the whole tool.
func InstanceDir(hf HostFiles, name string) string {
	return filepath.Join(hf.LimaHome(), name)
}
