package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "containish",
	Short: "Contain-ish is a naive containerization system",
	Long:  `Contain-ish is a simplistic containerization system built for educational purposes.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Contain-ish: A naive containerization system. Use 'containish --help' for more information.")
	},
}

// Execute runs the root command and adds child commands
func Execute() {
	rootCmd.AddCommand(runCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
