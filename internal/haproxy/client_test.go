package haproxy

import (
	"bufio"
	"context"
	"errors"
	"net"
	"path/filepath"
	"testing"
)

func fakeSocket(t *testing.T, responses map[string]string) (string, chan string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "haproxy.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })

	received := make(chan string, 16)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			command, _ := bufio.NewReader(conn).ReadString('\n')
			command = command[:len(command)-1]
			received <- command
			_, _ = conn.Write([]byte(responses[command]))
			conn.Close()
		}
	}()
	return path, received
}

func TestApplyStopsAtFirstRejectedCommand(t *testing.T) {
	socket, received := fakeSocket(t, map[string]string{
		"set server be_checkout/s1 addr 10.0.0.3 port 80": "IP changed from '127.0.0.1' to '10.0.0.3' by 'stats socket command'\n\n",
		"set server be_checkout/s9 weight 100":            "No such server.\n\n",
	})
	client := Client{AdminSocket: socket}

	err := client.Apply(context.Background(), []string{
		"set server be_checkout/s1 addr 10.0.0.3 port 80",
		"set server be_checkout/s9 weight 100",
		"set server be_checkout/s9 state ready",
	})

	if err == nil {
		t.Fatal("expected the unknown server to fail the apply")
	}
	if len(received) != 2 {
		t.Fatalf("sent %d commands, want to stop after the failure at 2", len(received))
	}
}

func TestReloadRequiresSuccess(t *testing.T) {
	socket, _ := fakeSocket(t, map[string]string{
		"reload": "Success=0\n--\n[ALERT] config parsing failed\n",
	})

	if err := (Client{MasterSocket: socket}).Reload(context.Background()); err == nil {
		t.Fatal("expected Success=0 to be reported as a failed reload")
	}
}

func TestMissingSocketMeansNotRunning(t *testing.T) {
	client := Client{MasterSocket: filepath.Join(t.TempDir(), "missing.sock")}

	if err := client.Reload(context.Background()); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
}
