package engine

import "github.com/Diudiuseven/dev-server-hmr-go/internal/builder"

// hashSource is the content hash of a module's source directory.
func hashSource(root string) (string, error) { return builder.HashDir(root) }
