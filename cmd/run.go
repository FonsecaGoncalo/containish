package cmd

import (
	"containish/container"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run [command]",
	Short: "Run a command inside a new container",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Contain-ish: Running '%v' inside a container.\n", args)

		if err := container.RunContainer("container_id"); err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
	},
}
