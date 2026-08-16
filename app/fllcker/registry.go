package fllcker

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common/net"
)

// timeNow is swappable in tests.
var timeNow = time.Now

// handle is a single live dispatched stream.
//
// Several handles may share one conn: Mux.cool and XHTTP multiplex many streams
// over a single transport connection, and each stream reaches the dispatcher
// separately.
type handle struct {
	id   uint64
	conn net.Conn
}

// ipState aggregates every live handle of one (email, ip) pair.
type ipState struct {
	// since is set once, when the IP first appears. It answers "when did this
	// address show up".
	since int64
	// lastSeen is bumped on every new stream. It answers "is this address still
	// doing anything". Kept explicit rather than derived from the handles:
	// short-lived streams die, so a derived value would age on a busy IP.
	lastSeen int64
	handles  map[uint64]*handle
}

// SessionIP is one address of one user, as reported over the API.
type SessionIP struct {
	IP       string
	Conns    int
	Since    int64
	LastSeen int64
}

// UserSessions is every address of one user.
type UserSessions struct {
	Email string
	IPs   []SessionIP
}

// addressFilter selects addresses to act on. A nil filter means all of them,
// which is what an empty address list from the API asks for.
type addressFilter map[string]struct{}

func addressSet(ips []string) addressFilter {
	if len(ips) == 0 {
		return nil
	}
	set := make(addressFilter, len(ips))
	for _, ip := range ips {
		set[ip] = struct{}{}
	}
	return set
}

func (f addressFilter) has(ip string) bool {
	if f == nil {
		return true
	}
	_, ok := f[ip]
	return ok
}

// registry tracks live sessions, keyed by email first and address second.
//
// The nesting order is not an implementation detail. It is what makes reaching
// another user's connections impossible: there is no path from an address to a
// session without naming the user first. Hundreds of our own users can sit
// behind one carrier-grade NAT address, so an operation scoped to an address
// alone would hit all of them at once. See FLK-000 in PATCHES.md.
type registry struct {
	access sync.RWMutex
	users  map[string]map[string]*ipState
}

// handleIDs is process-wide rather than per-registry so that ids are never
// reused across a Reset. A stream's release runs asynchronously and can land
// after the registry was replaced; with reused ids it would then delete an
// unrelated live handle.
var handleIDs atomic.Uint64

func newRegistry() *registry {
	return &registry{
		users: make(map[string]map[string]*ipState),
	}
}

// add registers a live stream and returns its handle id.
func (r *registry) add(email, ip string, conn net.Conn) uint64 {
	id := handleIDs.Add(1)
	now := timeNow().Unix()

	r.access.Lock()
	defer r.access.Unlock()

	ips := r.users[email]
	if ips == nil {
		ips = make(map[string]*ipState)
		r.users[email] = ips
	}
	st := ips[ip]
	if st == nil {
		st = &ipState{
			since:   now,
			handles: make(map[uint64]*handle),
		}
		ips[ip] = st
	}
	st.lastSeen = now
	st.handles[id] = &handle{id: id, conn: conn}

	return id
}

// remove drops one handle, cleaning up the address and the user once empty.
func (r *registry) remove(email, ip string, id uint64) {
	r.access.Lock()
	defer r.access.Unlock()

	ips := r.users[email]
	st := ips[ip]
	if st == nil {
		return
	}
	delete(st.handles, id)
	if len(st.handles) > 0 {
		return
	}
	delete(ips, ip)
	if len(ips) == 0 {
		delete(r.users, email)
	}
}

// addressesOf returns every address currently held by the user.
func (r *registry) addressesOf(email string) []string {
	r.access.RLock()
	defer r.access.RUnlock()

	ips := make([]string, 0, len(r.users[email]))
	for ip := range r.users[email] {
		ips = append(ips, ip)
	}
	return ips
}

// closeConns closes every connection of the user at the given addresses, or at
// all of them when ips is empty. It reports how many distinct connections were
// closed.
func (r *registry) closeConns(email string, ips []string) int {
	wanted := addressSet(ips)

	// Collect under the lock, close outside of it: Close may block, and it
	// wakes the stream's context, whose AfterFunc calls back into remove.
	var conns []net.Conn

	r.access.RLock()
	for addr, st := range r.users[email] {
		if !wanted.has(addr) {
			continue
		}
		for _, h := range st.handles {
			conns = append(conns, h.conn)
		}
	}
	r.access.RUnlock()

	// Handles sharing a conn (Mux, XHTTP) must count once. Closing twice is
	// harmless, but a doubled count would misreport how many clients were hit.
	seen := make(map[net.Conn]struct{}, len(conns))
	closed := 0
	for _, conn := range conns {
		if conn == nil {
			continue
		}
		if _, dup := seen[conn]; dup {
			continue
		}
		seen[conn] = struct{}{}
		// The error is expected and uninteresting: a connection that already
		// ended on its own is the common case.
		conn.Close()
		closed++
	}
	return closed
}

// snapshot reports live sessions for one user, or for everyone when email is
// empty.
func (r *registry) snapshot(email string) []UserSessions {
	r.access.RLock()
	defer r.access.RUnlock()

	out := make([]UserSessions, 0, len(r.users))
	for user, ips := range r.users {
		if email != "" && user != email {
			continue
		}
		us := UserSessions{Email: user, IPs: make([]SessionIP, 0, len(ips))}
		for ip, st := range ips {
			us.IPs = append(us.IPs, SessionIP{
				IP:       ip,
				Conns:    len(st.handles),
				Since:    st.since,
				LastSeen: st.lastSeen,
			})
		}
		out = append(out, us)
	}
	return out
}
