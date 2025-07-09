package cmd

import (
	"containish/container"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop <container-id>",
	Short: "Stop a running container",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		id := args[0]
		fmt.Printf("Contain-ish: Stopping '%v'\n", id)
		if err := container.StopContainer(id); err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
	},
}
