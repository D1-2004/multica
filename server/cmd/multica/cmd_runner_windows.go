//go:build windows

package main

import (
	"context"
	"errors"
	"os/exec"
)

func runnerSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

func acquireRunnerInstanceLock(string) (func(), error) {
	return nil, errors.New("Multica Runner currently supports macOS and Linux")
}

func configureRunnerDetached(*exec.Cmd) error {
	return errors.New("Multica Runner currently supports macOS and Linux")
}

func runnerProcessRunning(int) bool { return false }

func stopRunnerPID(int) error {
	return errors.New("Multica Runner currently supports macOS and Linux")
}

func configureRunnerProcessGroup(*exec.Cmd) error {
	return errors.New("Multica Runner currently supports macOS and Linux")
}

func killRunnerProcessGroup(*exec.Cmd) error {
	return errors.New("Multica Runner currently supports macOS and Linux")
}
