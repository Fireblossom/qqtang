package probe

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qqtang/internal/protocol/game"
)

func TestLogoutThenSocketResetIsInformational(t *testing.T) {
	for _, phase := range []string{"active", "logged-out", "logged-in-again"} {
		t.Run(phase, func(t *testing.T) {
			const uin uint32 = 1000007
			logs := filepath.Join(t.TempDir(), "server.jsonl")
			server, err := New(Config{CaptureRoot: t.TempDir(), LogFile: logs, SeedUIN: uin,
				MaxPacketSize: 4096, IdleTimeoutMS: 5000,
				Listeners: []ListenerConfig{{Name: "logout-reset", Network: "tcp", Address: "127.0.0.1:0",
					Response: ResponseConfig{QQTLoginSuccess: true}}}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer func() { cancel(); _ = server.Wait() }()
			if err := server.Start(ctx); err != nil {
				t.Fatal(err)
			}
			address, err := net.ResolveTCPAddr("tcp4", server.Addresses()[0].Address)
			if err != nil {
				t.Fatal(err)
			}
			connection, err := net.DialTCP("tcp4", nil, address)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			read := func(command uint16) {
				t.Helper()
				var header [4]byte
				if _, err := io.ReadFull(connection, header[:]); err != nil {
					t.Fatal(err)
				}
				size := binary.BigEndian.Uint32(header[:])
				if size < 50 || size > 1<<20 {
					t.Fatalf("unexpected reply size %d", size)
				}
				packet := make([]byte, size)
				copy(packet, header[:])
				if _, err := io.ReadFull(connection, packet[4:]); err != nil {
					t.Fatal(err)
				}
				decoded, err := game.InspectLocalPacket(packet)
				if err != nil || decoded.Command != command {
					t.Fatalf("reply command 0x%x, want 0x%x: %v", decoded.Command, command, err)
				}
			}
			login := func() {
				t.Helper()
				server.grantAccountAuth(connection.LocalAddr().String(), uin)
				payload := make([]byte, 53)
				binary.BigEndian.PutUint32(payload, uin)
				copy(payload[8:28], "reset-test")
				binary.BigEndian.PutUint16(payload[32:34], 1)
				payload[38] = 1
				packet := testLocalRoutedPacketWithPayload(t, game.LoginCommand, 2, 0xFFFF, 1, uin, payload)
				if _, err := connection.Write(packet); err != nil {
					t.Fatal(err)
				}
				read(game.LoginCommand)
			}
			login()
			if phase != "active" {
				payload := make([]byte, 9)
				binary.BigEndian.PutUint32(payload, uin)
				if _, err := connection.Write(testLocalRoutedPacketWithPayload(t, game.LogoutCommand, 2, 0xFFFF, 1, uin, payload)); err != nil {
					t.Fatal(err)
				}
				read(game.LogoutCommand)
			}
			if phase == "logged-in-again" {
				login()
			}
			if err := connection.SetLinger(0); err != nil {
				t.Fatal(err)
			}
			if err := connection.Close(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			var reset *logEvent
			for reset == nil && time.Now().Before(deadline) {
				data, _ := os.ReadFile(logs)
				for _, line := range strings.Split(string(data), "\n") {
					var event logEvent
					if json.Unmarshal([]byte(line), &event) == nil &&
						(event.Event == "connection_reset_after_logout" || event.Event == "tcp_read_failed") {
						reset = &event
					}
				}
				if reset == nil {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if reset == nil {
				t.Fatal("TCP reset did not reach the server read loop")
			}
			if phase == "logged-out" {
				if reset.Level != "info" || reset.Event != "connection_reset_after_logout" {
					t.Fatalf("completed logout reset misclassified: %+v", reset)
				}
			} else if reset.Level != "error" || reset.Event != "tcp_read_failed" {
				t.Fatalf("active connection failure was hidden after %s: %+v", phase, reset)
			}
		})
	}
}
