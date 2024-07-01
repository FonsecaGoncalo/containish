//go:build linux

package main

import (
	"containish/cmd"
	"os"
)

func main() {
	if os.Args[1] == "init" {
		os.Exit(0)
	}
	cmd.Execute()
}
