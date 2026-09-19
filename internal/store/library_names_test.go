package store

import "testing"

// TestLibraryNames checks that a library's display-name override round-trips
// through the database, that an unset library reports "", and that setting
// an empty name clears a previously-set override.
func TestLibraryNames(t *testing.T) {
	st := openTestStore(t)

	name, err := st.GetLibraryName("/libA")
	if err != nil {
		t.Fatal(err)
	}
	if name != "" {
		t.Fatalf("GetLibraryName on an unset library = %q, want \"\"", name)
	}

	if err := st.SetLibraryName("/libA", "Marvel Comics"); err != nil {
		t.Fatal(err)
	}
	if name, err := st.GetLibraryName("/libA"); err != nil || name != "Marvel Comics" {
		t.Fatalf("GetLibraryName after set = %q, %v, want %q, nil", name, err, "Marvel Comics")
	}

	// Setting it again overwrites, rather than erroring on a duplicate key.
	if err := st.SetLibraryName("/libA", "Marvel"); err != nil {
		t.Fatal(err)
	}
	if name, err := st.GetLibraryName("/libA"); err != nil || name != "Marvel" {
		t.Fatalf("GetLibraryName after overwrite = %q, %v, want %q, nil", name, err, "Marvel")
	}

	// A different library is unaffected.
	if name, err := st.GetLibraryName("/libB"); err != nil || name != "" {
		t.Fatalf("GetLibraryName(/libB) = %q, %v, want \"\", nil", name, err)
	}

	// Setting "" clears the override.
	if err := st.SetLibraryName("/libA", ""); err != nil {
		t.Fatal(err)
	}
	if name, err := st.GetLibraryName("/libA"); err != nil || name != "" {
		t.Fatalf("GetLibraryName after clearing = %q, %v, want \"\", nil", name, err)
	}
}
