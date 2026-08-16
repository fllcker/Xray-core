package command

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/xtls/xray-core/app/fllcker"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type fakeConn struct{ closed chan struct{} }

func newFakeConn() *fakeConn { return &fakeConn{closed: make(chan struct{})} }

func (c *fakeConn) Read([]byte) (int, error)    { return 0, net.ErrClosed }
func (c *fakeConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *fakeConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}
func (c *fakeConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}
func (c *fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

// dial brings up the service over a real gRPC connection, so the test exercises
// registration, serialization and the handlers the same way the API port does.
func dial(t *testing.T) FllckerServiceClient {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	(&service{}).Register(server)

	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return NewFllckerServiceClient(conn)
}

func track(t *testing.T, email, ip string, conn net.Conn) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ctx = session.ContextWithInbound(ctx, &session.Inbound{
		Source: xnet.TCPDestination(xnet.ParseAddress(ip), 40000),
		Conn:   conn,
	})
	fllcker.Track(ctx, email)
}

func TestKickOverGRPC(t *testing.T) {
	fllcker.Reset()
	client := dial(t)
	ctx := context.Background()

	stale := newFakeConn()
	kept := newFakeConn()
	track(t, "user1", "1.1.1.1", stale)
	track(t, "user1", "2.2.2.2", kept)

	response, err := client.Kick(ctx, &KickRequest{
		Email:      "user1",
		Ips:        []string{"1.1.1.1"},
		BanSeconds: 5,
	})
	if err != nil {
		t.Fatalf("Kick: %v", err)
	}

	if response.Closed != 1 {
		t.Errorf("closed = %d, want 1", response.Closed)
	}
	if response.BannedUntil <= time.Now().Unix() {
		t.Errorf("banned_until = %d is not in the future", response.BannedUntil)
	}
	if !stale.isClosed() {
		t.Error("the named address was left open")
	}
	if kept.isClosed() {
		t.Error("an address that was not named got closed")
	}
}

// TestKickRequiresEmail guards FLK-000 at the API boundary: without a user the
// call would be scoped to addresses alone, and one address can carry hundreds
// of unrelated users behind a carrier-grade NAT.
func TestKickRequiresEmail(t *testing.T) {
	fllcker.Reset()
	client := dial(t)

	_, err := client.Kick(context.Background(), &KickRequest{Ips: []string{"1.1.1.1"}, BanSeconds: 5})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error = %v, want InvalidArgument", err)
	}

	_, err = client.Unban(context.Background(), &UnbanRequest{Ips: []string{"1.1.1.1"}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Unban error = %v, want InvalidArgument", err)
	}
}

func TestListSessionsOverGRPC(t *testing.T) {
	fllcker.Reset()
	client := dial(t)
	ctx := context.Background()

	track(t, "user1", "1.1.1.1", newFakeConn())
	track(t, "user1", "1.1.1.1", newFakeConn())
	track(t, "user2", "3.3.3.3", newFakeConn())

	if _, err := client.Kick(ctx, &KickRequest{Email: "user1", Ips: []string{"1.1.1.1"}, BanSeconds: 30}); err != nil {
		t.Fatalf("Kick: %v", err)
	}

	response, err := client.ListSessions(ctx, &ListSessionsRequest{Email: "user1"})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(response.Users) != 1 {
		t.Fatalf("%d users returned for a single-user query", len(response.Users))
	}

	user := response.Users[0]
	if user.Email != "user1" || len(user.Ips) != 1 {
		t.Fatalf("unexpected user entry: %+v", user)
	}
	ip := user.Ips[0]
	if ip.Conns != 2 {
		t.Errorf("conns = %d, want 2", ip.Conns)
	}
	if ip.Since == 0 || ip.LastSeen == 0 {
		t.Errorf("timestamps missing: since=%d last_seen=%d", ip.Since, ip.LastSeen)
	}
	if ip.BannedUntil <= time.Now().Unix() {
		t.Errorf("banned_until = %d is not in the future", ip.BannedUntil)
	}

	bans, err := client.ListBans(ctx, &ListBansRequest{})
	if err != nil {
		t.Fatalf("ListBans: %v", err)
	}
	if len(bans.Bans) != 1 || bans.Bans[0].Email != "user1" {
		t.Fatalf("unexpected ban list: %+v", bans.Bans)
	}

	lifted, err := client.Unban(ctx, &UnbanRequest{Email: "user1"})
	if err != nil {
		t.Fatalf("Unban: %v", err)
	}
	if !lifted.Lifted {
		t.Error("Unban reported nothing to lift")
	}
}

func TestUnixSecondsRoundsUp(t *testing.T) {
	if got := unixSeconds(0); got != 0 {
		t.Errorf("unixSeconds(0) = %d, want 0", got)
	}
	// One nanosecond past a whole second must still report the next second, so
	// a caller waiting for the deadline never returns while the block is live.
	if got := unixSeconds(int64(time.Second) + 1); got != 2 {
		t.Errorf("unixSeconds(1s+1ns) = %d, want 2", got)
	}
	if got := unixSeconds(int64(time.Second)); got != 1 {
		t.Errorf("unixSeconds(1s) = %d, want 1", got)
	}
}
