// Package executil runs commands with cancellation that covers output collection,
// even when the shell has already exited and descendants still hold its pipes.
package executil

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Run starts cmd and waits for both process exit and output EOF. cmd.Cancel must
// terminate the process tree when configured; otherwise only the process is killed.
// afterStart attaches platform process-tree guards immediately after Start.
func Run(ctx context.Context, cmd *exec.Cmd, afterStart func()) error {
	type outputPipe struct {
		reader *os.File
		writer *os.File
		target io.Writer
		err    error
	}
	var pipes []*outputPipe
	stdin := cmd.Stdin
	var stdinReader, stdinWriter *os.File
	defer func() {
		if stdinReader != nil {
			_ = stdinReader.Close()
			_ = stdinWriter.Close()
		}
		for _, pipe := range pipes {
			_ = pipe.reader.Close()
			_ = pipe.writer.Close()
		}
	}()
	// Own non-file stdin as well. A short Cmd.WaitDelay would close it when
	// the shell exits, truncating input still being consumed by a child.
	if stdin != nil {
		if _, isFile := stdin.(*os.File); !isFile {
			var err error
			stdinReader, stdinWriter, err = os.Pipe()
			if err != nil {
				return err
			}
			cmd.Stdin = stdinReader
		}
	}
	for _, output := range []*io.Writer{&cmd.Stdout, &cmd.Stderr} {
		if *output == nil {
			continue
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			return err
		}
		pipes = append(pipes, &outputPipe{reader: reader, writer: writer, target: *output})
		*output = writer
	}

	// Cancellation can come from os/exec's watcher or our output-lifetime watcher.
	// Serialize it, and never mutate Cmd fields from a cancellation callback.
	originalCancel := cmd.Cancel
	var cancelOnce sync.Once
	var cancelErr error
	cmd.Cancel = func() error {
		cancelOnce.Do(func() {
			if originalCancel != nil {
				cancelErr = originalCancel()
			} else if cmd.Process != nil {
				cancelErr = cmd.Process.Kill()
			}
		})
		return cancelErr
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if afterStart != nil {
		afterStart()
	}
	var copies sync.WaitGroup
	var stdinErr error
	if stdinReader != nil {
		_ = stdinReader.Close() // only the child owns the read end now
		copies.Add(1)
		go func() {
			defer copies.Done()
			_, stdinErr = io.Copy(stdinWriter, stdin)
			_ = stdinWriter.Close()
		}()
	}
	for _, pipe := range pipes {
		_ = pipe.writer.Close() // only the child owns the write end now
		copies.Add(1)
		go func() {
			defer copies.Done()
			_, pipe.err = io.Copy(pipe.target, pipe.reader)
		}()
	}

	// os/exec can stop watching the context as soon as the shell exits. Keep
	// watching until output is drained, killing surviving children and closing
	// our readers even if a detached descendant retains an inherited write end.
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(canceled)
		_ = cmd.Cancel()
		if stdinWriter != nil {
			_ = stdinWriter.Close()
		}
		for _, pipe := range pipes {
			_ = pipe.reader.Close()
		}
	})
	defer func() {
		if !stop() {
			<-canceled
		}
	}()
	err := cmd.Wait()
	copies.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		if stdinErr != nil {
			// Like os/exec, ignore a child closing its input before consuming
			// everything, but preserve failures reading the supplied source.
			var pipeErr *os.PathError
			if !errors.As(stdinErr, &pipeErr) || pipeErr.Op != "write" {
				return stdinErr
			}
		}
		for _, pipe := range pipes {
			if pipe.err != nil {
				return pipe.err
			}
		}
	}
	return err
}
