package main

import (
	"fmt"
	"os"

	"github.com/jeffdhooton/trawl/internal/version"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	if isTerminal(os.Stderr) {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}

	root := newRootCmd()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var logLevel string

	cmd := &cobra.Command{
		Use:   "trawl",
		Short: "Intelligent tiered web scraping",
		Long: `trawl routes each URL through the cheapest engine that returns valid content:
HTTP → Lightpanda → Chromium. Persistent frontier, polite by default, resumable.`,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			lvl, err := zerolog.ParseLevel(logLevel)
			if err != nil {
				return fmt.Errorf("invalid --log-level: %w", err)
			}
			zerolog.SetGlobalLevel(lvl)
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "log level (debug, info, warn, error)")

	cmd.AddCommand(newVersionCmd())
	cmd.AddCommand(newScrapeCmd())
	cmd.AddCommand(newBatchCmd())
	cmd.AddCommand(newResumeCmd())
	cmd.AddCommand(newSitemapCmd())

	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("trawl %s (commit %s, built %s)\n", version.Version, version.Commit, version.Date)
		},
	}
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
