package lockfile

import (
	"path/filepath"
	"testing"
)

func TestLockExcludesASecondHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "megadl.lock")
	first, second := New(path), New(path)

	ok, err := first.TryAcquire()
	if err != nil || !ok {
		t.Fatalf("first TryAcquire = %v, %v; want true, nil", ok, err)
	}
	// Separate open files, so the kernel treats them as separate holders even
	// inside one process — which is what makes this testable at all.
	if ok, err := second.TryAcquire(); ok || err != nil {
		t.Fatalf("second TryAcquire = %v, %v; want false, nil", ok, err)
	}
	// The holder asking again still holds it, rather than shutting itself out.
	if ok, err := first.TryAcquire(); !ok || err != nil {
		t.Fatalf("re-acquire = %v, %v; want true, nil", ok, err)
	}

	first.Release()
	if ok, err := second.TryAcquire(); !ok || err != nil {
		t.Fatalf("TryAcquire after release = %v, %v; want true, nil", ok, err)
	}
	second.Release()
	second.Release() // releasing a lock that isn't held is not an error
}
