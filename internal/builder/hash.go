package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path/filepath"
	"sort"
)

// HashDir computes a stable content hash of all regular files under dir.
// It depends only on relative paths and file contents, so identical source
// trees always hash identically regardless of mtime or location.
func HashDir(dir string) (string, error) {
	type entry struct {
		rel  string
		data []byte
	}
	var files []entry
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name != "." && name[0] == '.' && path != dir {
				return filepath.SkipDir // skip .git, .hmr, etc.
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := readFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files = append(files, entry{rel: filepath.ToSlash(rel), data: data})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })

	h := sha256.New()
	for _, f := range files {
		h.Write([]byte(f.rel))
		h.Write([]byte{0})
		h.Write(f.data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// CombinedHash folds own-source hash with the version ids of the
// dependencies being built against. This is the "source hash" used to
// decide whether a module needs rebuilding.
func CombinedHash(ownSource string, depVersions map[string]string) string {
	names := make([]string, 0, len(depVersions))
	for n := range depVersions {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	h.Write([]byte(ownSource))
	h.Write([]byte{0})
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write([]byte(depVersions[n]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ArtifactID is the content address of an artifact blob.
func ArtifactID(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
