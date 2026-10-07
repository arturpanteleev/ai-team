package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func cmdDB() {
	if len(os.Args) < 3 || (os.Args[2] != "backup" && os.Args[2] != "restore") {
		fatal("Использование: ai-team db backup --db <existing.sqlite> --out <new-snapshot.sqlite> | ai-team db restore --from <snapshot.sqlite> --out <new.sqlite>")
	}
	operation := os.Args[2]
	flags := flag.NewFlagSet("db "+operation, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	database := flags.String("db", "", "existing SQLite controller database (backup)")
	snapshot := flags.String("from", "", "SQLite database snapshot to restore")
	destination := flags.String("out", "", "new SQLite database path")
	if err := flags.Parse(os.Args[3:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	if flags.NArg() != 0 || *destination == "" {
		fatal("Использование: ai-team db backup --db <existing.sqlite> --out <new-snapshot.sqlite> | ai-team db restore --from <snapshot.sqlite> --out <new.sqlite>")
	}
	switch operation {
	case "backup":
		if *database == "" || *snapshot != "" {
			fatal("Использование: ai-team db backup --db <existing.sqlite> --out <new-snapshot.sqlite>")
		}
		if err := store.BackupDatabase(context.Background(), *database, *destination); err != nil {
			fatal("Не удалось создать SQLite database snapshot: %v", err)
		}
		fmt.Printf("SQLite controller database snapshot written: %s\n", *destination)
	case "restore":
		if *snapshot == "" || *database != "" {
			fatal("Использование: ai-team db restore --from <snapshot.sqlite> --out <new.sqlite>")
		}
		if err := store.RestoreDatabaseSnapshot(context.Background(), *snapshot, *destination); err != nil {
			fatal("Не удалось восстановить SQLite database snapshot: %v", err)
		}
		fmt.Printf("SQLite controller database restored to new path: %s\n", *destination)
	}
	fmt.Println("Операция затрагивает только SQLite database; run evidence и artifacts резервируются отдельно.")
}
