package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRapido is a minimal Rapido-Go admin API: token login, user create /
// read / modify / reset, and a switch to expire the current token.
type fakeRapido struct {
	mu       sync.Mutex
	token    string
	logins   int
	users    map[string]map[string]any
	resets   int
	lastBody map[string]any
}

func newFakeRapido() (*fakeRapido, *httptest.Server) {
	f := &fakeRapido{users: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/admin/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("username") != "admin" || r.Form.Get("password") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"detail":"Incorrect username or password"}`))
			return
		}
		f.mu.Lock()
		f.logins++
		f.token = "tok-" + time.Now().Format("150405.000000")
		tok := f.token
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"access_token": tok})
	})
	authed := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			ok := r.Header.Get("Authorization") == "Bearer "+f.token && f.token != ""
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/system", authed(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	mux.HandleFunc("/api/user", authed(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastBody = body
		name := body["username"].(string)
		if _, exists := f.users[name]; exists {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"detail":"User already exists"}`))
			return
		}
		body["status"] = "active"
		body["used_traffic"] = 0
		body["subscription_url"] = "/sub/" + name // relative, like a panel with no public prefix
		f.users[name] = body
		json.NewEncoder(w).Encode(body)
	}))
	mux.HandleFunc("/api/user/", authed(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/user/")
		name := strings.TrimSuffix(rest, "/reset")
		f.mu.Lock()
		defer f.mu.Unlock()
		u, ok := f.users[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"detail":"User not found"}`))
			return
		}
		switch {
		case strings.HasSuffix(rest, "/reset"):
			f.resets++
			u["used_traffic"] = 0
		case r.Method == http.MethodPut:
			var patch map[string]any
			json.NewDecoder(r.Body).Decode(&patch)
			for k, v := range patch {
				u[k] = v
			}
		}
		json.NewEncoder(w).Encode(u)
	}))
	return f, httptest.NewServer(mux)
}

func TestCreateAccountSendsAPlanAndMakesTheLinkAbsolute(t *testing.T) {
	f, srv := newFakeRapido()
	defer srv.Close()
	c := New(srv.URL+"/", "admin", "secret") // trailing slash must not double up
	now := time.Unix(1_800_000_000, 0)

	acc, err := c.CreateAccount(context.Background(), "rb1_abcd", Grant{DataGB: 30, Days: 30}, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if want := srv.URL + "/sub/rb1_abcd"; acc.SubscriptionURL != want {
		t.Errorf("subscription url = %q, want the relative link made absolute: %q", acc.SubscriptionURL, want)
	}
	if got := int64(f.lastBody["data_limit"].(float64)); got != 30*gigabyte {
		t.Errorf("data_limit = %d, want 30 GB in bytes", got)
	}
	if got := int64(f.lastBody["expire"].(float64)); got != now.Add(30*24*time.Hour).Unix() {
		t.Errorf("expire = %d, want now + 30 days", got)
	}
	if _, hasInbounds := f.lastBody["inbounds"]; hasInbounds {
		t.Error("inbounds must be left out so the panel grants every inbound")
	}
	proxies, _ := f.lastBody["proxies"].(map[string]any)
	if _, ok := proxies["vless"]; !ok {
		t.Errorf("proxies = %v, want a vless proxy", f.lastBody["proxies"])
	}
}

func TestUnlimitedGrantSendsZeroes(t *testing.T) {
	f, srv := newFakeRapido()
	defer srv.Close()
	c := New(srv.URL, "admin", "secret")
	if _, err := c.CreateAccount(context.Background(), "rb2_x", Grant{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if f.lastBody["data_limit"].(float64) != 0 || f.lastBody["expire"].(float64) != 0 {
		t.Errorf("unlimited grant sent data_limit=%v expire=%v, want 0/0", f.lastBody["data_limit"], f.lastBody["expire"])
	}
}

func TestDuplicateUsernameIsErrExists(t *testing.T) {
	_, srv := newFakeRapido()
	defer srv.Close()
	c := New(srv.URL, "admin", "secret")
	ctx := context.Background()
	c.CreateAccount(ctx, "rb3_dup", Grant{DataGB: 1}, time.Now())
	if _, err := c.CreateAccount(ctx, "rb3_dup", Grant{DataGB: 1}, time.Now()); !errors.Is(err, ErrExists) {
		t.Errorf("err = %v, want ErrExists", err)
	}
}

func TestAnExpiredTokenIsRenewedOnceAndTheCallRetried(t *testing.T) {
	f, srv := newFakeRapido()
	defer srv.Close()
	c := New(srv.URL, "admin", "secret")
	ctx := context.Background()
	c.CreateAccount(ctx, "rb4_tok", Grant{DataGB: 1}, time.Now())

	// The panel forgets the token, as it would after a restart or expiry.
	f.mu.Lock()
	f.token = "rotated-away"
	loginsBefore := f.logins
	f.mu.Unlock()

	acc, err := c.GetAccount(ctx, "rb4_tok")
	if err != nil {
		t.Fatalf("a call after token expiry must succeed transparently: %v", err)
	}
	if acc.Username != "rb4_tok" {
		t.Errorf("got %+v", acc)
	}
	if f.logins != loginsBefore+1 {
		t.Errorf("logins after expiry = %d, want exactly one re-login", f.logins-loginsBefore)
	}
}

func TestWrongPasswordIsReported(t *testing.T) {
	_, srv := newFakeRapido()
	defer srv.Close()
	err := New(srv.URL, "admin", "wrong").Ping(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want a 401 APIError", err)
	}
	if !strings.Contains(apiErr.Detail, "Incorrect") {
		t.Errorf("the panel's own message was lost: %q", apiErr.Detail)
	}
}

func TestRenewStacksOnAStillValidExpiryAndResetsUsage(t *testing.T) {
	f, srv := newFakeRapido()
	defer srv.Close()
	c := New(srv.URL, "admin", "secret")
	ctx := context.Background()
	start := time.Unix(1_800_000_000, 0)
	c.CreateAccount(ctx, "rb5_ren", Grant{DataGB: 10, Days: 30}, start)

	tenDaysLater := start.Add(10 * 24 * time.Hour)
	acc, err := c.Renew(ctx, "rb5_ren", Grant{DataGB: 50, Days: 30}, tenDaysLater)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	want := start.Add(60 * 24 * time.Hour).Unix() // 30 left + 30 new, not 10 + 30
	if acc.Expire == nil || *acc.Expire != want {
		t.Errorf("expire = %v, want %d (stacked on the remaining time)", acc.Expire, want)
	}
	if acc.DataLimit == nil || *acc.DataLimit != 50*gigabyte {
		t.Errorf("data_limit = %v, want 50 GB", acc.DataLimit)
	}
	if f.resets != 1 {
		t.Errorf("usage resets = %d, want 1", f.resets)
	}
}

func TestRenewAfterExpiryCountsFromNow(t *testing.T) {
	_, srv := newFakeRapido()
	defer srv.Close()
	c := New(srv.URL, "admin", "secret")
	ctx := context.Background()
	start := time.Unix(1_800_000_000, 0)
	c.CreateAccount(ctx, "rb6_old", Grant{DataGB: 10, Days: 30}, start)

	muchLater := start.Add(90 * 24 * time.Hour)
	acc, err := c.Renew(ctx, "rb6_old", Grant{DataGB: 10, Days: 30}, muchLater)
	if err != nil {
		t.Fatal(err)
	}
	if want := muchLater.Add(30 * 24 * time.Hour).Unix(); *acc.Expire != want {
		t.Errorf("expire = %d, want now + 30 days (%d), not the long-past expiry + 30", *acc.Expire, want)
	}
}

func TestMissingAccountIsErrNotFound(t *testing.T) {
	_, srv := newFakeRapido()
	defer srv.Close()
	if _, err := New(srv.URL, "admin", "secret").GetAccount(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}
