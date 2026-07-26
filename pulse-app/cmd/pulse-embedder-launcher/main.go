package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const authorityNodeEnv = "PULSE_PRODUCT_AUTHORITY_NODE"

func main() {
	if err := run(os.Args[1:]); err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			os.Exit(exitError.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "pulse-embedder: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	node, err := trustedExecutable(os.Getenv(authorityNodeEnv))
	if err != nil {
		return fmt.Errorf("trusted Node runtime is unavailable: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("launcher path is unavailable: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return fmt.Errorf("launcher path is unsafe: %w", err)
	}
	runner := filepath.Clean(filepath.Join(filepath.Dir(self), "..", "runtime", "runner.mjs"))
	if err := regularFile(runner); err != nil {
		return fmt.Errorf("portable runner is unavailable: %w", err)
	}

	command := exec.Command(node, append([]string{runner}, args...)...)
	command.Env = os.Environ()
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func trustedExecutable(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("authority path must be absolute and clean")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if resolved != path {
		return "", errors.New("authority path must already be canonical")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", errors.New("authority path must be an executable regular file")
	}
	return path, nil
}

func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("path must be a regular file")
	}
	return nil
}
