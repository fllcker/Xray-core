package fllcker

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
)

type fakeConn struct {
	closed atomic.Bool
}

func (c *fakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *fakeConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *fakeConn) Close() error                     { c.closed.Store(true); return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

// track registers a stream the way the dispatcher hook does, and returns the
// cancel that stands in for the stream ending.
func track(email, ip string, conn net.Conn) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = session.ContextWithInbound(ctx, &session.Inbound{
		Source: net.TCPDestination(net.ParseAddress(ip), 40000),
		Conn:   conn,
	})
	Track(ctx, email)
	return cancel
}

// fakeClock freezes time and returns a function that advances it.
func fakeClock(t *testing.T) func(time.Duration) {
	t.Helper()
	var access sync.Mutex
	now := time.Now()
	timeNow = func() time.Time {
		access.Lock()
		defer access.Unlock()
		return now
	}
	t.Cleanup(func() { timeNow = time.Now })
	return func(d time.Duration) {
		access.Lock()
		now = now.Add(d)
		access.Unlock()
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

// TestKickIsolatedByUser guards FLK-000, the invariant everything else rests on:
// two users may legitimately share one address, and an operation on one of them
// must be invisible to the other.
//
// If this test is ever deleted or loosened, that is a red flag rather than
// cleanup: hundreds of users can sit behind one carrier-grade NAT address.
func TestKickIsolatedByUser(t *testing.T) {
	Reset()
	const ip = "1.1.1.1"

	victim := &fakeConn{}
	bystander := &fakeConn{}
	defer track("user1", ip, victim)()
	defer track("user2", ip, bystander)()

	closed, until := Kick("user1", []string{ip}, 5*time.Second)

	if closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
	if until == 0 {
		t.Fatal("no ban deadline returned")
	}
	if !victim.closed.Load() {
		t.Error("target connection was not closed")
	}
	if bystander.closed.Load() {
		t.Error("another user's connection at the same address was closed")
	}
	if !IsBanned("user1", ip) {
		t.Error("target was not banned")
	}
	if IsBanned("user2", ip) {
		t.Error("another user was banned at the same address")
	}
}

func TestKickWithoutBanOnlyCloses(t *testing.T) {
	Reset()
	conn := &fakeConn{}
	defer track("user1", "1.1.1.1", conn)()

	closed, until := Kick("user1", []string{"1.1.1.1"}, 0)

	if closed != 1 {
		t.Fatalf("closed = %d, want 1", closed)
	}
	if until != 0 {
		t.Errorf("banned until %d, want 0", until)
	}
	if IsBanned("user1", "1.1.1.1") {
		t.Error("ban was placed despite a zero duration")
	}
}

func TestKickWithoutAddressHitsEveryAddress(t *testing.T) {
	Reset()
	first := &fakeConn{}
	second := &fakeConn{}
	defer track("user1", "1.1.1.1", first)()
	defer track("user1", "2.2.2.2", second)()

	closed, _ := Kick("user1", nil, time.Second)

	if closed != 2 {
		t.Fatalf("closed = %d, want 2", closed)
	}
	if !first.closed.Load() || !second.closed.Load() {
		t.Error("not every address was closed")
	}
	if !IsBanned("user1", "1.1.1.1") || !IsBanned("user1", "2.2.2.2") {
		t.Error("not every address was banned")
	}
}

// TestKickSubsetKeepsTheRest covers the shape the backend actually uses: a user
// holding several addresses at once, all but the newest dropped in one call.
func TestKickSubsetKeepsTheRest(t *testing.T) {
	Reset()

	stale := []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"}
	conns := make(map[string]*fakeConn, len(stale)+1)
	for _, ip := range append(append([]string{}, stale...), "5.5.5.5") {
		conns[ip] = &fakeConn{}
		defer track("user1", ip, conns[ip])()
	}

	closed, until := Kick("user1", stale, 5*time.Second)

	if closed != len(stale) {
		t.Fatalf("closed = %d, want %d", closed, len(stale))
	}
	if until == 0 {
		t.Fatal("no ban deadline returned")
	}
	for _, ip := range stale {
		if !conns[ip].closed.Load() {
			t.Errorf("%s was left open", ip)
		}
		if !IsBanned("user1", ip) {
			t.Errorf("%s was not banned", ip)
		}
	}
	if conns["5.5.5.5"].closed.Load() {
		t.Error("the address left out of the list was closed")
	}
	if IsBanned("user1", "5.5.5.5") {
		t.Error("the address left out of the list was banned")
	}
}

// TestSharedConnCountedOnce covers Mux.cool and XHTTP, where many streams reach
// the dispatcher over one transport connection.
func TestSharedConnCountedOnce(t *testing.T) {
	Reset()
	shared := &fakeConn{}
	defer track("user1", "1.1.1.1", shared)()
	defer track("user1", "1.1.1.1", shared)()
	defer track("user1", "1.1.1.1", shared)()

	closed, _ := Kick("user1", []string{"1.1.1.1"}, 0)

	if closed != 1 {
		t.Fatalf("closed = %d, want 1: three streams share one connection", closed)
	}
}

// TestStreamEndReleasesState checks the context hook: state must disappear on
// its own when the last stream of an address ends.
func TestStreamEndReleasesState(t *testing.T) {
	Reset()
	first := track("user1", "1.1.1.1", &fakeConn{})
	second := track("user1", "1.1.1.1", &fakeConn{})

	first()
	waitFor(t, func() bool {
		got := Sessions("user1")
		return len(got) == 1 && got[0].IPs[0].Conns == 1
	})

	second()
	waitFor(t, func() bool { return len(Sessions("user1")) == 0 })
}

func TestSessionsReportBothTimestamps(t *testing.T) {
	Reset()
	advance := fakeClock(t)

	defer track("user1", "1.1.1.1", &fakeConn{})()
	advance(30 * time.Second)
	defer track("user1", "1.1.1.1", &fakeConn{})()

	got := Sessions("user1")
	if len(got) != 1 || len(got[0].IPs) != 1 {
		t.Fatalf("unexpected snapshot: %+v", got)
	}

	ip := got[0].IPs[0]
	if ip.Conns != 2 {
		t.Errorf("conns = %d, want 2", ip.Conns)
	}
	if ip.LastSeen-ip.Since != 30 {
		t.Errorf("lastSeen-since = %d, want 30: since must not move once set",
			ip.LastSeen-ip.Since)
	}
}

// TestKickBlocksReconnectThenReleases walks the sequence production runs: the
// backend drops the stale addresses of a user, the protocol hook turns away the
// handshakes that follow, and the block lifts on its own without anyone calling
// back.
//
// The hooks themselves (FLK-003 in VLESS, FLK-004 in Hysteria2) cannot be
// exercised from here, since each sits inside a protocol handshake. What this
// pins down is the contract they call into, so that a change to the ban
// registry cannot quietly alter what those hooks do.
func TestKickBlocksReconnectThenReleases(t *testing.T) {
	Reset()
	advance := fakeClock(t)

	const kept = "5.5.5.5"
	stale := []string{"1.1.1.1", "2.2.2.2"}

	for _, ip := range append(append([]string{}, stale...), kept) {
		defer track("user1", ip, &fakeConn{})()
	}

	Kick("user1", stale, 5*time.Second)

	// What the hook asks on the next handshake.
	for _, ip := range stale {
		if !IsBanned("user1", ip) {
			t.Errorf("%s would be let back in immediately", ip)
		}
	}
	if IsBanned("user1", kept) {
		t.Error("the surviving address would be turned away")
	}

	// Still blocked one second before the deadline.
	advance(4 * time.Second)
	if !IsBanned("user1", stale[0]) {
		t.Error("block lifted early")
	}

	advance(time.Second)
	for _, ip := range stale {
		if IsBanned("user1", ip) {
			t.Errorf("%s stayed blocked past its deadline", ip)
		}
	}

	// And the address is usable again: nothing lingers to reject it.
	defer track("user1", stale[0], &fakeConn{})()
	if got := len(Sessions("user1")); got != 1 {
		t.Fatalf("%d users in the registry, want 1", got)
	}
}

func TestTrackIgnoresIncompleteSessions(t *testing.T) {
	Reset()

	// No email: the dispatcher calls the hook before authentication on some
	// paths, and an empty key would pool unrelated clients together.
	track("", "1.1.1.1", &fakeConn{})

	// No conn: there is nothing to close later, so there is nothing to track.
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{
		Source: net.TCPDestination(net.ParseAddress("1.1.1.1"), 40000),
	})
	Track(ctx, "user1")

	// No inbound at all.
	Track(context.Background(), "user1")

	if got := Sessions(""); len(got) != 0 {
		t.Fatalf("registry is not empty: %+v", got)
	}
}
