// Package bot is the Telegram side: menus, conversations and messages. The
// selling rules themselves live in package shop.
package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/legendary1205/rapidobot/internal/shop"
	"github.com/legendary1205/rapidobot/internal/store"
)

// Sender is the part of the Telegram client the bot uses. *tgbot.Bot
// satisfies it; tests pass a recorder.
type Sender interface {
	SendMessage(ctx context.Context, p *tgbot.SendMessageParams) (*models.Message, error)
	SendPhoto(ctx context.Context, p *tgbot.SendPhotoParams) (*models.Message, error)
	EditMessageText(ctx context.Context, p *tgbot.EditMessageTextParams) (*models.Message, error)
	EditMessageReplyMarkup(ctx context.Context, p *tgbot.EditMessageReplyMarkupParams) (*models.Message, error)
	AnswerCallbackQuery(ctx context.Context, p *tgbot.AnswerCallbackQueryParams) (bool, error)
}

type Bot struct {
	tg          Sender
	shop        *shop.Shop
	st          *store.Store
	admins      map[int64]bool
	botUsername string
	log         *slog.Logger
	now         func() time.Time
	sessions    *sessions
}

func New(tg Sender, sh *shop.Shop, admins []int64, botUsername string, log *slog.Logger) *Bot {
	set := make(map[int64]bool, len(admins))
	for _, id := range admins {
		set[id] = true
	}
	return &Bot{
		tg: tg, shop: sh, st: sh.Store, admins: set, botUsername: botUsername, log: log,
		now: time.Now, sessions: newSessions(),
	}
}

func (b *Bot) isAdmin(id int64) bool { return b.admins[id] }

// ReportInterrupted tells the admins about orders the last shutdown left
// mid-delivery. They were already failed (and wallet payments refunded); a
// card purchase may still have created a panel account, so each needs a look.
func (b *Bot) ReportInterrupted(ctx context.Context, orders []store.Order) {
	text := "🚨 <b>سفارش‌های نیمه‌کاره هنگام راه‌اندازی مجدد</b>\n\n" +
		"این سفارش‌ها وسط تحویل قطع شدند و ناموفق علامت خوردند (پرداخت‌های کیف پول برگشت داده شد). لطفاً هر کدام را در پنل بررسی کنید:\n"
	for _, o := range orders {
		text += fmt.Sprintf("\n• سفارش <code>%d</code> - کاربر <code>%d</code> - %s", o.ID, o.UserID, toman(o.Amount))
	}
	b.notifyAdmins(ctx, text, nil)
}

// Handle is the single entry point for every update.
func (b *Bot) Handle(ctx context.Context, _ *tgbot.Bot, u *models.Update) {
	defer func() {
		if p := recover(); p != nil {
			b.log.Error("panic while handling an update", "panic", p)
		}
	}()

	switch {
	case u.CallbackQuery != nil:
		b.onCallback(ctx, u.CallbackQuery)
	case u.Message != nil && u.Message.From != nil && u.Message.Chat.Type == models.ChatTypePrivate:
		b.onMessage(ctx, u.Message)
	}
}

// customer loads (creating on first contact) the person behind an update,
// and reports false for a blocked customer so every handler can stop there.
func (b *Bot) customer(ctx context.Context, from models.User, referral string) (store.User, bool) {
	name := strings.TrimSpace(from.FirstName + " " + from.LastName)
	u, created, err := b.st.UpsertUser(ctx, from.ID, from.Username, name, referral)
	if err != nil {
		b.log.Error("could not load customer", "user", from.ID, "err", err)
		return store.User{}, false
	}
	if created && u.ReferredBy > 0 {
		b.send(ctx, u.ReferredBy, txtReferralJoined(name), nil)
	}
	if u.IsBlocked && !b.isAdmin(u.ID) {
		return u, false
	}
	return u, true
}

func (b *Bot) onMessage(ctx context.Context, m *models.Message) {
	text := strings.TrimSpace(m.Text)
	referral := ""
	if strings.HasPrefix(text, "/start") {
		referral = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(text, "/start")), "ref_")
	}
	u, ok := b.customer(ctx, *m.From, referral)
	if !ok {
		return
	}

	switch {
	case strings.HasPrefix(text, "/start"), text == "/menu":
		b.sessions.clear(u.ID)
		b.sendMainMenu(ctx, u)
		return
	case text == "/admin" && b.isAdmin(u.ID):
		b.sessions.clear(u.ID)
		b.adminHome(ctx, u.ID, 0)
		return
	case text == "/cancel":
		b.sessions.clear(u.ID)
		b.send(ctx, u.ID, txtCancelled, kbBackToMenu())
		return
	}

	if len(m.Photo) > 0 {
		b.onReceipt(ctx, u, m)
		return
	}
	// A receipt sent "as a file" arrives as a document, not a photo. Admins
	// review receipts as photos, so ask for it again the right way instead of
	// silently showing the menu and leaving the customer unsure it arrived.
	if m.Document != nil {
		b.send(ctx, u.ID, txtSendAsPhoto, nil)
		return
	}

	if s, ok := b.sessions.get(u.ID); ok {
		b.onInput(ctx, u, s, text)
		return
	}
	b.sendMainMenu(ctx, u)
}

func (b *Bot) onCallback(ctx context.Context, q *models.CallbackQuery) {
	u, ok := b.customer(ctx, q.From, "")
	if !ok {
		b.answer(ctx, q.ID, "", false)
		return
	}
	msgID := 0
	if q.Message.Message != nil {
		msgID = q.Message.Message.ID
	}
	parts := strings.Split(q.Data, ":")

	if parts[0] == "a" || parts[0] == "ok" || parts[0] == "no" {
		if !b.isAdmin(u.ID) {
			// Menus are hidden from customers, but a crafted callback must
			// still be refused on the server side.
			b.answer(ctx, q.ID, txtNotAllowed, true)
			return
		}
		b.onAdminCallback(ctx, u, q, parts, msgID)
		return
	}
	b.onCustomerCallback(ctx, u, q, parts, msgID)
}

// ── sending ─────────────────────────────────────────────────────────────────

func (b *Bot) send(ctx context.Context, chatID int64, text string, kb models.ReplyMarkup) *models.Message {
	p := &tgbot.SendMessageParams{
		ChatID: chatID, Text: text, ParseMode: models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: tgbot.True()},
	}
	if kb != nil {
		p.ReplyMarkup = kb
	}
	msg, err := b.tg.SendMessage(ctx, p)
	if err != nil {
		b.log.Warn("send failed", "chat", chatID, "err", err)
	}
	return msg
}

// show replaces the menu message in place when there is one, so browsing the
// bot does not bury the chat under a new message per tap. It falls back to
// a new message when editing is impossible (too old, or deleted).
func (b *Bot) show(ctx context.Context, chatID int64, msgID int, text string, kb models.ReplyMarkup) {
	if msgID > 0 {
		p := &tgbot.EditMessageTextParams{
			ChatID: chatID, MessageID: msgID, Text: text, ParseMode: models.ParseModeHTML,
			LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: tgbot.True()},
		}
		if kb != nil {
			p.ReplyMarkup = kb
		}
		_, err := b.tg.EditMessageText(ctx, p)
		if err == nil || strings.Contains(err.Error(), "message is not modified") {
			return
		}
	}
	b.send(ctx, chatID, text, kb)
}

func (b *Bot) answer(ctx context.Context, callbackID, text string, alert bool) {
	_, _ = b.tg.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID, Text: text, ShowAlert: alert,
	})
}

func (b *Bot) notifyAdmins(ctx context.Context, text string, kb models.ReplyMarkup) {
	for id := range b.admins {
		b.send(ctx, id, text, kb)
	}
}

// ── conversations ───────────────────────────────────────────────────────────

// session is a multi-step input in progress ("type the amount", "now the
// price"). It lives in memory on purpose: anything that matters for money
// is in the database, and losing a half-typed plan name on restart is fine.
type session struct {
	step    string
	orderID int64
	target  int64
	fields  map[string]string
	expires time.Time
}

type sessions struct {
	mu sync.Mutex
	m  map[int64]*session
}

func newSessions() *sessions { return &sessions{m: map[int64]*session{}} }

const sessionTTL = 30 * time.Minute

func (s *sessions) start(userID int64, step string) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := &session{step: step, fields: map[string]string{}, expires: time.Now().Add(sessionTTL)}
	s.m[userID] = sess
	return sess
}

func (s *sessions) get(userID int64) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[userID]
	if !ok {
		return nil, false
	}
	if time.Now().After(sess.expires) {
		delete(s.m, userID)
		return nil, false
	}
	sess.expires = time.Now().Add(sessionTTL)
	return sess, true
}

func (s *sessions) clear(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, userID)
}

// onInput routes typed text to whichever conversation is waiting for it.
func (b *Bot) onInput(ctx context.Context, u store.User, s *session, text string) {
	if strings.HasPrefix(s.step, "admin_") {
		if !b.isAdmin(u.ID) {
			b.sessions.clear(u.ID)
			return
		}
		b.onAdminInput(ctx, u, s, text)
		return
	}
	b.onCustomerInput(ctx, u, s, text)
}
