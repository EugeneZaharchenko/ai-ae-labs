// F10: WritePath пише атомарно (temp+rename), а не голим os.WriteFile.
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSurface_WritePathIsAtomic(t *testing.T) {
	root := t.TempDir()
	surface, err := NewSurface(root, "strategy.md")
	if err != nil {
		t.Fatalf("NewSurface: %v", err)
	}

	if err := surface.Write([]byte("v1")); err != nil {
		t.Fatalf("перший запис: %v", err)
	}
	if err := surface.Write([]byte("v2, довше за v1")); err != nil {
		t.Fatalf("другий запис: %v", err)
	}

	got, err := surface.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "v2, довше за v1" {
		t.Errorf("вміст = %q, хотіли %q", got, "v2, довше за v1")
	}

	// Жодного тимчасового файлу не мало лишитись — Rename або прибрав його
	// сам (перейменувавши), або defer os.Remove прибрав би за помилки, якої
	// тут нема.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "strategy.md" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("root містить %v, хотіли рівно [\"strategy.md\"] — без залишених .tmp-файлів", names)
	}
}

func TestAtomicWriteFile_RejectsUnwritableDir(t *testing.T) {
	// Немає такого каталогу — CreateTemp має провалитись до будь-якого запису
	// чи перейменування, а не впасти напівшляху.
	path := filepath.Join(t.TempDir(), "no-such-subdir", "strategy.md")
	if err := atomicWriteFile(path, []byte("x"), 0o644); err == nil {
		t.Fatal("хотіли помилку: батьківського каталогу не існує")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("файл не мав з'явитися після провалу запису")
	}
}
