package main

import (
	"log"

	"github.com/LeanerCloud/CUDly/internal/apiserver"
	pkgconfig "github.com/LeanerCloud/CUDly/pkg/config"
	"github.com/spf13/cobra"
)

var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the CUDly HTTP API server",
	RunE:  runServer,
}

func init() {
	serverCmd.Flags().String("listen", ":8080", "Address to listen on")
	serverCmd.Flags().String("api-key-env", "CUDLY_API_KEY", "Environment variable name containing the API key")
	rootCmd.AddCommand(serverCmd)
}

func runServer(cmd *cobra.Command, args []string) error {
	cfg, err := pkgconfig.Load("", cmd.Flags())
	if err != nil {
		return err
	}

	if cmd.Flags().Changed("listen") {
		v, _ := cmd.Flags().GetString("listen")
		cfg.Server.Listen = v
	}
	if cmd.Flags().Changed("api-key-env") {
		v, _ := cmd.Flags().GetString("api-key-env")
		cfg.Server.APIKeyEnv = v
	}
	cfg.Server.Enabled = true

	srv := apiserver.NewServer(cfg)
	log.Printf("Starting CUDly API server on %s", cfg.Server.Listen)
	return srv.Start()
}
