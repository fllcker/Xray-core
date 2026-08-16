package fllcker

import (
	"testing"
	"time"
)

func TestBanExpiresOnItsOwn(t *testing.T) {
	Reset()
	advance := fakeClock(t)

	bans.ban("user1", "1.1.1.1", 5*time.Second)
	if !IsBanned("user1", "1.1.1.1") {
		t.Fatal("ban did not take effect")
	}

	advance(5 * time.Second)
	if IsBanned("user1", "1.1.1.1") {
		t.Error("ban outlived its deadline")
	}
}

// TestBanIsNotShortened matters because the backend may kick the same pair
// repeatedly while a longer block is already running.
func TestBanIsNotShortened(t *testing.T) {
	Reset()
	advance := fakeClock(t)

	long := bans.ban("user1", "1.1.1.1", time.Minute)
	short := bans.ban("user1", "1.1.1.1", time.Second)

	if short != long {
		t.Errorf("deadline moved from %d to %d", long, short)
	}

	advance(2 * time.Second)
	if !IsBanned("user1", "1.1.1.1") {
		t.Error("the longer ban was cut short by a shorter one")
	}
}

// TestEmptyEmailIsNeverBanned guards the Hysteria2 hook (FLK-004). There,
// inbound.User starts as an empty MemoryUser and stays that way when the user is
// not recognised, so an empty key must never match anything.
func TestEmptyEmailIsNeverBanned(t *testing.T) {
	Reset()

	if until := bans.ban("", "1.1.1.1", time.Minute); until != 0 {
		t.Errorf("an empty email was banned, deadline %d", until)
	}
	if IsBanned("", "1.1.1.1") {
		t.Error("an empty email matched a ban")
	}

	bans.ban("user1", "1.1.1.1", time.Minute)
	if IsBanned("", "1.1.1.1") {
		t.Error("an empty email matched another user's ban at the same address")
	}
}

func TestUnban(t *testing.T) {
	Reset()

	bans.ban("user1", "1.1.1.1", time.Minute)
	bans.ban("user1", "2.2.2.2", time.Minute)

	if !Unban("user1", []string{"1.1.1.1"}) {
		t.Error("Unban reported no ban where one existed")
	}
	if IsBanned("user1", "1.1.1.1") {
		t.Error("ban survived Unban")
	}
	if !IsBanned("user1", "2.2.2.2") {
		t.Error("Unban hit an address it was not given")
	}

	if !Unban("user1", nil) {
		t.Error("Unban with an empty list reported nothing to lift")
	}
	if IsBanned("user1", "2.2.2.2") {
		t.Error("Unban with an empty address left a ban behind")
	}
	if got := bans.active.Load(); got != 0 {
		t.Errorf("active = %d after lifting everything, want 0", got)
	}
}

// TestIsBannedSkipsLockWhenIdle pins the fast path down rather than trusting it:
// IsBanned runs on every VLESS and Hysteria2 handshake, so it must not contend
// on the mutex while nobody is banned.
//
// Holding the write lock makes the point directly. If IsBanned reached for the
// lock, it would block here.
func TestIsBannedSkipsLockWhenIdle(t *testing.T) {
	Reset()

	bans.access.Lock()
	defer bans.access.Unlock()

	done := make(chan bool, 1)
	go func() { done <- IsBanned("user1", "1.1.1.1") }()

	select {
	case banned := <-done:
		if banned {
			t.Error("reported a ban on an empty registry")
		}
	case <-time.After(time.Second):
		t.Fatal("IsBanned blocked on the lock while no bans were placed")
	}
}

func TestSweepDropsExpiredEntries(t *testing.T) {
	Reset()
	advance := fakeClock(t)

	bans.ban("user1", "1.1.1.1", time.Second)
	if got := bans.active.Load(); got != 1 {
		t.Fatalf("active = %d, want 1", got)
	}

	// Sweeping is inline and rate limited, so it takes a later ban to trigger.
	advance(sweepInterval + time.Second)
	bans.ban("user2", "2.2.2.2", time.Minute)

	if got := bans.active.Load(); got != 1 {
		t.Errorf("active = %d, want 1: the expired entry should be gone", got)
	}
	if got := Bans(""); len(got) != 1 || got[0].Email != "user2" {
		t.Errorf("unexpected ban list: %+v", got)
	}
}

func TestBansListsOnlyLiveEntries(t *testing.T) {
	Reset()
	advance := fakeClock(t)

	bans.ban("user1", "1.1.1.1", 5*time.Second)
	bans.ban("user2", "2.2.2.2", time.Minute)

	advance(10 * time.Second)

	got := Bans("")
	if len(got) != 1 || got[0].Email != "user2" {
		t.Errorf("expired entry leaked into the list: %+v", got)
	}
	if got := Bans("user1"); len(got) != 0 {
		t.Errorf("expired entry returned for its own user: %+v", got)
	}
}
