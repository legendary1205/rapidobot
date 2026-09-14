package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/skip2/go-qrcode"

	"github.com/legendary1205/rapidobot/internal/panel"
	"github.com/legendary1205/rapidobot/internal/shop"
	"github.com/legendary1205/rapidobot/internal/store"
)

func (b *Bot) sendMainMenu(ctx context.Context, u store.User) {
	b.send(ctx, u.ID, txtWelcome(u.FirstName), kbMainMenu(b.isAdmin(u.ID), b.shop.TrialEnabled(ctx)))
}

func id(parts []string, i int) int64 {
	if i >= len(parts) {
		return 0
	}
	n, _ := strconv.ParseInt(parts[i], 10, 64)
	return n
}

func (b *Bot) onCustomerCallback(ctx context.Context, u store.User, q *models.CallbackQuery, parts []string, msgID int) {
	answered := false
	answer := func(text string, alert bool) {
		if !answered {
			b.answer(ctx, q.ID, text, alert)
			answered = true
		}
	}
	defer answer("", false)

	switch parts[0] {
	case "m":
		b.sessions.clear(u.ID)
		b.show(ctx, u.ID, msgID, txtWelcome(u.FirstName), kbMainMenu(b.isAdmin(u.ID), b.shop.TrialEnabled(ctx)))

	case "buy":
		b.showPlans(ctx, u, msgID, "p", txtChoosePlan)

	case "p": // plan chosen -> checkout
		o, plan, err := b.shop.StartPurchase(ctx, u, id(parts, 1))
		if err != nil {
			answer(txtPlanGone, true)
			return
		}
		b.showCheckout(ctx, u, msgID, o, plan)

	case "dc": // wants to type a discount code
		s := b.sessions.start(u.ID, "code")
		s.orderID = id(parts, 1)
		b.show(ctx, u.ID, msgID, txtAskCode, nil)

	case "pc": // pay by card
		o, err := b.shop.ChooseCard(ctx, u, id(parts, 1))
		switch {
		case errors.Is(err, shop.ErrCardNotConfigured):
			answer(txtCardMissing, true)
			return
		case err != nil:
			answer(txtOrderMoved, true)
			return
		}
		b.show(ctx, u.ID, msgID, b.cardText(ctx, o), kb(row(btn("❌ انصراف", "m"))))

	case "pw": // pay from wallet
		res, err := b.shop.PayWithWallet(ctx, u, id(parts, 1))
		switch {
		case errors.Is(err, store.ErrInsufficientBalance):
			answer(txtInsufficient, true)
			return
		case err != nil:
			answer(txtOrderMoved, true)
			return
		}
		b.show(ctx, u.ID, msgID, "⏳ در حال ساخت سرویس...", nil)
		b.deliver(ctx, res)

	case "svc":
		b.showServices(ctx, u, msgID)

	case "s":
		b.showService(ctx, u, msgID, id(parts, 1))

	case "sl": // link + QR as a fresh message, so it stays in the chat
		sv, acc, err := b.shop.ServiceAccount(ctx, u, id(parts, 1))
		if err != nil {
			answer(txtPanelDown, true)
			return
		}
		b.sendConfig(ctx, u.ID, "لینک سرویس "+sv.PanelUsername, acc, dataGBOf(acc))

	case "rn": // renew -> pick a plan
		b.showPlans(ctx, u, msgID, fmt.Sprintf("rp:%d", id(parts, 1)), txtChooseRenew)

	case "rp":
		o, plan, err := b.shop.StartRenew(ctx, u, id(parts, 1), id(parts, 2))
		if err != nil {
			answer(txtPlanGone, true)
			return
		}
		b.showCheckout(ctx, u, msgID, o, plan)

	case "wal":
		fresh, _ := b.st.GetUser(ctx, u.ID)
		b.show(ctx, u.ID, msgID, txtWallet(fresh.Balance), kbWallet())

	case "top":
		if !b.shop.CardConfigured(ctx) {
			answer(txtCardMissing, true)
			return
		}
		b.sessions.start(u.ID, "topup")
		b.show(ctx, u.ID, msgID, txtAskTopup, nil)

	case "trial":
		b.claimTrial(ctx, u, msgID, answer)

	case "ref":
		count, _ := b.st.CountReferrals(ctx, u.ID)
		link := fmt.Sprintf("https://t.me/%s?start=ref_%s", b.botUsername, u.ReferralCode)
		b.show(ctx, u.ID, msgID, txtReferral(link, count, b.shop.SettingInt(ctx, shop.SettingReferralReward)), kbBackToMenu())

	case "sup":
		if s := b.shop.Setting(ctx, shop.SettingSupport); s != "" {
			b.show(ctx, u.ID, msgID, txtSupport(s), kbBackToMenu())
		} else {
			answer(txtNoSupport, true)
		}
	}
}

func (b *Bot) showPlans(ctx context.Context, u store.User, msgID int, action, title string) {
	plans, err := b.st.ListPlans(ctx, true)
	if err != nil || len(plans) == 0 {
		b.show(ctx, u.ID, msgID, txtNoPlans, kbBackToMenu())
		return
	}
	b.show(ctx, u.ID, msgID, title, kbPlans(plans, u, action))
}

func (b *Bot) showCheckout(ctx context.Context, u store.User, msgID int, o store.Order, plan store.Plan) {
	fresh, err := b.st.GetUser(ctx, u.ID)
	if err != nil {
		fresh = u
	}
	text := txtPlanCheckout(plan.Name, plan.DataGB, plan.Days, o.BaseAmount, o.Discount, o.Amount, o.DiscountCode, fresh.Balance)
	b.show(ctx, u.ID, msgID, text, kbCheckout(o.ID, fresh.Balance >= o.Amount))
}

func (b *Bot) cardText(ctx context.Context, o store.Order) string {
	return txtCardInstructions(o.Amount,
		b.shop.Setting(ctx, shop.SettingCardNumber),
		b.shop.Setting(ctx, shop.SettingCardHolder),
		b.shop.Setting(ctx, shop.SettingBankName), o.ID)
}

func (b *Bot) onCustomerInput(ctx context.Context, u store.User, s *session, text string) {
	switch s.step {
	case "code":
		o, err := b.shop.ApplyCode(ctx, u, s.orderID, text)
		if err != nil {
			b.send(ctx, u.ID, txtCodeError(err)+"\nکد دیگری بفرستید یا /cancel", nil)
			return
		}
		b.sessions.clear(u.ID)
		plan, err := b.st.GetPlan(ctx, o.PlanID)
		if err != nil {
			b.send(ctx, u.ID, txtPlanGone, kbBackToMenu())
			return
		}
		b.send(ctx, u.ID, "✅ کد تخفیف اعمال شد.", nil)
		b.showCheckout(ctx, u, 0, o, plan)

	case "topup":
		amount, ok := parseAmount(text)
		if !ok {
			b.send(ctx, u.ID, txtNotANum, nil)
			return
		}
		o, err := b.shop.StartTopup(ctx, u, amount)
		if errors.Is(err, shop.ErrTopupRange) {
			b.send(ctx, u.ID, txtTopupRange(
				b.shop.SettingInt(ctx, shop.SettingTopupMin), b.shop.SettingInt(ctx, shop.SettingTopupMax)), nil)
			return
		}
		if err != nil {
			b.send(ctx, u.ID, txtSomethingWent, kbBackToMenu())
			return
		}
		b.sessions.clear(u.ID)
		b.send(ctx, u.ID, b.cardText(ctx, o), kb(row(btn("❌ انصراف", "m"))))

	default:
		b.sessions.clear(u.ID)
		b.sendMainMenu(ctx, u)
	}
}

// onReceipt takes a photo as the receipt for the customer's open card order
// and puts it in front of every admin with approve/reject buttons.
func (b *Bot) onReceipt(ctx context.Context, u store.User, m *models.Message) {
	// Telegram sends several sizes; the last is the largest.
	fileID := m.Photo[len(m.Photo)-1].FileID
	o, err := b.shop.SubmitReceipt(ctx, u.ID, fileID)
	if errors.Is(err, store.ErrNotFound) {
		b.send(ctx, u.ID, txtNoOpenReceipt, kbBackToMenu())
		return
	}
	if err != nil {
		b.send(ctx, u.ID, txtSomethingWent, kbBackToMenu())
		return
	}
	b.sessions.clear(u.ID)
	b.send(ctx, u.ID, txtReceiptThanks, kbBackToMenu())

	caption := b.reviewCaption(ctx, u, o)
	for adminID := range b.admins {
		if _, err := b.tg.SendPhoto(ctx, &tgbot.SendPhotoParams{
			ChatID: adminID, Photo: &models.InputFileString{Data: fileID},
			Caption: caption, ParseMode: models.ParseModeHTML, ReplyMarkup: kbReview(o.ID),
		}); err != nil {
			b.log.Warn("could not send receipt to admin", "admin", adminID, "order", o.ID, "err", err)
		}
	}
}

func (b *Bot) reviewCaption(ctx context.Context, u store.User, o store.Order) string {
	what := "شارژ کیف پول"
	if o.Kind != store.KindTopup {
		plan, _ := b.st.GetPlan(ctx, o.PlanID)
		verb := "خرید"
		if o.Kind == store.KindRenew {
			verb = "تمدید"
		}
		what = fmt.Sprintf("%s «%s»", verb, esc(plan.Name))
	}
	who := esc(u.FirstName)
	if u.Username != "" {
		who += " (@" + esc(u.Username) + ")"
	}
	s := fmt.Sprintf("🧾 <b>رسید جدید</b>\n\n🔖 سفارش: <code>%d</code>\n👤 %s\n🆔 <code>%d</code>\n📦 %s\n💰 مبلغ: <b>%s</b>",
		o.ID, who, u.ID, what, toman(o.Amount))
	if o.Discount > 0 {
		s += fmt.Sprintf("\n🎟 کد %s (-%s)", esc(o.DiscountCode), toman(o.Discount))
	}
	if u.IsAgent {
		s += "\n🤝 نماینده"
	}
	return s
}

// deliver tells the customer (and, on failure, the admins) how a delivery
// went, and pays out any referral reward it triggered.
func (b *Bot) deliver(ctx context.Context, res shop.Result) {
	o := res.Order
	if res.Failed {
		b.send(ctx, o.UserID, txtDeliveryFailed(o.ID, res.Refunded), kbBackToMenu())
		b.notifyAdmins(ctx, fmt.Sprintf("🚨 <b>ساخت سرویس ناموفق بود</b>\n🔖 سفارش <code>%d</code>\n👤 <code>%d</code>\n❗️ %s",
			o.ID, o.UserID, esc(fmt.Sprint(res.Err))), nil)
		return
	}

	switch o.Kind {
	case store.KindTopup:
		fresh, _ := b.st.GetUser(ctx, o.UserID)
		b.send(ctx, o.UserID, txtTopupDone(o.Amount, fresh.Balance), kbBackToMenu())
	case store.KindPurchase:
		b.sendConfig(ctx, o.UserID, "سرویس شما آماده است", res.Account, res.Plan.DataGB)
	case store.KindRenew:
		b.sendConfig(ctx, o.UserID, "سرویس شما تمدید شد", res.Account, res.Plan.DataGB)
	}

	if res.Referrer > 0 {
		b.send(ctx, res.Referrer, txtReferralReward(res.RewardAmount), nil)
	}
}

// sendConfig sends the subscription link with a scannable QR code. If the QR
// cannot be rendered the link alone still goes out - it is what matters.
func (b *Bot) sendConfig(ctx context.Context, chatID int64, title string, acc panel.Account, dataGB int64) {
	expire := "نامحدود"
	if !acc.NoExpiry() {
		expire = remaining(*acc.Expire, b.now())
	}
	caption := txtDelivered(title, acc.SubscriptionURL, dataGB, expire)

	png, err := qrcode.Encode(acc.SubscriptionURL, qrcode.Medium, 512)
	if err == nil {
		_, err = b.tg.SendPhoto(ctx, &tgbot.SendPhotoParams{
			ChatID: chatID, Photo: &models.InputFileUpload{Filename: "subscription.png", Data: bytes.NewReader(png)},
			Caption: caption, ParseMode: models.ParseModeHTML, ReplyMarkup: kbBackToMenu(),
		})
	}
	if err != nil {
		b.send(ctx, chatID, caption, kbBackToMenu())
	}
}

func dataGBOf(acc panel.Account) int64 {
	if acc.Unlimited() {
		return 0
	}
	return *acc.DataLimit / (1024 * 1024 * 1024)
}

func (b *Bot) showServices(ctx context.Context, u store.User, msgID int) {
	svcs, err := b.st.ListServices(ctx, u.ID)
	if err != nil || len(svcs) == 0 {
		b.show(ctx, u.ID, msgID, txtNoServices, kbBackToMenu())
		return
	}
	b.show(ctx, u.ID, msgID, txtServicesTitle, kbServices(svcs))
}

func (b *Bot) showService(ctx context.Context, u store.User, msgID int, serviceID int64) {
	sv, acc, err := b.shop.ServiceAccount(ctx, u, serviceID)
	if errors.Is(err, shop.ErrNotYours) {
		b.show(ctx, u.ID, msgID, txtNoServices, kbBackToMenu())
		return
	}
	if err != nil {
		b.show(ctx, u.ID, msgID, txtPanelDown, kb(row(btn("🔙 سرویس‌ها", "svc"))))
		return
	}

	usage := bytesHuman(acc.UsedTraffic)
	if acc.Unlimited() {
		usage += " از نامحدود"
	} else {
		usage = fmt.Sprintf("%s از %s\n%s", bytesHuman(acc.UsedTraffic), bytesHuman(*acc.DataLimit), usageBar(acc.UsedTraffic, *acc.DataLimit))
	}
	expire := "نامحدود"
	if !acc.NoExpiry() {
		expire = remaining(*acc.Expire, b.now())
	}
	text := fmt.Sprintf("📦 <b>%s</b>\n\nوضعیت: %s\n📊 مصرف: %s\n⏳ باقی‌مانده: %s",
		esc(sv.PanelUsername), statusLabel(acc.Status), usage, expire)
	if sv.IsTrial {
		text += "\n\n🎁 این یک اکانت تست است."
	}
	b.show(ctx, u.ID, msgID, text, kbService(sv.ID, !sv.IsTrial))
}

func (b *Bot) claimTrial(ctx context.Context, u store.User, msgID int, answer func(string, bool)) {
	sv, acc, err := b.shop.ClaimTrial(ctx, u)
	switch {
	case errors.Is(err, shop.ErrTrialDisabled):
		answer(txtTrialDisabled, true)
		return
	case errors.Is(err, shop.ErrTrialUsed):
		answer(txtTrialUsed, true)
		return
	case err != nil:
		b.log.Warn("trial failed", "user", u.ID, "err", err)
		answer(txtPanelDown, true)
		return
	}
	b.show(ctx, u.ID, msgID, "🎁 اکانت تست شما ساخته شد 👇", nil)
	b.sendConfig(ctx, u.ID, "اکانت تست "+sv.PanelUsername, acc, b.shop.SettingInt(ctx, shop.SettingTrialGB))
}
