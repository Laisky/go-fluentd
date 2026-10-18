package cmd

import (
	"fmt"
	gutils "github.com/Laisky/go-utils"
	"github.com/spf13/cobra"
	"gofluentd/internal/controller"
	"gofluentd/internal/global"
	"os"
	"os/signal"
	"syscall"
)

func newRecoverLegacyArchiveCommand() *cobra.Command {
	var directory, sourceID string
	var execute bool
	c := &cobra.Command{
		Use:   "recover-legacy-archive",
		Short: "Validate or explicitly replay a closed legacy child journal as a lossless OTLP archive",
		Long:  "Stop the source collector first. Select one existing child journal directory and a stable source identity. Default validation sends nothing. --execute wraps exact MessagePack originals as OTLP byte bodies to configured destinations; source data and ACK files remain unchanged. Replay may duplicate records.",
		Args:  cobra.NoArgs, SilenceErrors: true, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			setupSettings()
			cfg, e := controller.ParseOTLPServiceConfig(gutils.Settings.Get("settings.otlp"))
			if e != nil {
				return e
			}
			if cfg == nil {
				return fmt.Errorf("OTLP destination configuration is required")
			}
			count, e := controller.RecoverLegacyArchive(ctx, *cfg, directory, sourceID, execute)
			fmt.Fprintf(cmd.OutOrStdout(), "legacy_archive_records=%d execute=%t originals_retained=true\n", count, execute)
			return e
		},
	}
	c.Flags().StringVarP(&global.Config.CMDArgs.ConfigPath, "config", "c", "/etc/go-fluentd/settings/settings.yml", "existing OTLP destination configuration")
	c.Flags().StringVar(&directory, "directory", "", "existing closed child journal directory")
	c.Flags().StringVar(&sourceID, "source-id", "", "stable identity for this source archive")
	c.Flags().BoolVar(&execute, "execute", false, "replay validated original byte records; may duplicate delivery")
	c.MarkFlagRequired("directory")
	c.MarkFlagRequired("source-id")
	return c
}
func init() { rootCmd.AddCommand(newRecoverLegacyArchiveCommand()) }
