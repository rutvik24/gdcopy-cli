package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListVolumesFromDir(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "USB-DRIVE"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	volumes := listVolumesFromDir(root)
	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d: %+v", len(volumes), volumes)
	}
	if volumes[0].Name != "USB-DRIVE" {
		t.Fatalf("unexpected volume name: %q", volumes[0].Name)
	}
}

func TestIsNavigableDir(t *testing.T) {
	root := t.TempDir()
	if !isNavigableDir(root) {
		t.Fatal("expected temp dir to be navigable")
	}
	if isNavigableDir(filepath.Join(root, "missing")) {
		t.Fatal("expected missing path to be non-navigable")
	}
}
