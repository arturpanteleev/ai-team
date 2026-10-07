package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func cmdDB() {
	if len(os.Args) < 3 || os.Args[2] != "backup" {
		fatal("Использование: ai-team db backup --db <existing.sqlite> --out <new-snapshot.sqlite>")
	}
	flags := flag.NewFlagSet("db backup", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	source := flags.String("db", "", "existing SQLite controller database")
	destination := flags.String("out", "", "new SQLite snapshot path")
	if err := flags.Parse(os.Args[3:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 0 || *source == "" || *destination == "" {
		fatal("Использование: ai-team db backup --db <existing.sqlite> --out <new-snapshot.sqlite>")
	}
	if err := store.BackupDatabase(context.Background(), *source, *destination); err != nil {
		fatal("Не удалось создать SQLite database snapshot: %v", err)
	}
	fmt.Printf("SQLite controller database snapshot written: %s\n", *destination)
	fmt.Println("Снимок содержит только SQLite database; run evidence и artifacts резервируются отдельно.")
}
