package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testMCPToken(id, actor string, now time.Time) MCPToken {
	return MCPToken{ID: id, ActorID: actor, Label: "laptop", Hint: "abcd", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
}

func TestMCPTokenStoresOnlyTheDigestAndExpiresAndRevokes(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	raw := MCPTokenPrefix + strings.Repeat("x", 43)
	if err := s.CreateMCPToken(testMCPToken("id1", "qa@example.test", now), raw); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT token_hash FROM mcp_tokens WHERE id='id1'`).Scan(&stored); err != nil || stored == raw || stored != MCPTokenDigest(raw) {
		t.Fatalf("the secret must not be stored: %q %v", stored, err)
	}
	got, err := s.MCPTokenByRaw(raw, now.Add(time.Hour))
	if err != nil || got.ActorID != "qa@example.test" || got.ID != "id1" {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	for name, probe := range map[string]string{"wrong": MCPTokenPrefix + "nope", "no prefix": "xxxx", "empty": ""} {
		if _, err = s.MCPTokenByRaw(probe, now); !errors.Is(err, ErrMCPTokenNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err = s.MCPTokenByRaw(raw, now.Add(25*time.Hour)); !errors.Is(err, ErrMCPTokenNotFound) {
		t.Fatalf("expired token: %v", err)
	}
	if err = s.RevokeMCPToken("someone-else@example.test", "id1", now); !errors.Is(err, ErrMCPTokenNotFound) {
		t.Fatalf("another owner must not revoke: %v", err)
	}
	if _, err = s.MCPTokenByRaw(raw, now); err != nil {
		t.Fatalf("a refused revoke must leave the token live: %v", err)
	}
	if err = s.RevokeMCPToken("qa@example.test", "id1", now); err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeMCPToken("qa@example.test", "id1", now); err != nil {
		t.Fatalf("revoking twice is a no-op: %v", err)
	}
	if _, err = s.MCPTokenByRaw(raw, now); !errors.Is(err, ErrMCPTokenNotFound) {
		t.Fatalf("revoked token: %v", err)
	}
	list, err := s.ListMCPTokens("qa@example.test")
	if err != nil || len(list) != 1 || list[0].RevokedAt == nil || list[0].LastUsedAt == nil {
		t.Fatalf("list: %+v %v", list, err)
	}
	audit, err := s.ListTeamAudit(10)
	if err != nil || len(audit) != 2 || audit[0].Action != "mcp_token_revoked" || audit[1].Action != "mcp_token_issued" {
		t.Fatalf("audit: %+v %v", audit, err)
	}
}

func TestMCPTokenCapCountsOnlyLiveTokens(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for i := 0; i < MaxActiveMCPTokens; i++ {
		raw := MCPTokenPrefix + strings.Repeat(string(rune('a'+i)), 43)
		if err := s.CreateMCPToken(testMCPToken(string(rune('a'+i)), "dev@example.test", now), raw); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if err := s.CreateMCPToken(testMCPToken("over", "dev@example.test", now), MCPTokenPrefix+"over"); !errors.Is(err, ErrTooManyMCPTokens) {
		t.Fatalf("cap: %v", err)
	}
	if err := s.CreateMCPToken(testMCPToken("other", "other@example.test", now), MCPTokenPrefix+"other"); err != nil {
		t.Fatalf("the cap is per actor: %v", err)
	}
	if err := s.RevokeMCPToken("dev@example.test", "a", now); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateMCPToken(testMCPToken("again", "dev@example.test", now), MCPTokenPrefix+"again"); err != nil {
		t.Fatalf("a revoked token frees a slot: %v", err)
	}
	if err := s.CreateMCPToken(MCPToken{ID: "bad", ActorID: "dev@example.test", CreatedAt: now, ExpiresAt: now}, MCPTokenPrefix+"bad"); err == nil {
		t.Fatal("a token that is already expired must be rejected")
	}
	if err := s.CreateMCPToken(testMCPToken("noprefix", "dev@example.test", now), "plain"); err == nil {
		t.Fatal("a secret without the aitm_ prefix must be rejected")
	}
}
