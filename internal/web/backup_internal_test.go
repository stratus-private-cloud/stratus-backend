package web

import (
	"io/fs"
	"strings"
	"testing"
)

// TestNothingOfNpmIsEmbedded: npm runs the backup page's tests and nothing
// else (#337), from web-test/ at the root of the repository. Anything of it
// under a directory this package embeds would ship in the binary.
func TestNothingOfNpmIsEmbedded(t *testing.T) {
	t.Parallel()

	for _, tree := range []fs.FS{staticFS, backupFS, templateFS} {
		err := fs.WalkDir(tree, ".", func(path string, _ fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			for _, banned := range []string{"node_modules", "package.json", "package-lock.json", ".test.js"} {
				if strings.Contains(path, banned) {
					t.Errorf("%s is embedded in the binary", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
