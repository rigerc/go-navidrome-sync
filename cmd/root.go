package cmd

import (
	"fmt"
	"os"

	"github.com/rigerc/go-navidrome-ratings-sync/internal/config"
	"github.com/spf13/cobra"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "go-navidrome-ratings-sync",
	Short: "Sync ratings with Navidrome",
	Long:  "A tool to synchronize music ratings with a Navidrome server.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgFile)
		if err != nil {
			return err
		}
		fmt.Printf("Config loaded: %+v\n", cfg)
		return nil
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path (default: config.yaml)")
}
