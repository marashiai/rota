package main

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// The setup guide and the blueprints it builds a team from, as this build has them: rota setup
// writes them where Claude Code can read them, so setting up needs no copy of the repository.
//
//go:embed setup/SETUP.md setup/blueprints/*.md
var guide embed.FS

// guideDir is where rota setup writes the guide: rota's folder in the user's cache.
func guideDir() string {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	return filepath.Join(cache, "rota", "setup")
}

// writeGuide replaces dir with this build's guide: SETUP.md, and blueprints/<role>.md next to it.
func writeGuide(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return fs.WalkDir(guide, "setup", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("setup", p)
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := guide.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
}
