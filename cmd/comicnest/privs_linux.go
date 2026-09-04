//go:build linux

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
)

// dropPrivileges implements the PUID/PGID convention of NAS container images
// (Synology, Unraid, linuxserver.io): the container starts as root, takes
// ownership of the writable directories for the requested user, then switches
// to that user before touching anything else. Without PUID/PGID (or when not
// root) it is a no-op, so plain `docker run --user` and bare-metal runs are
// unaffected. Read-only paths (the library) are never chowned.
func dropPrivileges(configPath, dataDir string) error {
	uid, gid, set, err := parseIDs(os.Getenv("PUID"), os.Getenv("PGID"))
	if err != nil {
		return err
	}
	if !set {
		return nil
	}
	if os.Geteuid() != 0 {
		log.Printf("privileges: PUID/PGID ignored — not running as root (current uid %d)", os.Geteuid())
		return nil
	}

	// Ensure the writable tree exists, then hand it over.
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	for _, p := range []string{configPath, filepath.Dir(configPath), dataDir} {
		if err := chownTree(p, uid, gid); err != nil {
			return err
		}
	}

	if err := syscall.Setgroups([]int{gid}); err != nil {
		return fmt.Errorf("setgroups: %w", err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("setgid %d: %w", gid, err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("setuid %d: %w", uid, err)
	}
	log.Printf("privileges: running as uid %d, gid %d (PUID/PGID)", uid, gid)
	return nil
}

// chownTree changes the owner of path and, for directories, everything below
// it. Entries that already belong to uid:gid are skipped, so restarts with a
// large cover cache stay cheap. A missing path is fine (config not created yet).
func chownTree(path string, uid, gid int) error {
	return filepath.WalkDir(path, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) == uid && int(sys.Gid) == gid {
			return nil
		}
		if err := os.Lchown(p, uid, gid); err != nil {
			return fmt.Errorf("chown %s: %w", p, err)
		}
		return nil
	})
}
