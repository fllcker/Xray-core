package fllcker

import (
	"sync"
	"sync/atomic"
	"time"
)

// sweepInterval bounds how often expired bans are collected. Sweeping runs
// inline on a ban, at most this often, so there is no background goroutine and
// no O(n) walk per call during a burst of kicks.
const sweepInterval = 10 * time.Second

// Ban is one temporary block, as reported over the API.
type Ban struct {
	Email string
	IP    string
	Until int64 // unix nano
}

// banRegistry holds short-lived blocks on (email, address) pairs.
//
// Keyed by email first, exactly like the session registry, and for the same
// reason: a block must never apply to an address on its own. See FLK-000 in
// PATCHES.md.
//
// Blocks are in-memory only. They are measured in seconds, so surviving a
// restart would be meaningless.
type banRegistry struct {
	access sync.RWMutex
	bans   map[string]map[string]int64 // email -> ip -> unban deadline, unix nano

	// active counts stored entries, expired ones included. It exists only so
	// that isBanned can return without touching the lock while nobody is
	// banned, which is the overwhelmingly common case: isBanned sits on the
	// authentication path of every handshake.
	active    atomic.Int64
	lastSweep atomic.Int64
}

func newBanRegistry() *banRegistry {
	return &banRegistry{
		bans: make(map[string]map[string]int64),
	}
}

// ban blocks the pair until now+duration and returns the deadline. An existing
// longer block is never shortened.
func (b *banRegistry) ban(email, ip string, duration time.Duration) int64 {
	if email == "" || ip == "" || duration <= 0 {
		return 0
	}
	now := timeNow()
	until := now.Add(duration).UnixNano()

	b.access.Lock()
	ips := b.bans[email]
	if ips == nil {
		ips = make(map[string]int64)
		b.bans[email] = ips
	}
	current, exists := ips[ip]
	if !exists {
		b.active.Add(1)
	}
	if !exists || current < until {
		ips[ip] = until
	} else {
		until = current
	}
	b.sweepLocked(now.UnixNano())
	b.access.Unlock()

	return until
}

// isBanned reports whether the pair is currently blocked.
//
// Hot path: called on every VLESS and Hysteria2 handshake.
func (b *banRegistry) isBanned(email, ip string) bool {
	if email == "" || ip == "" || b.active.Load() == 0 {
		return false
	}

	b.access.RLock()
	until, ok := b.bans[email][ip]
	b.access.RUnlock()

	// An expired entry may still be stored, waiting for the next sweep. Compare
	// against the clock rather than trusting its presence.
	return ok && timeNow().UnixNano() < until
}

// until returns the deadline for the pair, or 0 when it is not blocked.
func (b *banRegistry) until(email, ip string) int64 {
	if email == "" || ip == "" || b.active.Load() == 0 {
		return 0
	}

	b.access.RLock()
	deadline, ok := b.bans[email][ip]
	b.access.RUnlock()

	if !ok || timeNow().UnixNano() >= deadline {
		return 0
	}
	return deadline
}

// unban lifts blocks early and reports whether at least one was in place. An
// empty address list lifts every block on the user.
func (b *banRegistry) unban(email string, ips []string) bool {
	if email == "" {
		return false
	}

	b.access.Lock()
	defer b.access.Unlock()

	stored := b.bans[email]
	if stored == nil {
		return false
	}

	if len(ips) == 0 {
		b.active.Add(-int64(len(stored)))
		delete(b.bans, email)
		return true
	}

	lifted := false
	for _, ip := range ips {
		if _, ok := stored[ip]; !ok {
			continue
		}
		delete(stored, ip)
		b.active.Add(-1)
		lifted = true
	}
	if len(stored) == 0 {
		delete(b.bans, email)
	}
	return lifted
}

// list reports live blocks for one user, or for everyone when email is empty.
func (b *banRegistry) list(email string) []Ban {
	if b.active.Load() == 0 {
		return nil
	}
	now := timeNow().UnixNano()

	b.access.RLock()
	defer b.access.RUnlock()

	var out []Ban
	for user, ips := range b.bans {
		if email != "" && user != email {
			continue
		}
		for ip, until := range ips {
			if until <= now {
				continue
			}
			out = append(out, Ban{Email: user, IP: ip, Until: until})
		}
	}
	return out
}

// sweepLocked drops expired entries. The caller must hold the write lock.
func (b *banRegistry) sweepLocked(now int64) {
	last := b.lastSweep.Load()
	if now-last < int64(sweepInterval) {
		return
	}
	b.lastSweep.Store(now)

	for email, ips := range b.bans {
		for ip, until := range ips {
			if until > now {
				continue
			}
			delete(ips, ip)
			b.active.Add(-1)
		}
		if len(ips) == 0 {
			delete(b.bans, email)
		}
	}
}
