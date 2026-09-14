// Package shop is the selling logic: pricing, discounts, payment, delivery,
// free trials and referral rewards. It joins the store to the panel and
// knows nothing about Telegram, so every rule here is testable without a
// bot.
package shop

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/legendary1205/rapidobot/internal/panel"
	"github.com/legendary1205/rapidobot/internal/store"
)

// Panel is the slice of the panel client the shop uses - an interface so
// tests can stand in for a real panel.
type Panel interface {
	CreateAccount(ctx context.Context, username string, g panel.Grant, now time.Time) (panel.Account, error)
	GetAccount(ctx context.Context, username string) (panel.Account, error)
	Renew(ctx context.Context, username string, g panel.Grant, now time.Time) (panel.Account, error)
}

type Shop struct {
	Store *store.Store
	Panel Panel
	Now   func() time.Time
}

func New(st *store.Store, p Panel) *Shop {
	return &Shop{Store: st, Panel: p, Now: time.Now}
}

// Setting keys, with the value used until an admin changes them.
const (
	SettingCardNumber     = "card_number"
	SettingCardHolder     = "card_holder"
	SettingBankName       = "bank_name"
	SettingSupport        = "support_username"
	SettingTrialEnabled   = "trial_enabled"
	SettingTrialGB        = "trial_gb"
	SettingTrialHours     = "trial_hours"
	SettingReferralReward = "referral_reward"
	SettingTopupMin       = "topup_min"
	SettingTopupMax       = "topup_max"
)

var settingDefaults = map[string]string{
	SettingTrialEnabled:   "1",
	SettingTrialGB:        "1",
	SettingTrialHours:     "24",
	SettingReferralReward: "10000",
	SettingTopupMin:       "10000",
	SettingTopupMax:       "10000000",
}

func (s *Shop) Setting(ctx context.Context, key string) string {
	return s.Store.Setting(ctx, key, settingDefaults[key])
}

func (s *Shop) SettingInt(ctx context.Context, key string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s.Setting(ctx, key)), 10, 64)
	return n
}

var (
	ErrPlanUnavailable   = errors.New("plan unavailable")
	ErrNotYours          = errors.New("not yours")
	ErrTopupRange        = errors.New("top-up amount out of range")
	ErrTrialDisabled     = errors.New("trial disabled")
	ErrTrialUsed         = errors.New("trial already used")
	ErrCodeInvalid       = errors.New("discount code invalid")
	ErrCodeExpired       = errors.New("discount code expired")
	ErrCodeUsedUp        = errors.New("discount code used up")
	ErrCodeAlreadyUsed   = errors.New("discount code already used by this customer")
	ErrCodeNotApplicable = errors.New("discount code not applicable to this order")
	ErrCardNotConfigured = errors.New("card details not configured")
)

// ── starting an order ───────────────────────────────────────────────────────

// StartPurchase opens a draft order for a plan at the customer's price.
// Any earlier unpaid checkout is cancelled first, so a receipt can only ever
// attach to the order the customer is looking at now.
func (s *Shop) StartPurchase(ctx context.Context, u store.User, planID int64) (store.Order, store.Plan, error) {
	plan, err := s.Store.GetPlan(ctx, planID)
	if err != nil || !plan.IsActive {
		return store.Order{}, store.Plan{}, ErrPlanUnavailable
	}
	if err := s.Store.CancelOpenOrders(ctx, u.ID); err != nil {
		return store.Order{}, store.Plan{}, err
	}
	o, err := s.Store.CreateOrder(ctx, store.Order{
		UserID: u.ID, Kind: store.KindPurchase, PlanID: plan.ID, BaseAmount: plan.PriceFor(u),
	})
	return o, plan, err
}

// StartRenew opens a draft order renewing one of the customer's own services.
func (s *Shop) StartRenew(ctx context.Context, u store.User, serviceID, planID int64) (store.Order, store.Plan, error) {
	sv, err := s.Store.GetService(ctx, serviceID)
	if err != nil || sv.UserID != u.ID {
		return store.Order{}, store.Plan{}, ErrNotYours
	}
	plan, err := s.Store.GetPlan(ctx, planID)
	if err != nil || !plan.IsActive {
		return store.Order{}, store.Plan{}, ErrPlanUnavailable
	}
	if err := s.Store.CancelOpenOrders(ctx, u.ID); err != nil {
		return store.Order{}, store.Plan{}, err
	}
	o, err := s.Store.CreateOrder(ctx, store.Order{
		UserID: u.ID, Kind: store.KindRenew, PlanID: plan.ID, ServiceID: sv.ID, BaseAmount: plan.PriceFor(u),
	})
	return o, plan, err
}

// StartTopup opens a card-paid wallet top-up within the configured range.
func (s *Shop) StartTopup(ctx context.Context, u store.User, amount int64) (store.Order, error) {
	if amount < s.SettingInt(ctx, SettingTopupMin) || amount > s.SettingInt(ctx, SettingTopupMax) {
		return store.Order{}, ErrTopupRange
	}
	if err := s.Store.CancelOpenOrders(ctx, u.ID); err != nil {
		return store.Order{}, err
	}
	o, err := s.Store.CreateOrder(ctx, store.Order{UserID: u.ID, Kind: store.KindTopup, BaseAmount: amount})
	if err != nil {
		return store.Order{}, err
	}
	// A top-up has no choice of payment method - go straight to the card.
	if err := s.Store.ChooseCard(ctx, o.ID, u.ID); err != nil {
		return store.Order{}, err
	}
	return s.Store.GetOrder(ctx, o.ID)
}

// ── discount codes ──────────────────────────────────────────────────────────

// ApplyCode checks a code against every rule and puts it on the draft.
// Checking here gives the customer an immediate answer; the code is only
// actually spent when the order completes.
func (s *Shop) ApplyCode(ctx context.Context, u store.User, orderID int64, code string) (store.Order, error) {
	o, err := s.Store.GetOrder(ctx, orderID)
	if err != nil || o.UserID != u.ID {
		return store.Order{}, ErrNotYours
	}
	if o.Kind == store.KindTopup || o.Status != store.StatusDraft {
		return store.Order{}, ErrCodeNotApplicable
	}
	d, err := s.Store.GetDiscount(ctx, code)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !d.IsActive) {
		return store.Order{}, ErrCodeInvalid
	}
	if err != nil {
		return store.Order{}, err
	}
	if d.ExpiresAt > 0 && s.Now().Unix() > d.ExpiresAt {
		return store.Order{}, ErrCodeExpired
	}
	if d.MaxUses > 0 && d.Used >= d.MaxUses {
		return store.Order{}, ErrCodeUsedUp
	}
	used, err := s.Store.DiscountUsedBy(ctx, d.Code, u.ID)
	if err != nil {
		return store.Order{}, err
	}
	if used {
		return store.Order{}, ErrCodeAlreadyUsed
	}
	return s.Store.ApplyDiscount(ctx, o.ID, u.ID, d.Code, d.Apply(o.BaseAmount))
}

// ── paying ──────────────────────────────────────────────────────────────────

func (s *Shop) CardConfigured(ctx context.Context) bool {
	return strings.TrimSpace(s.Setting(ctx, SettingCardNumber)) != ""
}

// ChooseCard moves a draft to "send your receipt".
func (s *Shop) ChooseCard(ctx context.Context, u store.User, orderID int64) (store.Order, error) {
	if !s.CardConfigured(ctx) {
		return store.Order{}, ErrCardNotConfigured
	}
	if err := s.Store.ChooseCard(ctx, orderID, u.ID); err != nil {
		return store.Order{}, err
	}
	return s.Store.GetOrder(ctx, orderID)
}

// SubmitReceipt attaches a receipt photo and hands the order to the admins.
func (s *Shop) SubmitReceipt(ctx context.Context, userID int64, fileID string) (store.Order, error) {
	return s.Store.AttachReceipt(ctx, userID, fileID)
}

// PayWithWallet pays a draft from the balance and delivers it straight away -
// no admin is needed when the money is already in the wallet.
func (s *Shop) PayWithWallet(ctx context.Context, u store.User, orderID int64) (Result, error) {
	o, err := s.Store.PayFromWallet(ctx, orderID, u.ID)
	if err != nil {
		return Result{}, err
	}
	return s.fulfil(ctx, o)
}

// ── admin decisions ─────────────────────────────────────────────────────────

// Approve accepts a receipt and delivers the order. Only the first admin to
// approve wins the move to processing; everyone after gets store.ErrConflict
// and nothing is delivered twice.
func (s *Shop) Approve(ctx context.Context, orderID, adminID int64) (Result, error) {
	if err := s.Store.Transition(ctx, orderID,
		[]store.OrderStatus{store.StatusAwaitingReview}, store.StatusProcessing, adminID); err != nil {
		return Result{}, err
	}
	o, err := s.Store.GetOrder(ctx, orderID)
	if err != nil {
		return Result{}, err
	}
	return s.fulfil(ctx, o)
}

func (s *Shop) Reject(ctx context.Context, orderID, adminID int64) (store.Order, error) {
	if err := s.Store.Transition(ctx, orderID,
		[]store.OrderStatus{store.StatusAwaitingReview}, store.StatusRejected, adminID); err != nil {
		return store.Order{}, err
	}
	return s.Store.GetOrder(ctx, orderID)
}

// ── delivery ────────────────────────────────────────────────────────────────

// Result is everything a completed (or failed) delivery produced.
type Result struct {
	Order        store.Order
	Plan         store.Plan
	Service      store.Service
	Account      panel.Account
	Referrer     int64 // who was paid a referral reward, 0 for nobody
	RewardAmount int64
	Failed       bool
	Refunded     bool
	Err          error
}

// fulfil delivers a processing order. A delivery failure is not returned as
// an error: the order is marked failed (and refunded when it was paid from
// the wallet) and reported in Result, because at this point money has moved
// and the caller must tell both the customer and the admins what happened.
func (s *Shop) fulfil(ctx context.Context, o store.Order) (Result, error) {
	res := Result{Order: o}

	fail := func(cause error) (Result, error) {
		refunded, err := s.Store.FailOrder(ctx, o.ID, cause.Error())
		if err != nil {
			return res, fmt.Errorf("delivery failed (%v) and marking the order failed also failed: %w", cause, err)
		}
		res.Failed, res.Refunded, res.Err = true, refunded, cause
		res.Order, _ = s.Store.GetOrder(ctx, o.ID)
		return res, nil
	}

	switch o.Kind {
	case store.KindTopup:
		done, err := s.Store.CompleteTopup(ctx, o.ID)
		if err != nil {
			return fail(err)
		}
		res.Order = done
		return res, nil

	case store.KindPurchase:
		plan, err := s.Store.GetPlan(ctx, o.PlanID)
		if err != nil {
			return fail(fmt.Errorf("plan %d is gone: %w", o.PlanID, err))
		}
		res.Plan = plan
		username, acc, err := s.createAccount(ctx, o.UserID, planGrant(plan, o.ID))
		if err != nil {
			return fail(err)
		}
		sv, err := s.Store.CreateService(ctx, o.UserID, username, plan.ID, false)
		if err != nil {
			// The account exists on the panel but not here. Name it in the
			// failure so an admin can find it instead of it silently leaking.
			return fail(fmt.Errorf("panel account %s was created but could not be recorded: %w", username, err))
		}
		res.Service, res.Account = sv, acc

	case store.KindRenew:
		plan, err := s.Store.GetPlan(ctx, o.PlanID)
		if err != nil {
			return fail(fmt.Errorf("plan %d is gone: %w", o.PlanID, err))
		}
		sv, err := s.Store.GetService(ctx, o.ServiceID)
		if err != nil {
			return fail(fmt.Errorf("service %d is gone: %w", o.ServiceID, err))
		}
		acc, err := s.Panel.Renew(ctx, sv.PanelUsername, planGrant(plan, o.ID), s.Now())
		if err != nil {
			return fail(err)
		}
		res.Plan, res.Service, res.Account = plan, sv, acc

	default:
		return fail(fmt.Errorf("unknown order kind %q", o.Kind))
	}

	if err := s.Store.CompleteOrder(ctx, o.ID); err != nil {
		return res, fmt.Errorf("delivered, but closing the order failed: %w", err)
	}
	res.Order, _ = s.Store.GetOrder(ctx, o.ID)

	// Referral rewards follow a real purchase, never a top-up: otherwise a
	// customer could top up, get their friend rewarded, and ask for a refund.
	reward := s.SettingInt(ctx, SettingReferralReward)
	if referrer, err := s.Store.RewardReferrer(ctx, o.UserID, reward, o.ID); err == nil && referrer > 0 {
		res.Referrer, res.RewardAmount = referrer, reward
	}
	return res, nil
}

func planGrant(p store.Plan, orderID int64) panel.Grant {
	return panel.Grant{DataGB: p.DataGB, Days: p.Days, Note: fmt.Sprintf("rapidobot order #%d", orderID)}
}

// createAccount picks a free panel username for the customer. The name
// carries the Telegram id, so an admin looking at the panel can always tell
// whose account it is, plus a short random tail that makes a clash with an
// existing account vanishingly unlikely - and retried if it happens anyway.
func (s *Shop) createAccount(ctx context.Context, userID int64, g panel.Grant) (string, panel.Account, error) {
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		username := fmt.Sprintf("rb%d_%s", userID, randomTail(4))
		acc, err := s.Panel.CreateAccount(ctx, username, g, s.Now())
		if errors.Is(err, panel.ErrExists) {
			lastErr = err
			continue
		}
		if err != nil {
			return "", panel.Account{}, err
		}
		return username, acc, nil
	}
	return "", panel.Account{}, fmt.Errorf("could not find a free username: %w", lastErr)
}

const tailAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

func randomTail(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(tailAlphabet))))
		if err != nil {
			b.WriteByte('x')
			continue
		}
		b.WriteByte(tailAlphabet[idx.Int64()])
	}
	return b.String()
}

// ── free trial ──────────────────────────────────────────────────────────────

func (s *Shop) TrialEnabled(ctx context.Context) bool {
	return s.Setting(ctx, SettingTrialEnabled) == "1"
}

// ClaimTrial gives the customer their one free account.
func (s *Shop) ClaimTrial(ctx context.Context, u store.User) (store.Service, panel.Account, error) {
	if !s.TrialEnabled(ctx) {
		return store.Service{}, panel.Account{}, ErrTrialDisabled
	}
	claimed, err := s.Store.ClaimTrial(ctx, u.ID)
	if err != nil {
		return store.Service{}, panel.Account{}, err
	}
	if !claimed {
		return store.Service{}, panel.Account{}, ErrTrialUsed
	}
	grant := panel.Grant{
		DataGB: s.SettingInt(ctx, SettingTrialGB),
		Hours:  s.SettingInt(ctx, SettingTrialHours),
		Note:   "rapidobot free trial",
	}
	username, acc, err := s.createAccount(ctx, u.ID, grant)
	if err != nil {
		// The panel failed, not the customer - give the trial back.
		_ = s.Store.ReleaseTrial(ctx, u.ID)
		return store.Service{}, panel.Account{}, err
	}
	sv, err := s.Store.CreateService(ctx, u.ID, username, 0, true)
	return sv, acc, err
}

// ── reads ───────────────────────────────────────────────────────────────────

// ServiceAccount returns a customer's service with its live panel state.
func (s *Shop) ServiceAccount(ctx context.Context, u store.User, serviceID int64) (store.Service, panel.Account, error) {
	sv, err := s.Store.GetService(ctx, serviceID)
	if err != nil || sv.UserID != u.ID {
		return store.Service{}, panel.Account{}, ErrNotYours
	}
	acc, err := s.Panel.GetAccount(ctx, sv.PanelUsername)
	return sv, acc, err
}

// RecoverInterrupted fails any order left in processing by a crash or a
// restart mid-delivery - refunding wallet payments - and returns them so
// the admins can check each one by hand.
func (s *Shop) RecoverInterrupted(ctx context.Context) ([]store.Order, error) {
	stuck, err := s.Store.ListOrdersByStatus(ctx, store.StatusProcessing, 1000)
	if err != nil {
		return nil, err
	}
	var out []store.Order
	for _, o := range stuck {
		if _, err := s.Store.FailOrder(ctx, o.ID, "interrupted by a restart during delivery - check the panel"); err == nil {
			o, _ = s.Store.GetOrder(ctx, o.ID)
			out = append(out, o)
		}
	}
	return out, nil
}
