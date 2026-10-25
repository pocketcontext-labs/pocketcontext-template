// Command templatecontext is the TemplateContext server and client in one binary.
package main

import (
	"os"

	"github.com/pocketcontext-labs/pocketcontext-template/internal/client"
)

func main() {
	os.Exit(client.Run(os.Args[1:]))
}
