package version

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/Diudiuseven/dev-server-hmr-go/internal/artifact"
)

// Hash computes a content-addressed version id for one module build.
//
// The id is a function of:
//   - the module name,
//   - the versions of all its dependencies,
//   - the produced artifact contents.
//
// Therefore identical inputs always yield the same id (so rebuilding
// unchanged content does not produce a new version number) and any
// content change, or any dependency version change, yields a new id.
func Hash(module string, depVersions map[string]string, arts []artifact.Artifact) string {
	h := sha256.New()
	writeString := func(tag, s string) {
		h.Write([]byte(tag))
		h.Write([]byte{':'})
		writeLen(h, len(s))
		h.Write([]byte(s))
	}
	writeString("module", module)

	// Sort dependency names so map iteration order cannot affect the hash.
	depNames := make([]string, 0, len(depVersions))
	for dep := range depVersions {
		depNames = append(depNames, dep)
	}
	sort.Strings(depNames)
	writeLen(h, len(depNames))
	for _, dep := range depNames {
		writeString("dep", dep)
		writeString("ver", depVersions[dep])
	}

	// A module with no artifacts is a valid build; record the count so
	// "no artifacts" is distinguishable from artifact paths themselves.
	writeLen(h, len(arts))
	index := make([]int, len(arts))
	for i := range arts {
		index[i] = i
	}
	sort.Slice(index, func(a, b int) bool {
		return arts[index[a]].Path < arts[index[b]].Path
	})
	for _, i := range index {
		a := arts[i]
		writeString("path", a.Path)
		writeLen(h, len(a.Content))
		h.Write(a.Content)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// writeLen writes n as 8 big-endian bytes as a length/framing prefix.
func writeLen(h interface{ Write([]byte) (int, error) }, n int) {
	var buf [8]byte
	v := uint64(n)
	for i := 7; i >= 0; i-- {
		buf[i] = byte(v)
		v >>= 8
	}
	h.Write(buf[:])
}
