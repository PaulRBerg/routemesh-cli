// Package auth implements RouteMesh credential precedence and macOS Keychain access.
package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/paulrberg/routemesh-cli/internal/failure"
)

const (
	EnvironmentVariable = "ROUTEMESH_API_KEY"
	KeychainService     = "routemesh-cli"
	KeychainAccount     = "ROUTEMESH_API_KEY"
	SecurityPath        = "/usr/bin/security"
)

var (
	ErrNotFound    = errors.New("keychain item not found")
	ErrUnavailable = errors.New("macOS Keychain is unavailable")
)

type Store interface {
	Available() bool
	Platform() string
	ToolPath() string
	Get(context.Context) (string, error)
	AddInteractive(context.Context, io.Reader, io.Writer, io.Writer) error
	Delete(context.Context) (bool, error)
}

type Runner interface {
	Run(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error
	Output(context.Context, string, []string) ([]byte, error)
}

type MacStoreOptions struct {
	GOOS       string
	Path       string
	Runner     Runner
	PathExists func(string) bool
}

type MacStore struct {
	goos       string
	path       string
	runner     Runner
	pathExists func(string) bool
}

func NewMacStore(options MacStoreOptions) *MacStore {
	goos := options.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	path := options.Path
	if path == "" {
		path = SecurityPath
	}
	runner := options.Runner
	if runner == nil {
		runner = execRunner{}
	}
	pathExists := options.PathExists
	if pathExists == nil {
		pathExists = func(target string) bool {
			info, err := os.Stat(target)
			return err == nil && !info.IsDir()
		}
	}
	return &MacStore{goos: goos, path: path, runner: runner, pathExists: pathExists}
}

func (s *MacStore) Available() bool {
	return s.goos == "darwin" && s.pathExists(s.path)
}

func (s *MacStore) Platform() string {
	return s.goos
}

func (s *MacStore) ToolPath() string {
	return s.path
}

func (s *MacStore) Get(ctx context.Context) (string, error) {
	if !s.Available() {
		return "", ErrUnavailable
	}
	output, err := s.runner.Output(ctx, s.path, []string{
		"find-generic-password", "-s", KeychainService, "-a", KeychainAccount, "-w",
	})
	if err != nil {
		if isNotFound(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("retrieve Keychain item: %w", err)
	}
	key := strings.TrimSpace(string(output))
	if key == "" {
		return "", fmt.Errorf("stored Keychain API key is empty")
	}
	return key, nil
}

func (s *MacStore) AddInteractive(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	if !s.Available() {
		return ErrUnavailable
	}
	args := []string{
		"add-generic-password", "-U", "-s", KeychainService, "-a", KeychainAccount, "-w",
	}
	if err := s.runner.Run(ctx, s.path, args, stdin, stdout, stderr); err != nil {
		return fmt.Errorf("store Keychain item: %w", err)
	}
	return nil
}

func (s *MacStore) Delete(ctx context.Context) (bool, error) {
	if !s.Available() {
		return false, ErrUnavailable
	}
	err := s.runner.Run(ctx, s.path, []string{
		"delete-generic-password", "-s", KeychainService, "-a", KeychainAccount,
	}, nil, io.Discard, io.Discard)
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("delete Keychain item: %w", err)
}

func Resolve(ctx context.Context, getenv func(string) string, store Store) (string, string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if key := getenv(EnvironmentVariable); key != "" {
		return key, "environment", nil
	}
	if store == nil || !store.Available() {
		return "", "", failure.New(failure.Credential, "credential_missing", "no RouteMesh API key is configured")
	}
	key, err := store.Get(ctx)
	if errors.Is(err, ErrNotFound) {
		return "", "", failure.New(failure.Credential, "credential_missing", "no RouteMesh API key is configured")
	}
	if err != nil {
		return "", "", failure.Wrap(failure.Credential, "keychain_error", "could not retrieve the RouteMesh API key from Keychain", err)
	}
	return key, "keychain", nil
}

func isNotFound(err error) bool {
	var exitErr interface{ ExitCode() int }
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 44
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func (execRunner) Output(ctx context.Context, name string, args []string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
