// QomraNote — a visual, board-based workspace for creative work.
// Architecture is modeled on the Milanote Deep Research Report:
// typed elements, transaction-based mutations, element-granular realtime sync.
package main

import (
	"fmt"
	"os"

	"qomranote/backend/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
