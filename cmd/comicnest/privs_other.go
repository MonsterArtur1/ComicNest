//go:build !linux

package main

import (
	"log"
	"os"
)

// dropPrivileges is Linux-only (containers); elsewhere PUID/PGID are ignored.
func dropPrivileges(configPath, dataDir string) error {
	if _, _, set, err := parseIDs(os.Getenv("PUID"), os.Getenv("PGID")); err != nil {
		return err
	} else if set {
		log.Printf("privileges: PUID/PGID are only supported on Linux — ignored")
	}
	return nil
}
