// Package fllcker adds session control to Xray: it tracks which addresses are
// live for each user and can drop them, optionally blocking reconnection for a
// short while.
//
// It exists to make sharing one subscription across devices inconvenient rather
// than to punish it. A legitimate user must never notice; someone handing their
// link around gets a connection that keeps breaking.
//
// Everything is scoped to an (email, address) pair, never to an address on its
// own. See FLK-000 in PATCHES.md for why that matters.
package fllcker

import (
	"context"
	"time"

	"github.com/xtls/xray-core/common/session"
)

// The registries are package-level rather than a core.Feature. A feature would
// mean edits in core/, dependency injection into the dispatcher and config
// plumbing: a dozen lines in files this fork does not own. Keeping the diff
// against upstream small is worth more here than idiomatic wiring, because that
// diff is rebased onto every upstream release. See PLAN.md §4.3.
var (
	sessions = newRegistry()
	bans     = newBanRegistry()
)

// Track registers the dispatched stream carried by ctx and arranges for it to
// be dropped from the registry when the stream ends.
//
// The stream's context is the same lifetime signal upstream uses for its online
// map, so tracking follows connection liveness without a timer of its own.
func Track(ctx context.Context, email string) {
	if email == "" {
		return
	}
	inbound := session.InboundFromContext(ctx)
	if inbound == nil || inbound.Conn == nil || inbound.Source.Address == nil {
		return
	}

	// Same key as the upstream online map. Formatting IPv6 any other way would
	// make a ban silently fail to match its own session record.
	ip := inbound.Source.Address.String()

	id := sessions.add(email, ip, inbound.Conn)
	context.AfterFunc(ctx, func() { sessions.remove(email, ip, id) })
}

// Kick closes the user's connections at the given addresses, or at all of them
// when ips is empty, and blocks reconnection for banFor.
//
// Taking a list rather than one address is not only convenience. A user often
// holds several addresses at once, and kicking them one call at a time leaves a
// window: while the second call is in flight, the client dropped by the first
// can already be back on an address that was not in the caller's list. One call
// blocks every address before closing anything, which shrinks that window to a
// single round trip.
//
// It reports how many distinct connections were closed and the latest block
// deadline as unix nanoseconds, or 0 when banFor was not positive.
func Kick(email string, ips []string, banFor time.Duration) (closed int, bannedUntil int64) {
	if email == "" {
		return 0, 0
	}

	// Ban before closing, never after. A client reconnects in roughly the time
	// it takes to notice the drop, so the other order leaves a window that
	// makes the whole call pointless.
	if banFor > 0 {
		targets := ips
		if len(targets) == 0 {
			targets = sessions.addressesOf(email)
		}
		for _, target := range targets {
			if until := bans.ban(email, target, banFor); until > bannedUntil {
				bannedUntil = until
			}
		}
	}

	return sessions.closeConns(email, ips), bannedUntil
}

// IsBanned reports whether the pair is currently blocked. It sits on the
// authentication path of every handshake, so it returns without locking while
// nobody is banned.
func IsBanned(email, ip string) bool {
	return bans.isBanned(email, ip)
}

// BannedUntil returns the block deadline for the pair as unix nanoseconds, or 0.
func BannedUntil(email, ip string) int64 {
	return bans.until(email, ip)
}

// Unban lifts blocks early. With an empty list it lifts every block on the user.
func Unban(email string, ips []string) bool {
	return bans.unban(email, ips)
}

// Sessions reports live sessions for one user, or for everyone when email is
// empty.
func Sessions(email string) []UserSessions {
	return sessions.snapshot(email)
}

// Bans reports live blocks for one user, or for everyone when email is empty.
func Bans(email string) []Ban {
	return bans.list(email)
}

// Reset clears all state. Tests only: the registries are process-wide, so tests
// sharing a process would otherwise leak state into each other.
func Reset() {
	sessions = newRegistry()
	bans = newBanRegistry()
}
