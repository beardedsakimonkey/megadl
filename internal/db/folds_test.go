package db

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCollapsedDirsMigrationAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.db")
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first := insertQueued(t, d, "first")
	second := insertQueued(t, d, "second")
	// Simulate a library created before folder state was stored.
	if _, err := d.sql.Exec(`DROP TABLE collapsed_dirs`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetCollapsedDirs(map[int64][]string{first: {"Season", "Season/Extras"}, second: {"Season"}}); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	want := map[int64]map[string]bool{first: {"Season": true, "Season/Extras": true}, second: {"Season": true}}
	got, err := d.CollapsedDirs()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("restored = %v, %v", got, err)
	}
	if err := d.SetCollapsedDirs(map[int64][]string{first: nil}); err != nil {
		t.Fatal(err)
	}
	delete(want, first)
	got, err = d.CollapsedDirs()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("expanded = %v, %v", got, err)
	}
	if err := d.DeleteDownload(second); err != nil {
		t.Fatal(err)
	}
	// A delayed save must not recreate state after deletion.
	if err := d.SetCollapsedDirs(map[int64][]string{second: {"Season"}}); err != nil {
		t.Fatal(err)
	}
	got, err = d.CollapsedDirs()
	if err != nil || len(got) != 0 {
		t.Fatalf("deleted = %v, %v", got, err)
	}
}
