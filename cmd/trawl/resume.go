package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <job-id>",
		Short: "Resume a previously interrupted job",
		Long: `Reopen the frontier for <job-id>, re-queue any in-flight URLs,
and drain the remaining work using the same configuration saved at job
creation time.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResume(cmd.Context(), args[0])
		},
	}
}

func runResume(parentCtx context.Context, jobID string) error {
	ctx, cancel := signal.NotifyContext(parentCtx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dir, err := jobDirFor(jobID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("job %q not found at %s: %w", jobID, dir, err)
	}

	cfg, err := loadJobConfig(dir)
	if err != nil {
		return err
	}
	log.Info().
		Str("job_id", cfg.ID).
		Str("job_dir", dir).
		Str("output", cfg.OutputPath).
		Msg("resuming job")

	return runJob(ctx, dir, cfg)
}
