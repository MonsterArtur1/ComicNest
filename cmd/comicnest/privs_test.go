package main

import "testing"

func TestParseIDs(t *testing.T) {
	for _, tc := range []struct {
		puid, pgid string
		uid, gid   int
		set, bad   bool
	}{
		{"", "", 0, 0, false, false},
		{"1026", "100", 1026, 100, true, false},
		{"1026", "", 1026, 1026, true, false},
		{"", "100", 100, 100, true, false},
		{"abc", "100", 0, 0, false, true},
		{"-1", "", 0, 0, false, true},
	} {
		uid, gid, set, err := parseIDs(tc.puid, tc.pgid)
		if (err != nil) != tc.bad || uid != tc.uid || gid != tc.gid || set != tc.set {
			t.Errorf("parseIDs(%q, %q) = %d, %d, %v, %v", tc.puid, tc.pgid, uid, gid, set, err)
		}
	}
}
