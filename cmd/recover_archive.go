package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	gutils "github.com/Laisky/go-utils"
	"github.com/spf13/cobra"
	"gofluentd/internal/controller"
	"gofluentd/internal/global"
)

func newRecoverArchiveCommand() *cobra.Command {
	var execute bool
	c := &cobra.Command{
		Use:           "recover-otlp-archive",
		Short:         "Validate retained OTLP originals; explicitly replay after downstream loss",
		Long:          "Stop the collector owning this root first. The default validates and counts without sending. --execute replays all retained accepted envelopes to configured logical destinations and may duplicate previously delivered rows. Originals remain retained.",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			setupSettings()
			b, e := controller.ParseDiskBudgetConfig(gutils.Settings.Get("settings.disk_budget"))
			if e != nil {
				return e
			}
			cfg, e := controller.ParseOTLPServiceConfig(gutils.Settings.Get("settings.otlp"))
			if e != nil {
				return e
			}
			if e = b.ApplyOTLP(cfg); e != nil {
				return e
			}
			if cfg == nil {
				return fmt.Errorf("OTLP configuration is required")
			}
			count, e := controller.RecoverOTLPAcceptedArchive(ctx, *cfg, execute)
			fmt.Fprintf(cmd.OutOrStdout(), "accepted_archive_envelopes=%d execute=%t originals_retained=true\n", count, execute)
			return e
		},
	}
	c.Flags().StringVarP(&global.Config.CMDArgs.ConfigPath, "config", "c", "/etc/go-fluentd/settings/settings.yml", "existing collector configuration")
	c.Flags().BoolVar(&execute, "execute", false, "replay all validated accepted originals; may duplicate records")
	return c
}
func init() { rootCmd.AddCommand(newRecoverArchiveCommand()) }
