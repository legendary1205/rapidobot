package shop

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/legendary1205/rapidobot/internal/panel"
	"github.com/legendary1205/rapidobot/internal/store"
)

// fakePanel stands in for a Rapido-Go panel: accounts live in a map, and
// failNext makes the next write fail the way a panel outage would.
type fakePanel struct {
	mu       sync.Mutex
	accounts map[string]panel.Account
	failNext error
	creates  int
}

func newFakePanel() *fakePanel { return &fakePanel{accounts: map[string]panel.Account{}} }

func (f *fakePanel) CreateAccount(_ context.Context, username string, g panel.Grant, now time.Time) (panel.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNext; err != nil {
		f.failNext = nil
		return panel.Account{}, err
	}
	if _, ok := f.accounts[username]; ok {
		return panel.Account{}, panel.ErrExists
	}
	f.creates++
	limit := g.DataGB * 1024 * 1024 * 1024
	acc := panel.Account{Username: username, Status: "active", DataLimit: &limit, SubscriptionURL: "https://p/sub/" + username}
	if g.Days > 0 || g.Hours > 0 {
		d := time.Duration(g.Days) * 24 * time.Hour
		if g.Hours > 0 {
			d = time.Duration(g.Hours) * time.Hour
		}
		exp := now.Add(d).Unix()
		acc.Expire = &exp
	}
	f.accounts[username] = acc
	return acc, nil
}

func (f *fakePanel) GetAccount(_ context.Context, username string) (panel.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	acc, ok := f.accounts[username]
	if !ok {
		return panel.Account{}, panel.ErrNotFound
	}
	return acc, nil
}

func (f *fakePanel) Renew(_ context.Context, username string, g panel.Grant, now time.Time) (panel.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failNext; err != nil {
		f.failNext = nil
		return panel.Account{}, err
	}
	acc, ok := f.accounts[username]
	if !ok {
		return panel.Account{}, panel.ErrNotFound
	}
	base := now
	if acc.Expire != nil && time.Unix(*acc.Expire, 0).After(now) {
		base = time.Unix(*acc.Expire, 0)
	}
	exp := base.Add(time.Duration(g.Days) * 24 * time.Hour).Unix()
	acc.Expire = &exp
	acc.UsedTraffic = 0
	f.accounts[username] = acc
	return acc, nil
}

type harness struct {
	t     *testing.T
	ctx   context.Context
	shop  *Shop
	st    *store.Store
	panel *fakePanel
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	fp := newFakePanel()
	h := &harness{t: t, ctx: ctx, st: st, panel: fp, now: time.Unix(1_800_000_000, 0)}
	h.shop = New(st, fp)
	h.shop.Now = func() time.Time { return h.now }
	if err := st.SetSetting(ctx, SettingCardNumber, "6037-9911-1111-1111"); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) user(id int64, referrerCode string) store.User {
	h.t.Helper()
	u, _, err := h.st.UpsertUser(h.ctx, id, "u", "User", referrerCode)
	if err != nil {
		h.t.Fatalf("upsert user %d: %v", id, err)
	}
	return u
}

func (h *harness) reload(id int64) store.User {
	h.t.Helper()
	u, err := h.st.GetUser(h.ctx, id)
	if err != nil {
		h.t.Fatalf("get user %d: %v", id, err)
	}
	return u
}

func (h *harness) plan(price, agentPrice int64) store.Plan {
	h.t.Helper()
	p, err := h.st.CreatePlan(h.ctx, store.Plan{Name: "30 GB", DataGB: 30, Days: 30, Price: price, AgentPrice: agentPrice})
	if err != nil {
		h.t.Fatalf("create plan: %v", err)
	}
	return p
}

// payByCard runs a draft through card payment up to "awaiting review".
func (h *harness) payByCard(u store.User, orderID int64) {
	h.t.Helper()
	if _, err := h.shop.ChooseCard(h.ctx, u, orderID); err != nil {
		h.t.Fatalf("choose card: %v", err)
	}
	if _, err := h.shop.SubmitReceipt(h.ctx, u.ID, "receipt-file"); err != nil {
		h.t.Fatalf("submit receipt: %v", err)
	}
}

func TestCardPurchaseDeliversOnApproval(t *testing.T) {
	h := newHarness(t)
	u := h.user(100, "")
	p := h.plan(150_000, 0)

	o, _, err := h.shop.StartPurchase(h.ctx, u, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	h.payByCard(u, o.ID)

	res, err := h.shop.Approve(h.ctx, o.ID, 999)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if res.Failed {
		t.Fatalf("delivery failed: %v", res.Err)
	}
	if res.Order.Status != store.StatusCompleted {
		t.Errorf("status = %s, want completed", res.Order.Status)
	}
	if res.Account.SubscriptionURL == "" || res.Service.UserID != u.ID {
		t.Errorf("no usable service delivered: %+v", res)
	}
}

func TestApprovingTwiceDeliversOnce(t *testing.T) {
	h := newHarness(t)
	u := h.user(101, "")
	o, _, _ := h.shop.StartPurchase(h.ctx, u, h.plan(100_000, 0).ID)
	h.payByCard(u, o.ID)

	if _, err := h.shop.Approve(h.ctx, o.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.shop.Approve(h.ctx, o.ID, 2); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second approve err = %v, want ErrConflict", err)
	}
	if h.panel.creates != 1 {
		t.Errorf("panel accounts created = %d, want exactly 1", h.panel.creates)
	}
	svcs, _ := h.st.ListServices(h.ctx, u.ID)
	if len(svcs) != 1 {
		t.Errorf("services = %d, want 1", len(svcs))
	}
}

func TestRejectedReceiptDeliversNothingAndKeepsTheCode(t *testing.T) {
	h := newHarness(t)
	u := h.user(102, "")
	if err := h.st.CreateDiscount(h.ctx, store.DiscountCode{Code: "OFF10", Percent: 10}); err != nil {
		t.Fatal(err)
	}
	o, _, _ := h.shop.StartPurchase(h.ctx, u, h.plan(100_000, 0).ID)
	if _, err := h.shop.ApplyCode(h.ctx, u, o.ID, "off10"); err != nil {
		t.Fatalf("apply code: %v", err)
	}
	h.payByCard(u, o.ID)
	if _, err := h.shop.Reject(h.ctx, o.ID, 1); err != nil {
		t.Fatal(err)
	}
	if h.panel.creates != 0 {
		t.Errorf("a rejected order created %d panel accounts", h.panel.creates)
	}
	// The customer must still be able to use their code on a new order.
	o2, _, _ := h.shop.StartPurchase(h.ctx, u, h.plan(100_000, 0).ID)
	if _, err := h.shop.ApplyCode(h.ctx, u, o2.ID, "OFF10"); err != nil {
		t.Errorf("code was burned by a rejected order: %v", err)
	}
}

func TestDiscountRules(t *testing.T) {
	h := newHarness(t)
	p := h.plan(200_000, 0)
	mustCode := func(d store.DiscountCode) {
		if err := h.st.CreateDiscount(h.ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	mustCode(store.DiscountCode{Code: "PCT25", Percent: 25})
	mustCode(store.DiscountCode{Code: "FLAT", Amount: 500_000}) // more than the price
	mustCode(store.DiscountCode{Code: "OLD", Percent: 10, ExpiresAt: h.now.Unix() - 1})
	mustCode(store.DiscountCode{Code: "ONCE", Percent: 10, MaxUses: 1})

	u := h.user(200, "")
	o, _, _ := h.shop.StartPurchase(h.ctx, u, p.ID)
	got, err := h.shop.ApplyCode(h.ctx, u, o.ID, "pct25")
	if err != nil || got.Amount != 150_000 || got.Discount != 50_000 {
		t.Errorf("25%% off 200000 = amount %d discount %d err %v, want 150000/50000", got.Amount, got.Discount, err)
	}
	// A second code replaces the first instead of stacking.
	got, err = h.shop.ApplyCode(h.ctx, u, o.ID, "FLAT")
	if err != nil || got.Amount != 0 || got.Discount != 200_000 {
		t.Errorf("flat code larger than price: amount %d discount %d err %v, want capped at 0", got.Amount, got.Discount, err)
	}

	if _, err := h.shop.ApplyCode(h.ctx, u, o.ID, "nope"); !errors.Is(err, ErrCodeInvalid) {
		t.Errorf("unknown code err = %v", err)
	}
	if _, err := h.shop.ApplyCode(h.ctx, u, o.ID, "OLD"); !errors.Is(err, ErrCodeExpired) {
		t.Errorf("expired code err = %v", err)
	}

	// ONCE: first customer spends it, second customer finds it used up.
	a := h.user(201, "")
	oa, _, _ := h.shop.StartPurchase(h.ctx, a, p.ID)
	if _, err := h.shop.ApplyCode(h.ctx, a, oa.ID, "ONCE"); err != nil {
		t.Fatal(err)
	}
	h.payByCard(a, oa.ID)
	if _, err := h.shop.Approve(h.ctx, oa.ID, 1); err != nil {
		t.Fatal(err)
	}
	b := h.user(202, "")
	ob, _, _ := h.shop.StartPurchase(h.ctx, b, p.ID)
	if _, err := h.shop.ApplyCode(h.ctx, b, ob.ID, "ONCE"); !errors.Is(err, ErrCodeUsedUp) {
		t.Errorf("max-uses code err = %v, want ErrCodeUsedUp", err)
	}
	// And the same customer cannot use a code twice.
	mustCode(store.DiscountCode{Code: "MULTI", Percent: 5})
	o1, _, _ := h.shop.StartPurchase(h.ctx, a, p.ID)
	h.shop.ApplyCode(h.ctx, a, o1.ID, "MULTI")
	h.payByCard(a, o1.ID)
	h.shop.Approve(h.ctx, o1.ID, 1)
	o2, _, _ := h.shop.StartPurchase(h.ctx, a, p.ID)
	if _, err := h.shop.ApplyCode(h.ctx, a, o2.ID, "MULTI"); !errors.Is(err, ErrCodeAlreadyUsed) {
		t.Errorf("reusing a spent code err = %v, want ErrCodeAlreadyUsed", err)
	}
}

func TestDiscountDoesNotApplyToTopups(t *testing.T) {
	h := newHarness(t)
	u := h.user(210, "")
	h.st.CreateDiscount(h.ctx, store.DiscountCode{Code: "X", Percent: 50})
	o, err := h.shop.StartTopup(h.ctx, u, 50_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.shop.ApplyCode(h.ctx, u, o.ID, "X"); !errors.Is(err, ErrCodeNotApplicable) {
		t.Errorf("discount on a top-up err = %v", err)
	}
}

func TestWalletPurchase(t *testing.T) {
	h := newHarness(t)
	u := h.user(300, "")
	p := h.plan(100_000, 0)
	if err := h.st.AdjustBalance(h.ctx, u.ID, 250_000, "admin", 0); err != nil {
		t.Fatal(err)
	}

	o, _, _ := h.shop.StartPurchase(h.ctx, u, p.ID)
	res, err := h.shop.PayWithWallet(h.ctx, u, o.ID)
	if err != nil || res.Failed {
		t.Fatalf("wallet purchase: err %v failed %v (%v)", err, res.Failed, res.Err)
	}
	if bal := h.reload(u.ID).Balance; bal != 150_000 {
		t.Errorf("balance = %d, want 150000", bal)
	}
	// Paying the same order again must not charge again.
	if _, err := h.shop.PayWithWallet(h.ctx, u, o.ID); !errors.Is(err, store.ErrConflict) {
		t.Errorf("second wallet pay err = %v, want ErrConflict", err)
	}
	if bal := h.reload(u.ID).Balance; bal != 150_000 {
		t.Errorf("balance after double pay = %d, want still 150000", bal)
	}
}

func TestWalletCannotOverspend(t *testing.T) {
	h := newHarness(t)
	u := h.user(301, "")
	h.st.AdjustBalance(h.ctx, u.ID, 50_000, "admin", 0)
	o, _, _ := h.shop.StartPurchase(h.ctx, u, h.plan(100_000, 0).ID)
	if _, err := h.shop.PayWithWallet(h.ctx, u, o.ID); !errors.Is(err, store.ErrInsufficientBalance) {
		t.Fatalf("err = %v, want ErrInsufficientBalance", err)
	}
	if bal := h.reload(u.ID).Balance; bal != 50_000 {
		t.Errorf("a refused payment changed the balance to %d", bal)
	}
	if got, _ := h.st.GetOrder(h.ctx, o.ID); got.Status != store.StatusDraft {
		t.Errorf("order status = %s, want still draft", got.Status)
	}
}

func TestPanelFailureRefundsTheWallet(t *testing.T) {
	h := newHarness(t)
	u := h.user(302, "")
	h.st.AdjustBalance(h.ctx, u.ID, 100_000, "admin", 0)
	o, _, _ := h.shop.StartPurchase(h.ctx, u, h.plan(100_000, 0).ID)

	h.panel.failNext = errors.New("panel down")
	res, err := h.shop.PayWithWallet(h.ctx, u, o.ID)
	if err != nil {
		t.Fatalf("a delivery failure must be reported in Result, not as an error: %v", err)
	}
	if !res.Failed || !res.Refunded {
		t.Errorf("failed=%v refunded=%v, want both true", res.Failed, res.Refunded)
	}
	if bal := h.reload(u.ID).Balance; bal != 100_000 {
		t.Errorf("balance after a failed delivery = %d, want the full 100000 back", bal)
	}
	if res.Order.Status != store.StatusFailed {
		t.Errorf("status = %s, want failed", res.Order.Status)
	}
}

func TestTopupCreditsOnce(t *testing.T) {
	h := newHarness(t)
	u := h.user(310, "")
	o, err := h.shop.StartTopup(h.ctx, u, 80_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.shop.SubmitReceipt(h.ctx, u.ID, "r"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.shop.Approve(h.ctx, o.ID, 1); err != nil {
		t.Fatal(err)
	}
	h.shop.Approve(h.ctx, o.ID, 2)
	if bal := h.reload(u.ID).Balance; bal != 80_000 {
		t.Errorf("balance = %d, want 80000 exactly once", bal)
	}
	if _, err := h.shop.StartTopup(h.ctx, u, 1); !errors.Is(err, ErrTopupRange) {
		t.Errorf("tiny top-up err = %v, want ErrTopupRange", err)
	}
}

func TestAgentsPayTheAgentPrice(t *testing.T) {
	h := newHarness(t)
	u := h.user(400, "")
	if err := h.st.SetAgent(h.ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	p := h.plan(100_000, 70_000)
	o, _, _ := h.shop.StartPurchase(h.ctx, h.reload(u.ID), p.ID)
	if o.Amount != 70_000 {
		t.Errorf("agent paid %d, want 70000", o.Amount)
	}
	// With no agent price set, an agent pays the normal price.
	o2, _, _ := h.shop.StartPurchase(h.ctx, h.reload(u.ID), h.plan(100_000, 0).ID)
	if o2.Amount != 100_000 {
		t.Errorf("agent with no agent price paid %d, want 100000", o2.Amount)
	}
}

func TestReferralRewardIsPaidOnceAndOnlyForPurchases(t *testing.T) {
	h := newHarness(t)
	h.st.SetSetting(h.ctx, SettingReferralReward, "20000")
	referrer := h.user(500, "")
	friend := h.user(501, referrer.ReferralCode)
	if friend.ReferredBy != referrer.ID {
		t.Fatalf("friend referred_by = %d, want %d", friend.ReferredBy, referrer.ID)
	}

	// A top-up alone earns nothing.
	top, _ := h.shop.StartTopup(h.ctx, friend, 100_000)
	h.shop.SubmitReceipt(h.ctx, friend.ID, "r")
	h.shop.Approve(h.ctx, top.ID, 1)
	if bal := h.reload(referrer.ID).Balance; bal != 0 {
		t.Errorf("referrer paid %d for a top-up", bal)
	}

	p := h.plan(50_000, 0)
	for i := 0; i < 2; i++ {
		o, _, _ := h.shop.StartPurchase(h.ctx, friend, p.ID)
		res, err := h.shop.PayWithWallet(h.ctx, h.reload(friend.ID), o.ID)
		if err != nil || res.Failed {
			t.Fatalf("purchase %d: %v %v", i, err, res.Err)
		}
		if i == 0 && res.Referrer != referrer.ID {
			t.Errorf("first purchase did not report the reward")
		}
	}
	if bal := h.reload(referrer.ID).Balance; bal != 20_000 {
		t.Errorf("referrer balance = %d, want 20000 exactly once", bal)
	}
}

func TestReferralCannotBeSelfOrReassigned(t *testing.T) {
	h := newHarness(t)
	a := h.user(600, "")
	// Opening your own link does nothing.
	again, _, _ := h.st.UpsertUser(h.ctx, 600, "u", "User", a.ReferralCode)
	if again.ReferredBy != 0 {
		t.Errorf("self-referral recorded")
	}
	b := h.user(601, "")
	// An existing customer opening someone's link is not re-attributed.
	existing, _, _ := h.st.UpsertUser(h.ctx, 600, "u", "User", b.ReferralCode)
	if existing.ReferredBy != 0 {
		t.Errorf("an existing customer was re-attributed to %d", existing.ReferredBy)
	}
}

func TestTrialIsOncePerCustomerAndSurvivesAPanelOutage(t *testing.T) {
	h := newHarness(t)
	u := h.user(700, "")

	h.panel.failNext = errors.New("panel down")
	if _, _, err := h.shop.ClaimTrial(h.ctx, u); err == nil {
		t.Fatal("trial should fail while the panel is down")
	}
	if h.reload(u.ID).TrialUsed {
		t.Fatal("a failed trial still counted as used")
	}

	sv, acc, err := h.shop.ClaimTrial(h.ctx, u)
	if err != nil || !sv.IsTrial || acc.SubscriptionURL == "" {
		t.Fatalf("trial: sv %+v acc %+v err %v", sv, acc, err)
	}
	if _, _, err := h.shop.ClaimTrial(h.ctx, u); !errors.Is(err, ErrTrialUsed) {
		t.Errorf("second trial err = %v, want ErrTrialUsed", err)
	}

	h.st.SetSetting(h.ctx, SettingTrialEnabled, "0")
	if _, _, err := h.shop.ClaimTrial(h.ctx, h.user(701, "")); !errors.Is(err, ErrTrialDisabled) {
		t.Errorf("disabled trial err = %v", err)
	}
}

func TestRenewExtendsFromTheCurrentExpiry(t *testing.T) {
	h := newHarness(t)
	u := h.user(800, "")
	h.st.AdjustBalance(h.ctx, u.ID, 1_000_000, "admin", 0)
	p := h.plan(100_000, 0) // 30 days

	o, _, _ := h.shop.StartPurchase(h.ctx, u, p.ID)
	first, _ := h.shop.PayWithWallet(h.ctx, u, o.ID)
	firstExpire := *first.Account.Expire

	// Renew 10 days later, while still valid: the 30 days stack on top.
	h.now = h.now.Add(10 * 24 * time.Hour)
	r, _, err := h.shop.StartRenew(h.ctx, u, first.Service.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.shop.PayWithWallet(h.ctx, h.reload(u.ID), r.ID)
	if err != nil || res.Failed {
		t.Fatalf("renew: %v %v", err, res.Err)
	}
	want := firstExpire + 30*24*3600
	if got := *res.Account.Expire; got != want {
		t.Errorf("renewed expiry = %d, want %d (current expiry + 30 days, not now + 30)", got, want)
	}

	// Someone else cannot renew this service.
	if _, _, err := h.shop.StartRenew(h.ctx, h.user(801, ""), first.Service.ID, p.ID); !errors.Is(err, ErrNotYours) {
		t.Errorf("renewing another customer's service err = %v", err)
	}
}

func TestReceiptOnlyAttachesToTheCurrentCheckout(t *testing.T) {
	h := newHarness(t)
	u := h.user(900, "")
	p := h.plan(100_000, 0)
	old, _, _ := h.shop.StartPurchase(h.ctx, u, p.ID)
	h.shop.ChooseCard(h.ctx, u, old.ID)

	// The customer walks away and starts a different order.
	cur, _, _ := h.shop.StartPurchase(h.ctx, u, p.ID)
	h.shop.ChooseCard(h.ctx, u, cur.ID)
	got, err := h.shop.SubmitReceipt(h.ctx, u.ID, "r")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != cur.ID {
		t.Errorf("receipt attached to order %d, want the current one %d", got.ID, cur.ID)
	}
	if o, _ := h.st.GetOrder(h.ctx, old.ID); o.Status != store.StatusCancelled {
		t.Errorf("abandoned order status = %s, want cancelled", o.Status)
	}
}

func TestInterruptedDeliveriesAreFailedAndRefunded(t *testing.T) {
	h := newHarness(t)
	u := h.user(950, "")
	h.st.AdjustBalance(h.ctx, u.ID, 100_000, "admin", 0)
	o, _, _ := h.shop.StartPurchase(h.ctx, u, h.plan(100_000, 0).ID)
	// Simulate a crash right after the wallet was charged.
	if _, err := h.st.PayFromWallet(h.ctx, o.ID, u.ID); err != nil {
		t.Fatal(err)
	}

	stuck, err := h.shop.RecoverInterrupted(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stuck) != 1 || stuck[0].Status != store.StatusFailed {
		t.Fatalf("recovered = %+v, want one failed order", stuck)
	}
	if bal := h.reload(u.ID).Balance; bal != 100_000 {
		t.Errorf("balance after recovery = %d, want the charge refunded", bal)
	}
}
