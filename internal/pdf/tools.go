package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// run executes a binary with the given args, optionally piping stdinBytes
// to stdin, and returns stdout plus any non-zero-exit error with stderr
// attached for diagnostics.
//
// timeout applies to the whole invocation (stdin write, process wait,
// output read). Zero disables it and only ctx bounds the process.
//
// Stderr is captured into the returned error on failure because poppler
// and tesseract write actionable diagnostics there:
//
//	"Error: PDF file is damaged"
//	"Error: Copying of text from this document is not allowed"
//	"Error: Incorrect password"
//	"Syntax Error: Missing 'endstream'"
//
// Surfacing these lets Phase 2+ match on known strings to return
// semantic errors (ErrEncrypted, etc.) rather than opaque exit codes.
func run(ctx context.Context, timeout time.Duration, name string, stdinBytes []byte, args ...string) ([]byte, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...)
	if len(stdinBytes) > 0 {
		cmd.Stdin = bytes.NewReader(stdinBytes)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdout.Bytes(), fmt.Errorf("%s: timed out after %s: %w", name, timeout, err)
		}
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %w: %s", name, err, stderrStr)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", name, err)
	}
	return stdout.Bytes(), nil
}
