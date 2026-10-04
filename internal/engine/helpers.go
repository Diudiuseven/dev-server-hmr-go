package engine

import "path/filepath"

func joinPath(parts ...string) string { return filepath.Join(parts...) }

// storeKey is the content-addressed storage key for one module version.
func storeKey(module, versionID string) string {
	return module + "/" + versionID
}
