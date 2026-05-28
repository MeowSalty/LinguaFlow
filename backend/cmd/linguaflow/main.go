// LinguaFlow CLI 入口。
package main

import (
	"os"

	"github.com/MeowSalty/LinguaFlow/backend/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
