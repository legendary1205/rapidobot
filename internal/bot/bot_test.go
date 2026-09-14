package bot

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/legendary1205/rapidobot/internal/panel"
	"github.com/legendary1205/rapidobot/internal/shop"
	"github.com/legendary1205/rapidobot/internal/store"
)

// recorder captures everything the bot would have sent to Telegram.
type recorder struct {
	mu      sync.Mutex
	texts   map[int64][]string
	photos  map[int64][]*tgbot.SendPhotoParams
	answers []*tgbot.AnswerCallbackQueryParams
}

func newRecorder() *recorder {
	return &recorder{texts: map[int64][]string{}, photos: map[int64][]*tgbot.SendPhotoParams{}}
}

func chatOf(v any) int64 { n, _ := v.(int64); return n }

func (r *recorder) SendMessage(_ context.Context, p *tgbot.SendMessageParams) (*models.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts[chatOf(p.ChatID)] = append(r.texts[chatOf(p.ChatID)], p.Text)
	return &models.Message{ID: len(r.texts[chatOf(p.ChatID)])}, nil
}

func (r *recorder) SendPhoto(_ context.Context, p *tgbot.SendPhotoParams) (*models.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.photos[chatOf(p.ChatID)] = append(r.photos[chatOf(p.ChatID)], p)
	return &models.Message{ID: 1}, nil
}

func (r *recorder) EditMessageText(_ context.Context, p *tgbot.EditMessageTextParams) (*models.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.texts[chatOf(p.ChatID)] = append(r.texts[chatOf(p.ChatID)], p.Text)
	return &models.Message{ID: p.MessageID}, nil
}

func (r *recorder) EditMessageReplyMarkup(context.Context, *tgbot.EditMessageReplyMarkupParams) (*models.Message, error) {
	return &models.Message{}, nil
}

func (r *recorder) AnswerCallbackQuery(_ context.Context, p *tgbot.AnswerCallbackQueryParams) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers = append(r.answers, p)
	return true, nil
}

func (r *recorder) lastText(chat int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.texts[chat]
	if len(t) == 0 {
		return ""
	}
	return t[len(t)-1]
}

func (r *recorder) allText(chat int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.texts[chat], "\n---\n")
}

// memPanel creates accounts in memory.
type memPanel struct{ n int }

func (m *memPanel) CreateAccount(_ context.Context, username string, g panel.Grant, now time.Time) (panel.Account, error) {
	m.n++
	limit := g.DataGB * 1024 * 1024 * 1024
	exp := now.Add(time.Duration(g.Days) * 24 * time.Hour).Unix()
	return panel.Account{Username: username, Status: "active", DataLimit: &limit, Expire: &exp, SubscriptionURL: "https://panel/sub/" + username}, nil
}
func (m *memPanel) GetAccount(_ context.Context, u string) (panel.Account, error) {
	return panel.Account{Username: u, Status: "active", SubscriptionURL: "https://panel/sub/" + u}, nil
}
func (m *memPanel) Renew(ctx context.Context, u string, g panel.Grant, now time.Time) (panel.Account, error) {
	return m.CreateAccount(ctx, u, g, now)
}

const (
	adminID    int64 = 1000
	customerID int64 = 2000
)

type fixture struct {
	t     *testing.T
	ctx   context.Context
	bot   *Bot
	rec   *recorder
	st    *store.Store
	panel *memPanel
	plan  store.Plan
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mp := &memPanel{}
	sh := shop.New(st, mp)
	rec := newRecorder()
	b := New(rec, sh, []int64{adminID}, "testshop_bot", slog.New(slog.NewTextHandler(io.Discard, nil)))
	st.SetSetting(ctx, shop.SettingCardNumber, "6037991111111111")
	plan, err := st.CreatePlan(ctx, store.Plan{Name: "30GB", DataGB: 30, Days: 30, Price: 120_000})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, ctx: ctx, bot: b, rec: rec, st: st, panel: mp, plan: plan}
}

func (f *fixture) text(from int64, text string) {
	f.bot.Handle(f.ctx, nil, &models.Update{Message: &models.Message{
		ID: 1, Text: text, From: &models.User{ID: from, FirstName: "Ali"},
		Chat: models.Chat{ID: from, Type: models.ChatTypePrivate},
	}})
}

func (f *fixture) photo(from int64) {
	f.bot.Handle(f.ctx, nil, &models.Update{Message: &models.Message{
		ID: 2, From: &models.User{ID: from, FirstName: "Ali"},
		Chat:  models.Chat{ID: from, Type: models.ChatTypePrivate},
		Photo: []models.PhotoSize{{FileID: "small"}, {FileID: "receipt-large"}},
	}})
}

func (f *fixture) tap(from int64, data string) {
	f.bot.Handle(f.ctx, nil, &models.Update{CallbackQuery: &models.CallbackQuery{
		ID: "cb", From: models.User{ID: from, FirstName: "Ali"}, Data: data,
		Message: models.MaybeInaccessibleMessage{Type: models.MaybeInaccessibleMessageTypeMessage, Message: &models.Message{ID: 7}},
	}})
}

func (f *fixture) latestOrder(userID int64) store.Order {
	f.t.Helper()
	for _, st := range []store.OrderStatus{store.StatusAwaitingReview, store.StatusAwaitingReceipt, store.StatusDraft, store.StatusCompleted} {
		if orders, _ := f.st.ListOrdersByStatus(f.ctx, st, 100); len(orders) > 0 {
			for i := len(orders) - 1; i >= 0; i-- {
				if orders[i].UserID == userID {
					return orders[i]
				}
			}
		}
	}
	f.t.Fatalf("no order for user %d", userID)
	return store.Order{}
}

func TestFullCardPurchaseThroughTheBot(t *testing.T) {
	f := newFixture(t)
	f.text(customerID, "/start")
	f.tap(customerID, "buy")
	f.tap(customerID, "p:1")
	o := f.latestOrder(customerID)
	f.tap(customerID, "pc:"+itoa(o.ID))
	if !strings.Contains(f.rec.lastText(customerID), "6037991111111111") {
		t.Fatalf("card instructions did not show the card number:\n%s", f.rec.lastText(customerID))
	}

	f.photo(customerID)
	adminPhotos := f.rec.photos[adminID]
	if len(adminPhotos) != 1 {
		t.Fatalf("admin received %d receipts, want 1", len(adminPhotos))
	}
	if got := adminPhotos[0].Photo.(*models.InputFileString).Data; got != "receipt-large" {
		t.Errorf("admin was sent photo %q, want the largest size", got)
	}

	f.tap(adminID, "ok:"+itoa(o.ID))
	delivered := f.rec.photos[customerID]
	if len(delivered) != 1 || !strings.Contains(delivered[0].Caption, "https://panel/sub/") {
		t.Fatalf("customer did not receive a subscription link: %+v", delivered)
	}
	if got, _ := f.st.GetOrder(f.ctx, o.ID); got.Status != store.StatusCompleted {
		t.Errorf("order status = %s, want completed", got.Status)
	}
}

func TestCustomerCannotApproveTheirOwnReceipt(t *testing.T) {
	f := newFixture(t)
	f.text(customerID, "/start")
	f.tap(customerID, "p:1")
	o := f.latestOrder(customerID)
	f.tap(customerID, "pc:"+itoa(o.ID))
	f.photo(customerID)

	// A crafted callback: the approve button is never shown to a customer,
	// but nothing stops a client from sending the data anyway.
	f.tap(customerID, "ok:"+itoa(o.ID))
	f.tap(customerID, "a:ua:"+itoa(customerID)) // and try to make themselves an agent

	if f.panel.n != 0 {
		t.Errorf("a customer's crafted approve created %d accounts", f.panel.n)
	}
	if got, _ := f.st.GetOrder(f.ctx, o.ID); got.Status != store.StatusAwaitingReview {
		t.Errorf("order status = %s, want still awaiting_review", got.Status)
	}
	if u, _ := f.st.GetUser(f.ctx, customerID); u.IsAgent {
		t.Error("a customer promoted themselves to agent")
	}
}

func TestBlockedCustomerIsIgnored(t *testing.T) {
	f := newFixture(t)
	f.text(customerID, "/start")
	f.st.SetBlocked(f.ctx, customerID, true)
	before := len(f.rec.texts[customerID])
	f.text(customerID, "/start")
	f.tap(customerID, "buy")
	if after := len(f.rec.texts[customerID]); after != before {
		t.Errorf("a blocked customer got %d more messages", after-before)
	}
}

func TestTopupAcceptsPersianDigits(t *testing.T) {
	f := newFixture(t)
	f.text(customerID, "/start")
	f.tap(customerID, "top")
	f.text(customerID, "۱۵۰,۰۰۰")
	last := f.rec.lastText(customerID)
	if !strings.Contains(last, "150,000") {
		t.Fatalf("a Persian-typed amount was not understood:\n%s", last)
	}
	o := f.latestOrder(customerID)
	if o.Kind != store.KindTopup || o.Amount != 150_000 {
		t.Errorf("order = %+v, want a 150000 top-up", o)
	}
}

func TestReceiptWithoutAnOrderIsExplained(t *testing.T) {
	f := newFixture(t)
	f.text(customerID, "/start")
	f.photo(customerID)
	if len(f.rec.photos[adminID]) != 0 {
		t.Error("a stray photo was forwarded to admins as a receipt")
	}
	if !strings.Contains(f.rec.lastText(customerID), "در انتظار رسید") {
		t.Errorf("customer was not told there is no open order: %q", f.rec.lastText(customerID))
	}
}

func TestNamesAreEscaped(t *testing.T) {
	f := newFixture(t)
	f.bot.Handle(f.ctx, nil, &models.Update{Message: &models.Message{
		Text: "/start", From: &models.User{ID: 3000, FirstName: "<b>x</b> & co"},
		Chat: models.Chat{ID: 3000, Type: models.ChatTypePrivate},
	}})
	got := f.rec.lastText(3000)
	if strings.Contains(got, "<b>x</b> & co") || !strings.Contains(got, "&lt;b&gt;x&lt;/b&gt; &amp; co") {
		t.Errorf("a name with HTML was not escaped:\n%s", got)
	}
}

func TestAdminCanCreateAPlanInPersianDigits(t *testing.T) {
	f := newFixture(t)
	f.text(adminID, "/start")
	f.tap(adminID, "a:pladd")
	for _, in := range []string{"۵۰ گیگ", "۵۰", "۳۰", "۲۰۰,۰۰۰", "۱۸۰۰۰۰"} {
		f.text(adminID, in)
	}
	plans, _ := f.st.ListPlans(f.ctx, false)
	var got store.Plan
	for _, p := range plans {
		if p.Name == "۵۰ گیگ" {
			got = p
		}
	}
	if got.DataGB != 50 || got.Days != 30 || got.Price != 200_000 || got.AgentPrice != 180_000 {
		t.Errorf("plan = %+v, want 50GB / 30 days / 200000 / agent 180000", got)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestFormatHelpers(t *testing.T) {
	if got := toman(1250000); got != "1,250,000 تومان" {
		t.Errorf("toman = %q", got)
	}
	if got := normalizeDigits("۱۲٬۳۴۵"); got != "12345" {
		t.Errorf("normalizeDigits = %q", got)
	}
	if n, ok := parseAmount("٥٠٠"); !ok || n != 500 {
		t.Errorf("parseAmount arabic digits = %d %v", n, ok)
	}
	if _, ok := parseAmount("abc"); ok {
		t.Error("parseAmount accepted text")
	}
	if got := usageBar(5, 10); got != "▰▰▰▰▰▱▱▱▱▱" {
		t.Errorf("usageBar = %q", got)
	}
}

const helperID int64 = 4000

func TestOwnerAddsAnAdminWhoCanThenReviewReceipts(t *testing.T) {
	f := newFixture(t)
	f.text(helperID, "/start") // the new admin has opened the bot
	f.text(adminID, "/start")
	f.tap(adminID, "a:admadd")
	f.text(adminID, strconv.FormatInt(helperID, 10))

	if !f.st.IsAdmin(f.ctx, helperID) {
		t.Fatal("owner's add did not grant admin rights")
	}

	// A customer now buys; the new admin must receive the receipt too.
	f.text(customerID, "/start")
	f.tap(customerID, "p:1")
	o := f.latestOrder(customerID)
	f.tap(customerID, "pc:"+itoa(o.ID))
	f.photo(customerID)
	if len(f.rec.photos[helperID]) != 1 {
		t.Fatalf("added admin received %d receipts, want 1", len(f.rec.photos[helperID]))
	}

	// ...and approve it.
	f.tap(helperID, "ok:"+itoa(o.ID))
	if got, _ := f.st.GetOrder(f.ctx, o.ID); got.Status != store.StatusCompleted {
		t.Errorf("order status after the added admin approved = %s, want completed", got.Status)
	}
}

func TestAddedAdminCannotManageAdmins(t *testing.T) {
	f := newFixture(t)
	f.text(helperID, "/start")
	if _, err := f.st.AddAdmin(f.ctx, helperID, adminID); err != nil {
		t.Fatal(err)
	}
	const outsider int64 = 5000
	f.text(outsider, "/start")

	// Every admin-management action, crafted directly since the button is hidden.
	f.tap(helperID, "a:admadd")
	f.text(helperID, strconv.FormatInt(outsider, 10))
	if f.st.IsAdmin(f.ctx, outsider) {
		t.Error("an added admin was able to create another admin")
	}

	other := int64(6000)
	f.st.AddAdmin(f.ctx, other, adminID)
	f.tap(helperID, "a:admdel:"+strconv.FormatInt(other, 10))
	if !f.st.IsAdmin(f.ctx, other) {
		t.Error("an added admin was able to remove another admin")
	}

	// And they never see the button.
	if strings.Contains(f.rec.allText(helperID), "مدیریت ادمین‌ها") {
		t.Error("the admin-management screen was shown to a non-owner")
	}
}

func TestRemovedAdminLosesAccessImmediately(t *testing.T) {
	f := newFixture(t)
	f.text(helperID, "/start")
	f.st.AddAdmin(f.ctx, helperID, adminID)

	f.text(adminID, "/start")
	f.tap(adminID, "a:admdel:"+strconv.FormatInt(helperID, 10))
	if f.st.IsAdmin(f.ctx, helperID) {
		t.Fatal("owner could not remove the admin")
	}

	// Their very next admin action must be refused.
	f.text(customerID, "/start")
	f.tap(customerID, "p:1")
	o := f.latestOrder(customerID)
	f.tap(customerID, "pc:"+itoa(o.ID))
	f.photo(customerID)
	f.tap(helperID, "ok:"+itoa(o.ID))
	if got, _ := f.st.GetOrder(f.ctx, o.ID); got.Status != store.StatusAwaitingReview {
		t.Errorf("a removed admin still approved an order (status %s)", got.Status)
	}
	if len(f.rec.photos[helperID]) != 0 {
		t.Error("a removed admin still receives receipts")
	}
}

func TestOwnersCannotBeAddedOrRemovedFromTheBot(t *testing.T) {
	f := newFixture(t)
	f.text(adminID, "/start")
	f.tap(adminID, "a:admadd")
	f.text(adminID, strconv.FormatInt(adminID, 10)) // try to add an owner as a plain admin
	if f.st.IsAdmin(f.ctx, adminID) {
		t.Error("an owner was stored as a removable admin")
	}
	f.tap(adminID, "a:admdel:"+strconv.FormatInt(adminID, 10))
	if !f.bot.isAdmin(f.ctx, adminID) {
		t.Error("an owner lost admin rights from inside the bot")
	}
}
