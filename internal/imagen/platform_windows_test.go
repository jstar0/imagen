package imagen

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsAtomicReplacementWaitsForReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := atomicJSON(path, map[string]int{"generation": 1}); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	done := make(chan error, 1)
	go func() { done <- atomicJSON(path, map[string]int{"generation": 2}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		if _, err := os.Stat(path); err != nil {
			t.Fatal("old state was removed while locked", err)
		}
		reader.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "{\n  \"generation\": 2\n}\n" {
		t.Fatalf("replacement: %q %v", b, err)
	}
}

func TestWindowsPrivateCredentialDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.key")
	if err := writePrivateTestFile(path, []byte("fixture-secret")); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(path); err != nil {
		t.Fatal(err)
	}
	if err := makeTestFilePublic(path); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivate(path); err == nil {
		t.Fatal("Everyone-readable credential accepted")
	}
}
