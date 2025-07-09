package cmd

import (
	"containish/container"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var configPath string

var runCmd = &cobra.Command{
	Use:   "run <container-id>",
	Short: "Run a command inside a new container",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		id := args[0]
		fmt.Printf("Contain-ish: Running '%v' inside a container.\n", id)

		if err := container.RunContainer(id, configPath); err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
	},
}

func init() {
	runCmd.Flags().StringVarP(&configPath, "config", "c", "config.json", "path to OCI config file")
}
