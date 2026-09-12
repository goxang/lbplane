package haproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

var ErrNotRunning = errors.New("haproxy is not running")

type Client struct {
	AdminSocket  string
	MasterSocket string
	Binary       string
	Timeout      time.Duration
}

func (c Client) Check(ctx context.Context, configPath string) error {
	output, err := exec.CommandContext(ctx, c.Binary, "-c", "-f", configPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("haproxy -c: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (c Client) Apply(ctx context.Context, commands []string) error {
	for _, command := range commands {
		response, err := c.send(ctx, c.AdminSocket, command)
		if err != nil {
			return err
		}
		if !runtimeSucceeded(response) {
			return fmt.Errorf("%q: %s", command, strings.TrimSpace(response))
		}
	}
	return nil
}

func (c Client) Reload(ctx context.Context) error {
	response, err := c.send(ctx, c.MasterSocket, "reload")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(response, "Success=1") {
		return fmt.Errorf("reload failed: %s", strings.TrimSpace(response))
	}
	return nil
}

// Addr updates answer "IP changed ..." instead of an empty line.
func runtimeSucceeded(response string) bool {
	response = strings.TrimSpace(response)
	return response == "" || strings.Contains(response, "changed")
}

func (c Client) send(ctx context.Context, socket, command string) (string, error) {
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return "", fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	if err != nil {
		return "", err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := io.WriteString(conn, command+"\n"); err != nil {
		return "", err
	}
	response, err := io.ReadAll(conn)
	if err != nil {
		return "", fmt.Errorf("reading response to %q: %w", command, err)
	}
	return string(response), nil
}
