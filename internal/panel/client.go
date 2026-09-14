// Package panel talks to a Rapido-Go panel's admin API: log in, create an
// account, read it back, renew it. It holds no bot logic.
package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const gigabyte = 1024 * 1024 * 1024

var (
	ErrNotFound = errors.New("panel: account not found")
	ErrExists   = errors.New("panel: username already exists")
)

// APIError is any non-2xx answer the panel gave, with its own message.
type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("panel returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("panel returned HTTP %d: %s", e.Status, e.Detail)
}

type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client

	mu    sync.Mutex
	token string
}

func New(baseURL, username, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http:     &http.Client{Timeout: 20 * time.Second},
	}
}

// Account is the part of a panel user this bot reads.
type Account struct {
	Username        string `json:"username"`
	Status          string `json:"status"`
	UsedTraffic     int64  `json:"used_traffic"`
	DataLimit       *int64 `json:"data_limit"`
	Expire          *int64 `json:"expire"`
	SubscriptionURL string `json:"subscription_url"`
}

// Unlimited reports whether the account has no data cap.
func (a Account) Unlimited() bool { return a.DataLimit == nil || *a.DataLimit == 0 }

// NoExpiry reports whether the account never expires.
func (a Account) NoExpiry() bool { return a.Expire == nil || *a.Expire == 0 }

// Grant is what a plan gives an account.
type Grant struct {
	DataGB int64 // 0 = unlimited
	Days   int64 // 0 = never expires
	Hours  int64 // used instead of Days when set; for short free trials
	Note   string
}

func (g Grant) dataLimit() int64 { return g.DataGB * gigabyte }

func (g Grant) duration() time.Duration {
	if g.Hours > 0 {
		return time.Duration(g.Hours) * time.Hour
	}
	return time.Duration(g.Days) * 24 * time.Hour
}

// Login fetches a fresh token. Callers rarely need it directly - every API
// call logs in on demand and again after a 401.
func (c *Client) Login(ctx context.Context) error {
	form := url.Values{"username": {c.username}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/admin/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("panel unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return apiError(resp.StatusCode, body)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return errors.New("panel login returned no token")
	}
	c.mu.Lock()
	c.token = out.AccessToken
	c.mu.Unlock()
	return nil
}

// CreateAccount makes a new VLESS account. Leaving `inbounds` out on
// purpose: the panel then grants every inbound it has for the protocol,
// which keeps the bot correct when the operator adds a location later.
func (c *Client) CreateAccount(ctx context.Context, username string, g Grant, now time.Time) (Account, error) {
	body := map[string]any{
		"username":                  username,
		"proxies":                   map[string]any{"vless": map[string]any{}},
		"data_limit":                g.dataLimit(),
		"data_limit_reset_strategy": "no_reset",
		"status":                    "active",
		"note":                      g.Note,
	}
	if d := g.duration(); d > 0 {
		body["expire"] = now.Add(d).Unix()
	} else {
		body["expire"] = 0
	}
	var acc Account
	err := c.call(ctx, http.MethodPost, "/api/user", body, &acc)
	// 409 means a clash only here, on create. The panel also answers 409 to
	// an edit of a Gateway-managed account, which is not "already exists"
	// at all - so the translation lives with the one call where it is true.
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
		return Account{}, ErrExists
	}
	return c.absolute(acc), err
}

func (c *Client) GetAccount(ctx context.Context, username string) (Account, error) {
	var acc Account
	err := c.call(ctx, http.MethodGet, "/api/user/"+url.PathEscape(username), nil, &acc)
	return c.absolute(acc), err
}

// Renew extends an account by a plan: the new expiry is counted from
// whichever is later, now or the current expiry, so renewing early never
// throws away days the customer already paid for. Usage is reset and the
// account re-enabled, since a renewal is normally bought exactly because the
// old allowance ran out.
func (c *Client) Renew(ctx context.Context, username string, g Grant, now time.Time) (Account, error) {
	current, err := c.GetAccount(ctx, username)
	if err != nil {
		return Account{}, err
	}
	var expire int64
	if d := g.duration(); d > 0 {
		base := now
		if !current.NoExpiry() {
			if cur := time.Unix(*current.Expire, 0); cur.After(now) {
				base = cur
			}
		}
		expire = base.Add(d).Unix()
	}
	body := map[string]any{
		"data_limit": g.dataLimit(),
		"expire":     expire,
		"status":     "active",
	}
	var acc Account
	if err := c.call(ctx, http.MethodPut, "/api/user/"+url.PathEscape(username), body, &acc); err != nil {
		return Account{}, err
	}
	if err := c.call(ctx, http.MethodPost, "/api/user/"+url.PathEscape(username)+"/reset", nil, nil); err != nil {
		return Account{}, fmt.Errorf("renewed, but resetting usage failed: %w", err)
	}
	return c.GetAccount(ctx, username)
}

// Ping proves the URL, the credentials and the API all work - the installer
// and the bot's startup use it to fail loudly instead of at the first sale.
func (c *Client) Ping(ctx context.Context) error {
	if err := c.Login(ctx); err != nil {
		return err
	}
	return c.call(ctx, http.MethodGet, "/api/system", nil, nil)
}

// absolute turns the panel's relative "/sub/<token>" into a full URL when the
// panel was not configured with a public subscription prefix.
func (c *Client) absolute(a Account) Account {
	if strings.HasPrefix(a.SubscriptionURL, "/") {
		a.SubscriptionURL = c.baseURL + a.SubscriptionURL
	}
	return a
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	c.mu.Lock()
	haveToken := c.token != ""
	c.mu.Unlock()
	if !haveToken {
		if err := c.Login(ctx); err != nil {
			return err
		}
	}

	status, body, err := c.do(ctx, method, path, in)
	if err != nil {
		return err
	}
	// Tokens expire. One fresh login and one retry, never a loop.
	if status == http.StatusUnauthorized {
		if err := c.Login(ctx); err != nil {
			return err
		}
		if status, body, err = c.do(ctx, method, path, in); err != nil {
			return err
		}
	}

	switch {
	case status == http.StatusNotFound:
		return ErrNotFound
	case status < 200 || status > 299:
		return apiError(status, body)
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("panel returned an unexpected response: %w", err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, in any) (int, []byte, error) {
	var reader io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.mu.Lock()
	req.Header.Set("Authorization", "Bearer "+c.token)
	c.mu.Unlock()

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("panel unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, body, err
}

func apiError(status int, body []byte) error {
	var parsed struct {
		Detail any `json:"detail"`
	}
	detail := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &parsed) == nil && parsed.Detail != nil {
		if s, ok := parsed.Detail.(string); ok {
			detail = s
		} else if raw, err := json.Marshal(parsed.Detail); err == nil {
			detail = string(raw)
		}
	}
	if len(detail) > 300 {
		detail = detail[:300]
	}
	return &APIError{Status: status, Detail: detail}
}
