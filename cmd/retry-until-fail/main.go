// Command retry re-runs a command a number of times until it fails.
//
//   Usage: retry <count> <command> [args...]
//
// Copied from Apache 2.0 LICENSED
// https://github.com/foxygoat/s/blob/master/cmd/retry/retry.go
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func main() {
	count, dur, args, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := retry(count, dur, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseArgs(args []string) (int, time.Duration, []string, error) {
	if len(args) < 3 {
		return 0, 0, nil, fmt.Errorf("usage: retry <count> <sleep> <command> [args...]")
	}
	count, err := strconv.Atoi(args[0])
	if err != nil {
		return 0, 0, nil, fmt.Errorf("invalid count: %w", err)
	}
	if count < 0 {
		return 0, 0, nil, fmt.Errorf("negative retries are not allowed")
	}
	dur, err := time.ParseDuration(args[1])
	if err != nil {
		return 0, 0, nil, fmt.Errorf("invalid sleep duration: %w", err)
	}
	return count, dur, args[2:], nil
}

func retry(count int, dur time.Duration, args []string) error {
	for i := 0; i <= count; i++ {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err := cmd.Run()
		var e *exec.ExitError
		if err != nil && !errors.As(err, &e) {
			return fmt.Errorf("cannot run command, %w", err)
		}
		if cmd.ProcessState.ExitCode() != 0 {
			return nil
		}
		time.Sleep(dur)
	}
	return fmt.Errorf("ran out of retries")
}
