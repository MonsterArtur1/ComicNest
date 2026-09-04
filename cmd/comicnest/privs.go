package main

import (
	"fmt"
	"strconv"
)

// parseIDs reads the PUID/PGID values. Both empty = not set. One of them
// alone is allowed: the missing one defaults to the other (uid == gid is the
// common single-user NAS case).
func parseIDs(puid, pgid string) (uid, gid int, set bool, err error) {
	if puid == "" && pgid == "" {
		return 0, 0, false, nil
	}
	parse := func(name, v string) (int, error) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%s: %q is not a valid id", name, v)
		}
		return n, nil
	}
	if puid != "" {
		if uid, err = parse("PUID", puid); err != nil {
			return 0, 0, false, err
		}
	}
	if pgid != "" {
		if gid, err = parse("PGID", pgid); err != nil {
			return 0, 0, false, err
		}
	}
	if puid == "" {
		uid = gid
	}
	if pgid == "" {
		gid = uid
	}
	return uid, gid, true, nil
}
