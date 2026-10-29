package main

import "testing"

func TestPermissionName(t *testing.T) {
	for _, ok := range []string{"project:read", "tasks:assignable", "a.b_c-d"} {
		if !permissionName.MatchString(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	long := ""
	for range 65 {
		long += "a"
	}
	for _, bad := range []string{"", "  ", " project:read", "project:read ", "Project:Read", "project read", long} {
		if permissionName.MatchString(bad) {
			t.Errorf("%q taken", bad)
		}
	}
}
