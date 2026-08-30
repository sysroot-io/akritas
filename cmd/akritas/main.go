package main

import (
	"os"

	"akritas/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
