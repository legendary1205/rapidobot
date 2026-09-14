//go:build integration

package panel

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAgainstARealPanel exercises the client against a live Rapido-Go panel,
// checking the assumptions the unit tests can only fake: that the panel
// accepts this create body, honours expire and data_limit, re-enables and
// resets on renew, and hands back a usable subscription link.
//
//	PANEL_URL=... PANEL_USERNAME=... PANEL_PASSWORD=... go test -tags integration ./internal/panel -run Real -v
//
// It creates one throwaway account and deletes it again.
func TestAgainstARealPanel(t *testing.T) {
	base, user, pass := os.Getenv("PANEL_URL"), os.Getenv("PANEL_USERNAME"), os.Getenv("PANEL_PASSWORD")
	if base == "" {
		t.Skip("PANEL_URL not set")
	}
	ctx := context.Background()
	c := New(base, user, pass)
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}

	name := fmt.Sprintf("rbit_%d", time.Now().Unix()%1_000_000)
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete, strings.TrimRight(base, "/")+"/api/user/"+url.PathEscape(name), nil)
		c.mu.Lock()
		req.Header.Set("Authorization", "Bearer "+c.token)
		c.mu.Unlock()
		if resp, err := c.http.Do(req); err == nil {
			resp.Body.Close()
			t.Logf("cleanup: deleted %s (HTTP %d)", name, resp.StatusCode)
		}
	})

	now := time.Now()
	acc, err := c.CreateAccount(ctx, name, Grant{DataGB: 5, Days: 3, Note: "rapidobot integration test"}, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Logf("created: status=%s limit=%v expire=%v sub=%s", acc.Status, deref(acc.DataLimit), deref(acc.Expire), acc.SubscriptionURL)
	if acc.DataLimit == nil || *acc.DataLimit != 5*gigabyte {
		t.Errorf("data_limit = %v, want 5 GB", deref(acc.DataLimit))
	}
	if acc.Expire == nil || abs(*acc.Expire-now.Add(3*24*time.Hour).Unix()) > 5 {
		t.Errorf("expire = %v, want now+3d", deref(acc.Expire))
	}
	if !strings.HasPrefix(acc.SubscriptionURL, "http") || !strings.Contains(acc.SubscriptionURL, "/sub/") {
		t.Errorf("subscription url %q is not an absolute /sub/ link", acc.SubscriptionURL)
	}

	// The link must actually serve configs, not just look right.
	resp, err := http.Get(acc.SubscriptionURL)
	if err != nil {
		t.Fatalf("fetch subscription: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("subscription link returned HTTP %d, want 200", resp.StatusCode)
	}

	if _, err := c.CreateAccount(ctx, name, Grant{DataGB: 1}, now); err != ErrExists {
		t.Errorf("creating the same username again: err = %v, want ErrExists", err)
	}

	renewed, err := c.Renew(ctx, name, Grant{DataGB: 20, Days: 30}, now)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	t.Logf("renewed: status=%s limit=%v expire=%v", renewed.Status, deref(renewed.DataLimit), deref(renewed.Expire))
	wantExpire := time.Unix(*acc.Expire, 0).Add(30 * 24 * time.Hour).Unix()
	if renewed.Expire == nil || abs(*renewed.Expire-wantExpire) > 5 {
		t.Errorf("renewed expire = %v, want the old expiry + 30d (%d)", deref(renewed.Expire), wantExpire)
	}
	if renewed.DataLimit == nil || *renewed.DataLimit != 20*gigabyte {
		t.Errorf("renewed data_limit = %v, want 20 GB", deref(renewed.DataLimit))
	}
	if renewed.Status != "active" || renewed.UsedTraffic != 0 {
		t.Errorf("renewed status=%s used=%d, want active and reset", renewed.Status, renewed.UsedTraffic)
	}
}

func deref(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
