//go:build darwin

package tray

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCommandLinksRotaAndGlossButKeepsAGlossOfTheirOwn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	app := filepath.Join(t.TempDir(), "rota.app", "Contents", "MacOS")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"rota", "gloss"} {
		if err := os.WriteFile(filepath.Join(app, b), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(home, ".local", "bin")
	links := func() (rota, gloss string) {
		rota, _ = os.Readlink(filepath.Join(bin, "rota"))
		gloss, _ = os.Readlink(filepath.Join(bin, "gloss"))
		return
	}

	if _, err := InstallCommand(filepath.Join(app, "rota")); err != nil {
		t.Fatal(err)
	}
	if r, g := links(); r != filepath.Join(app, "rota") || g != filepath.Join(app, "gloss") {
		t.Fatalf("rota -> %q, gloss -> %q: both should be the app's", r, g)
	}

	// another rota.app's gloss is replaced, as rota is
	moved := filepath.Join(t.TempDir(), "rota.app", "Contents", "MacOS")
	os.MkdirAll(moved, 0o755)
	os.Rename(filepath.Join(app, "rota"), filepath.Join(moved, "rota"))
	os.Rename(filepath.Join(app, "gloss"), filepath.Join(moved, "gloss"))
	if _, err := InstallCommand(filepath.Join(moved, "rota")); err != nil {
		t.Fatal(err)
	}
	if r, g := links(); r != filepath.Join(moved, "rota") || g != filepath.Join(moved, "gloss") {
		t.Fatalf("after a move: rota -> %q, gloss -> %q", r, g)
	}

	// a gloss of their own, a file or a link elsewhere, stays
	for _, own := range []func(string) error{
		func(p string) error { return os.WriteFile(p, []byte("own"), 0o755) },
		func(p string) error { return os.Symlink("/opt/gloss/bin/gloss", p) },
	} {
		g := filepath.Join(bin, "gloss")
		os.Remove(g)
		if err := own(g); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Lstat(g)
		if _, err := InstallCommand(filepath.Join(moved, "rota")); err != nil {
			t.Fatal(err)
		}
		after, err := os.Lstat(g)
		if err != nil || !os.SameFile(before, after) {
			t.Fatalf("a gloss of their own was replaced")
		}
	}
}
