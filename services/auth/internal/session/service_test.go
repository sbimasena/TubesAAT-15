package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/auth/internal/identity"
	"github.com/sbimasena/TubesAAT-15/services/auth/internal/token"
)

type testIdentities struct{}

func (testIdentities) Verify(_ context.Context, id, password string) (identity.Identity, error) {
	if id != "field-team" || password != "test-only-password" {
		return identity.Identity{}, identity.ErrCredentials
	}
	return identity.Identity{ID: id, Scope: "field-team"}, nil
}

func newService(t *testing.T) *Service {
	t.Helper()
	issuer, err := token.New([]byte(strings.Repeat("test-only-key", 3)), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return New(testIdentities{}, issuer, time.Minute, time.Hour)
}

func login(t *testing.T, service *Service) Pair {
	t.Helper()
	pair, err := service.Login(context.Background(), "field-team", "test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func TestRefreshRotatesAndImmediatelyRejectsOldAccessAndRefresh(t *testing.T) {
	service := newService(t)
	old := login(t, service)
	if _, err := service.Inspect(context.Background(), old.AccessToken); err != nil {
		t.Fatal(err)
	}
	next, err := service.Refresh(context.Background(), old.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if next.AccessToken == old.AccessToken || next.RefreshToken == old.RefreshToken {
		t.Fatal("tokens did not rotate")
	}
	if _, err := service.Inspect(context.Background(), old.AccessToken); !errors.Is(err, ErrToken) {
		t.Fatal("old unexpired access accepted")
	}
	claims, err := service.Inspect(context.Background(), next.AccessToken)
	if err != nil || claims.Generation != 2 {
		t.Fatalf("new token rejected: %v", err)
	}
	if _, err := service.Refresh(context.Background(), old.RefreshToken); !errors.Is(err, ErrToken) {
		t.Fatal("refresh replay accepted")
	}
	if _, err := service.Inspect(context.Background(), next.AccessToken); err != nil {
		t.Fatal("replay invalidated winner")
	}
}

func TestConcurrentRefreshHasExactlyOneWinner(t *testing.T) {
	service := newService(t)
	old := login(t, service)
	var winners atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := service.Refresh(context.Background(), old.RefreshToken); err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrToken) {
				t.Errorf("unexpected refresh failure: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("expected one winner, got %d", winners.Load())
	}
}

type failingIssuer struct {
	tokenIssuer
	fail bool
}

func (issuer *failingIssuer) Issue(id, scope, sid string, generation uint64) (string, error) {
	if issuer.fail {
		return "", errors.New("test signing failure")
	}
	return issuer.tokenIssuer.Issue(id, scope, sid, generation)
}

func TestSigningFailureAndCancellationDoNotConsumeRefresh(t *testing.T) {
	service := newService(t)
	issuer := &failingIssuer{tokenIssuer: service.issuer}
	service.issuer = issuer
	old := login(t, service)
	issuer.fail = true
	if _, err := service.Refresh(context.Background(), old.RefreshToken); err == nil {
		t.Fatal("signing failure ignored")
	}
	if _, err := service.Inspect(context.Background(), old.AccessToken); err != nil {
		t.Fatal("signing failure invalidated old access")
	}
	issuer.fail = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Refresh(ctx, old.RefreshToken); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored")
	}
	if _, err := service.Refresh(context.Background(), old.RefreshToken); err != nil {
		t.Fatal("old refresh consumed on failure")
	}
}

func TestRevocationExpiryRestartAndSeparateSessions(t *testing.T) {
	service := newService(t)
	a, b := login(t, service), login(t, service)
	claims, _ := service.Inspect(context.Background(), a.AccessToken)
	service.Revoke(claims.SessionID)
	if _, err := service.Refresh(context.Background(), a.RefreshToken); !errors.Is(err, ErrToken) {
		t.Fatal("revoked refresh accepted")
	}
	if _, err := service.Inspect(context.Background(), a.AccessToken); !errors.Is(err, ErrToken) {
		t.Fatal("revoked access accepted")
	}
	if _, err := service.Inspect(context.Background(), b.AccessToken); err != nil {
		t.Fatal("unrelated session revoked")
	}
	restarted := New(testIdentities{}, service.issuer, time.Minute, time.Hour)
	if _, err := restarted.Inspect(context.Background(), b.AccessToken); !errors.Is(err, ErrToken) {
		t.Fatal("old session survived memory restart")
	}
	if _, err := restarted.Refresh(context.Background(), b.RefreshToken); !errors.Is(err, ErrToken) {
		t.Fatal("old refresh survived restart")
	}
	service.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := service.Refresh(context.Background(), b.RefreshToken); !errors.Is(err, ErrToken) {
		t.Fatal("expired refresh accepted")
	}
}

func TestBadLoginAndRefreshRejected(t *testing.T) {
	service := newService(t)
	if _, err := service.Login(context.Background(), "field-team", "bad"); !errors.Is(err, identity.ErrCredentials) {
		t.Fatal("bad password accepted")
	}
	for _, value := range []string{"", "unknown"} {
		if _, err := service.Refresh(context.Background(), value); !errors.Is(err, ErrToken) {
			t.Fatal("invalid refresh accepted")
		}
	}
}

func TestRotationDoesNotExtendSessionExpiryAndStoresOnlyRefreshHash(t *testing.T) {
	service := newService(t)
	now := time.Now()
	service.now = func() time.Time { return now }
	old := login(t, service)
	claims, _ := service.Inspect(context.Background(), old.AccessToken)
	deadline := service.sessions[claims.SessionID].expires
	now = now.Add(30 * time.Minute)
	next, err := service.Refresh(context.Background(), old.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if service.sessions[claims.SessionID].expires != deadline {
		t.Fatal("rotation extended session lifetime")
	}
	now = deadline
	if _, err := service.Refresh(context.Background(), next.RefreshToken); !errors.Is(err, ErrToken) {
		t.Fatal("refresh accepted at session expiry")
	}
}
