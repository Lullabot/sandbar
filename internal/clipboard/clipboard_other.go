//go:build !darwin && !linux

package clipboard

// readImagePNG is the non-unix stub: there is no probe implemented for this
// platform, so the feature degrades cleanly to "unsupported" rather than
// mis-probing.
func readImagePNG() ([]byte, error) {
	return nil, ErrUnsupported
}
