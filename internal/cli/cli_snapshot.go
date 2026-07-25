package cli

import (
	"fmt"
	"os"
)

func cmdSnapshot(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "snapshot {create|restore} <file> [--db <dsn>] [--objects <root>]")
		return 2
	}
	mode, file := args[0], args[1]
	db, objects := parseSnapshotFlags(args[2:])
	switch mode {
	case "create":
		if err := snapshotCreate(file, db, objects); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("snapshot written:", file)
	case "restore":
		if err := snapshotRestore(file, db, objects); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Println("snapshot restored from:", file)
	default:
		fmt.Fprintln(os.Stderr, "snapshot mode must be create|restore")
		return 2
	}
	return 0
}

func parseSnapshotFlags(args []string) (db, objects string) {
	db = os.Getenv("DB_DSN")
	if db == "" {
		db = "file:./var/aero.db"
	}
	objects = os.Getenv("STORAGE_LOCAL_ROOT")
	if objects == "" {
		objects = "./var/objects"
	}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--db":
			db = args[i+1]
		case "--objects":
			objects = args[i+1]
		}
	}
	return
}
