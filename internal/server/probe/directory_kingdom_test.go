package probe

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"

	"qqtang/internal/protocol/directory"
	"qqtang/internal/protocol/game"
)

func kingdomTestServer(t *testing.T) *Server {
	t.Helper()
	config := directory.DefaultLocalHallNativeConfig()
	config.ServerPort = 58000
	payload, err := directory.BuildLocalKingdomPayload(config)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{authenticationState: newAuthenticationState(), logWriter: io.Discard,
		directoryKingdomPayload: payload, directoryHallPayload: []byte{0, 0}}
}

func kingdomRequest(t *testing.T, uin uint32, section uint16) []byte {
	t.Helper()
	payload := make([]byte, 8)
	binary.BigEndian.PutUint32(payload, uin)
	binary.BigEndian.PutUint32(payload[4:], 0x21955398)
	return testLocalRoutedPacketWithPayload(t, directory.KingdomCommand, 6, 0xFFFF, section, uin, payload)
}

func TestNativeKingdomRequestUsesBoundNavigationWithoutGameLogin(t *testing.T) {
	for _, section := range []uint16{0xFD, 0xFE} {
		const uin uint32 = 1000007
		server := kingdomTestServer(t)
		remote := "192.0.2.10:12345"
		server.grantAccountAuth(remote, uin)
		session := &connectionSession{remoteAddress: remote, connectionID: "native-directory"}
		config := ListenerConfig{Response: ResponseConfig{QQTDirectoryResponse: true, QQTLoginSuccess: true}}
		hall := server.dispatchTCPMessage(config, session, session.connectionID, "127.0.0.1:58000", remote,
			testLocalRoutedPacket(t, localDirectoryRequestCommand, 6, 0xFFFF, section, uin))
		if !hall.keepConnection || len(hall.response) == 0 || session.authNavigationUIN.Load() != uin {
			t.Fatalf("hall did not bind the verified directory capability: %+v", hall)
		}
		packet := kingdomRequest(t, uin, section)
		outcome := server.dispatchTCPMessage(config, session, session.connectionID, "127.0.0.1:58000", remote, packet)
		if !outcome.keepConnection || outcome.result != "qqt_directory_kingdom_response" || session.UIN != 0 || session.liveUIN.Load() != 0 {
			t.Fatalf("kingdom navigation changed game authentication or closed: %+v", outcome)
		}
		decoded, err := game.InspectLocalPacket(outcome.response)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Command != 0x131 || decoded.EnvelopeUIN != uin || decoded.SectionID != section || !bytes.Equal(decoded.Payload, server.directoryKingdomPayload) {
			t.Fatalf("wrong correlated directory catalog: %+v", decoded)
		}
		// Directory access must neither become game authentication nor consume
		// the separate one-time grant used when selecting a district.
		logout := make([]byte, 9)
		binary.BigEndian.PutUint32(logout, uin)
		business := testLocalRoutedPacketWithPayload(t, game.LogoutCommand, 2, 0xFFFF, 1, uin, logout)
		denied := server.dispatchTCPMessage(config, session, "business", "127.0.0.1:58000", remote, business)
		if denied.keepConnection || len(denied.response) != 0 {
			t.Fatal("navigation capability allowed game business before REQUEST_LOGIN")
		}
		if !server.consumeAccountAuth(remote, uin) || server.consumeAccountAuth(remote, uin) {
			t.Fatal("kingdom request consumed or duplicated the one-time district grant")
		}
	}
}

func TestKingdomDoesNotCreateAnUnauthenticatedCommandException(t *testing.T) {
	for _, mode := range []string{"unbound", "other-navigation", "stale-game", "auxiliary", "foreign-envelope", "game-owner", "navigation-owner"} {
		t.Run(mode, func(t *testing.T) {
			const uin uint32 = 1000007
			server := kingdomTestServer(t)
			session := &connectionSession{}
			packetUIN := uin
			switch mode {
			case "other-navigation":
				session.authNavigationUIN.Store(uin + 1)
			case "stale-game":
				session.UIN = uin
			case "auxiliary":
				session.setUIN(uin)
				session.auxiliary = true
			case "foreign-envelope":
				session.setUIN(uin)
				packetUIN++
			case "game-owner":
				session.setUIN(uin)
			case "navigation-owner":
				session.authNavigationUIN.Store(uin)
			}
			outcome := server.dispatchTCPMessage(ListenerConfig{Response: ResponseConfig{QQTDirectoryResponse: true, QQTLoginSuccess: true}},
				session, mode, "127.0.0.1:58000", "192.0.2.10:12345", kingdomRequest(t, packetUIN, 0xFE))
			allowed := mode == "game-owner" || mode == "navigation-owner"
			if allowed != (len(outcome.response) != 0 && outcome.keepConnection) {
				t.Fatalf("%s got %+v", mode, outcome)
			}
			if !allowed && outcome.keepConnection {
				t.Fatal("unbound kingdom request left the game socket authorized")
			}
		})
	}
}

func TestMalformedKingdomRouteStillPassesThroughBusinessBoundary(t *testing.T) {
	for _, fields := range [][3]uint16{{2, 0xFFFF, 0xFE}, {6, 0, 0xFE}, {6, 0xFFFF, 1}} {
		server := kingdomTestServer(t)
		session := &connectionSession{}
		session.authNavigationUIN.Store(1000007)
		payload := make([]byte, 8)
		binary.BigEndian.PutUint32(payload, 1000007)
		packet := testLocalRoutedPacketWithPayload(t, 0x131, fields[0], fields[1], fields[2], 1000007, payload)
		outcome := server.dispatchTCPMessage(ListenerConfig{Response: ResponseConfig{QQTDirectoryResponse: true, QQTLoginSuccess: true}},
			session, "lookalike", "127.0.0.1:58000", "192.0.2.10:12345", packet)
		if outcome.keepConnection || len(outcome.response) != 0 {
			t.Fatalf("directory capability accepted wrong route: %v %+v", fields, outcome)
		}
	}
}

func TestLogoutNavigationRebindsBeforeKingdomRefresh(t *testing.T) {
	const uin uint32 = 1000007
	server := kingdomTestServer(t)
	remote := "192.0.2.10:12345"
	old := &connectionSession{Profile: game.DefaultPlayerProfile(), remoteAddress: remote}
	old.setUIN(uin)
	if err := server.logoutSession(old, "logout"); err != nil {
		t.Fatal(err)
	}
	if server.attachAccountAuthNavigation(&connectionSession{}, "192.0.2.11:23456", uin) ||
		server.attachAccountAuthNavigation(&connectionSession{}, remote, uin+1) {
		t.Fatal("retained navigation escaped its host/UIN")
	}
	fresh := &connectionSession{remoteAddress: remote}
	if !server.attachAccountAuthNavigation(fresh, remote, uin) || fresh.UIN != 0 {
		t.Fatal("logout navigation did not bind to the new selector independently of game login")
	}
	outcome := server.dispatchTCPMessage(ListenerConfig{Response: ResponseConfig{QQTDirectoryResponse: true, QQTLoginSuccess: true}},
		fresh, "new-selector", "127.0.0.1:58000", remote, kingdomRequest(t, uin, 0xFE))
	if !outcome.keepConnection || outcome.result != "qqt_directory_kingdom_response" {
		t.Fatalf("post-logout kingdom request was rejected: %+v", outcome)
	}
	if !server.consumeAccountAuth(remote, uin) || server.consumeAccountAuth(remote, uin) {
		t.Fatal("selector refresh changed the one-time login grant")
	}
}

func TestNavigationRebindRevokesPreviousLiveSelector(t *testing.T) {
	const uin uint32 = 1000007
	server := kingdomTestServer(t)
	remote := "192.0.2.10:12345"
	one, two := net.Pipe()
	defer one.Close()
	defer two.Close()
	first, second := &connectionSession{remoteAddress: remote}, &connectionSession{remoteAddress: remote}
	server.liveSessions = map[net.Conn]*connectionSession{one: first, two: second}
	server.grantAccountAuth(remote, uin)
	if !server.attachAccountAuthNavigation(first, remote, uin) || !server.attachAccountAuthNavigation(second, remote, uin) {
		t.Fatal("valid selector capability did not transfer")
	}
	if first.authNavigationUIN.Load() != 0 || second.authNavigationUIN.Load() != uin {
		t.Fatal("navigation transfer left two live selector owners")
	}
	if !server.consumeAccountAuth(remote, uin) || second.authNavigationUIN.Load() != 0 || server.consumeAccountAuth(remote, uin) {
		t.Fatal("game login did not consume both cached and live navigation ownership")
	}
}
