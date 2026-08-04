package auth

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/paulrberg/routemesh-cli/internal/failure"
	"golang.org/x/term"
)

// ReadAPIKeyInteractive prompts for the RouteMesh API key on stderr and reads it twice from
// stdin to guard against typos, returning the confirmed value. When stdin is a real terminal the
// input is read without echo; otherwise (piped or scripted stdin) it falls back to plain
// line-based reads.
func ReadAPIKeyInteractive(stdin io.Reader, stderr io.Writer) (string, error) {
	read := plainLineReader(stdin)
	masked := false
	if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		read = maskedLineReader(int(file.Fd()))
		masked = true
	}

	fmt.Fprint(stderr, "API key: ")
	first, err := read()
	if masked {
		fmt.Fprintln(stderr)
	}
	if err != nil {
		return "", failure.Wrap(failure.Credential, "prompt_read_error", "could not read the API key from the terminal", err)
	}
	if first == "" {
		return "", failure.New(failure.Credential, "api_key_empty", "the API key must not be empty")
	}

	fmt.Fprint(stderr, "Retype API key: ")
	second, err := read()
	if masked {
		fmt.Fprintln(stderr)
	}
	if err != nil {
		return "", failure.Wrap(failure.Credential, "prompt_read_error", "could not read the API key from the terminal", err)
	}

	if first != second {
		return "", failure.New(failure.Credential, "api_key_mismatch", "the entered API keys did not match")
	}
	return first, nil
}

func plainLineReader(stdin io.Reader) func() (string, error) {
	reader := bufio.NewReader(stdin)
	return func() (string, error) {
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
}

func maskedLineReader(fd int) func() (string, error) {
	return func() (string, error) {
		value, err := term.ReadPassword(fd)
		if err != nil {
			return "", err
		}
		return string(value), nil
	}
}
