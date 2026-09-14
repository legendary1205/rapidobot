package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/legendary1205/rapidobot/internal/shop"
	"github.com/legendary1205/rapidobot/internal/store"
)

func startOfToday(now time.Time) int64 {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).Unix()
}

func (b *Bot) adminHome(ctx context.Context, adminID int64, msgID int) {
	st, _ := b.st.Stats(ctx, startOfToday(b.now()))
	text := fmt.Sprintf("⚙️ <b>پنل مدیریت</b>\n\n👥 کاربران: %d\n🧾 رسید در انتظار: <b>%d</b>\n💰 فروش امروز: %s",
		st.Users, st.PendingReview, toman(st.RevenueToday))
	if !b.shop.CardConfigured(ctx) {
		text += "\n\n⚠️ <b>شماره کارت تنظیم نشده</b> - تا تنظیم نشود هیچ مشتری‌ای نمی‌تواند پرداخت کند."
	}
	b.show(ctx, adminID, msgID, text, kbAdminHome(st.PendingReview))
}

func (b *Bot) onAdminCallback(ctx context.Context, admin store.User, q *models.CallbackQuery, parts []string, msgID int) {
	answered := false
	answer := func(text string, alert bool) {
		if !answered {
			b.answer(ctx, q.ID, text, alert)
			answered = true
		}
	}
	defer answer("", false)

	switch parts[0] {
	case "ok":
		b.approve(ctx, admin, id(parts, 1), msgID, answer)
		return
	case "no":
		b.reject(ctx, admin, id(parts, 1), msgID, answer)
		return
	}

	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch action {
	case "":
		b.sessions.clear(admin.ID)
		b.adminHome(ctx, admin.ID, msgID)

	case "rev":
		b.listPending(ctx, admin, msgID)

	case "st":
		st, err := b.st.Stats(ctx, startOfToday(b.now()))
		if err != nil {
			answer(txtSomethingWent, true)
			return
		}
		b.show(ctx, admin.ID, msgID, fmt.Sprintf(
			"📊 <b>آمار</b>\n\n👥 کاربران: %d\n🤝 نماینده‌ها: %d\n📦 سرویس‌های فروخته‌شده: %d\n🧾 فروش موفق: %d\n💰 کل درآمد: %s\n📅 درآمد امروز: %s\n⏳ رسید در انتظار: %d",
			st.Users, st.Agents, st.Services, st.CompletedSales, toman(st.Revenue), toman(st.RevenueToday), st.PendingReview),
			kbAdminBack())

	case "pl":
		b.listPlans(ctx, admin, msgID)
	case "pladd":
		b.sessions.start(admin.ID, "admin_plan_name")
		b.show(ctx, admin.ID, msgID, "📦 <b>پلن جدید</b>\n\nنام پلن را بفرستید (مثلاً «۳۰ گیگ یک‌ماهه»).\nبرای انصراف /cancel", nil)
	case "pltog":
		p, err := b.st.GetPlan(ctx, id(parts, 2))
		if err == nil {
			_ = b.st.SetPlanActive(ctx, p.ID, !p.IsActive)
		}
		b.listPlans(ctx, admin, msgID)
	case "pldel":
		deleted, err := b.st.DeletePlan(ctx, id(parts, 2))
		switch {
		case err != nil:
			answer(txtSomethingWent, true)
		case !deleted:
			answer("این پلن سابقه‌ی فروش دارد، به‌جای حذف غیرفعال شد.", true)
		}
		b.listPlans(ctx, admin, msgID)

	case "dc":
		b.listDiscounts(ctx, admin, msgID)
	case "dcadd":
		b.sessions.start(admin.ID, "admin_dc_code")
		b.show(ctx, admin.ID, msgID, "🎟 <b>کد تخفیف جدید</b>\n\nخود کد را بفرستید (مثلاً <code>NOROOZ</code>).\nبرای انصراف /cancel", nil)
	case "dctog":
		code := strings.Join(parts[2:], ":")
		if d, err := b.st.GetDiscount(ctx, code); err == nil {
			_ = b.st.SetDiscountActive(ctx, d.Code, !d.IsActive)
		}
		b.listDiscounts(ctx, admin, msgID)

	case "u":
		b.sessions.start(admin.ID, "admin_user_lookup")
		b.show(ctx, admin.ID, msgID, "👤 آیدی عددی کاربر را بفرستید.\nبرای انصراف /cancel", nil)
	case "ua": // toggle agent
		if u, err := b.st.GetUser(ctx, id(parts, 2)); err == nil {
			_ = b.st.SetAgent(ctx, u.ID, !u.IsAgent)
			if !u.IsAgent {
				b.send(ctx, u.ID, "🤝 حساب شما به <b>نماینده</b> ارتقا یافت و از این پس پلن‌ها را با قیمت نمایندگی می‌بینید.", nil)
			}
		}
		b.showUserCard(ctx, admin.ID, msgID, id(parts, 2))
	case "ub": // toggle block
		if u, err := b.st.GetUser(ctx, id(parts, 2)); err == nil && !b.isAdmin(u.ID) {
			_ = b.st.SetBlocked(ctx, u.ID, !u.IsBlocked)
		}
		b.showUserCard(ctx, admin.ID, msgID, id(parts, 2))
	case "um": // adjust balance
		s := b.sessions.start(admin.ID, "admin_user_balance")
		s.target = id(parts, 2)
		b.show(ctx, admin.ID, msgID, "💰 مبلغ تغییر موجودی را بفرستید.\nبرای افزایش عدد مثبت (<code>50000</code>) و برای کاهش منفی (<code>-50000</code>).\nبرای انصراف /cancel", nil)

	case "set":
		b.listSettings(ctx, admin, msgID)
	case "se":
		key := strings.Join(parts[2:], ":")
		if _, ok := settingLabels[key]; !ok {
			answer(txtSomethingWent, true)
			return
		}
		s := b.sessions.start(admin.ID, "admin_setting")
		s.fields["key"] = key
		b.show(ctx, admin.ID, msgID, fmt.Sprintf("✏️ مقدار جدید «%s» را بفرستید.\nمقدار فعلی: <code>%s</code>\nبرای خالی‌کردن <code>-</code> بفرستید. انصراف /cancel",
			settingLabels[key], esc(b.shop.Setting(ctx, key))), nil)

	case "bc":
		b.sessions.start(admin.ID, "admin_broadcast")
		b.show(ctx, admin.ID, msgID, "📣 متن پیام همگانی را بفرستید.\nبرای انصراف /cancel", nil)
	case "bcgo":
		s, ok := b.sessions.get(admin.ID)
		if !ok || s.step != "admin_broadcast_confirm" {
			answer("پیام منقضی شده، دوباره شروع کنید.", true)
			return
		}
		text := s.fields["text"]
		b.sessions.clear(admin.ID)
		b.show(ctx, admin.ID, msgID, "📣 ارسال شروع شد...", nil)
		go b.broadcast(context.WithoutCancel(ctx), admin.ID, text)
	}
}

// ── receipts ────────────────────────────────────────────────────────────────

func (b *Bot) listPending(ctx context.Context, admin store.User, msgID int) {
	orders, err := b.st.ListOrdersByStatus(ctx, store.StatusAwaitingReview, 20)
	if err != nil || len(orders) == 0 {
		b.show(ctx, admin.ID, msgID, "✅ رسیدی در انتظار نیست.", kbAdminBack())
		return
	}
	b.show(ctx, admin.ID, msgID, fmt.Sprintf("🧾 %d رسید در انتظار - در ادامه ارسال می‌شوند:", len(orders)), kbAdminBack())
	for _, o := range orders {
		u, _ := b.st.GetUser(ctx, o.UserID)
		if _, err := b.tg.SendPhoto(ctx, &tgbot.SendPhotoParams{
			ChatID: admin.ID, Photo: &models.InputFileString{Data: o.ReceiptFileID},
			Caption: b.reviewCaption(ctx, u, o), ParseMode: models.ParseModeHTML, ReplyMarkup: kbReview(o.ID),
		}); err != nil {
			b.log.Warn("could not resend receipt", "order", o.ID, "err", err)
		}
	}
}

// closeReview strips the approve/reject buttons from a receipt once it has
// been decided, so nobody taps a stale one later.
func (b *Bot) closeReview(ctx context.Context, adminID int64, msgID int) {
	if msgID == 0 {
		return
	}
	_, _ = b.tg.EditMessageReplyMarkup(ctx, &tgbot.EditMessageReplyMarkupParams{
		ChatID: adminID, MessageID: msgID, ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}},
	})
}

func (b *Bot) approve(ctx context.Context, admin store.User, orderID int64, msgID int, answer func(string, bool)) {
	res, err := b.shop.Approve(ctx, orderID, admin.ID)
	if errors.Is(err, store.ErrConflict) {
		answer("این رسید قبلاً بررسی شده.", true)
		b.closeReview(ctx, admin.ID, msgID)
		return
	}
	if err != nil {
		b.log.Error("approve failed", "order", orderID, "err", err)
		answer(txtSomethingWent, true)
		return
	}
	b.closeReview(ctx, admin.ID, msgID)
	if res.Failed {
		answer("⚠️ پرداخت تأیید شد ولی ساخت سرویس ناموفق بود.", true)
	} else {
		answer("✅ تأیید و ارسال شد.", false)
		b.send(ctx, admin.ID, fmt.Sprintf("✅ سفارش <code>%d</code> تأیید و تحویل شد.", orderID), nil)
	}
	b.deliver(ctx, res)
}

func (b *Bot) reject(ctx context.Context, admin store.User, orderID int64, msgID int, answer func(string, bool)) {
	o, err := b.shop.Reject(ctx, orderID, admin.ID)
	if errors.Is(err, store.ErrConflict) {
		answer("این رسید قبلاً بررسی شده.", true)
		b.closeReview(ctx, admin.ID, msgID)
		return
	}
	if err != nil {
		answer(txtSomethingWent, true)
		return
	}
	b.closeReview(ctx, admin.ID, msgID)
	answer("❌ رد شد.", false)
	b.send(ctx, admin.ID, fmt.Sprintf("❌ سفارش <code>%d</code> رد شد.", orderID), nil)
	b.send(ctx, o.UserID, txtRejected(o.ID), kbBackToMenu())
}

// ── plans ───────────────────────────────────────────────────────────────────

func (b *Bot) listPlans(ctx context.Context, admin store.User, msgID int) {
	plans, _ := b.st.ListPlans(ctx, false)
	text := "📦 <b>پلن‌ها</b>\n"
	var rows [][]models.InlineKeyboardButton
	if len(plans) == 0 {
		text += "\nهنوز پلنی ساخته نشده."
	}
	for _, p := range plans {
		state := "🟢"
		if !p.IsActive {
			state = "⚫"
		}
		agent := ""
		if p.AgentPrice > 0 {
			agent = " | نماینده " + toman(p.AgentPrice)
		}
		text += fmt.Sprintf("\n%s <b>%s</b>\n   %s | %s | %s%s", state, esc(p.Name), gbLabel(p.DataGB), daysLabel(p.Days), toman(p.Price), agent)
		toggle := "غیرفعال کن"
		if !p.IsActive {
			toggle = "فعال کن"
		}
		rows = append(rows, row(
			btn(fmt.Sprintf("%s %s", state, p.Name), "a:pl"),
			btn(toggle, "a:pltog:"+fmt.Sprint(p.ID)),
			btn("🗑", "a:pldel:"+fmt.Sprint(p.ID)),
		))
	}
	rows = append(rows, row(btn("➕ افزودن پلن", "a:pladd")), row(btn("🔙 پنل مدیریت", "a")))
	b.show(ctx, admin.ID, msgID, text, kb(rows...))
}

// ── discount codes ──────────────────────────────────────────────────────────

func (b *Bot) listDiscounts(ctx context.Context, admin store.User, msgID int) {
	codes, _ := b.st.ListDiscounts(ctx)
	text := "🎟 <b>کدهای تخفیف</b>\n"
	var rows [][]models.InlineKeyboardButton
	if len(codes) == 0 {
		text += "\nهنوز کدی ساخته نشده."
	}
	for _, d := range codes {
		state := "🟢"
		if !d.IsActive {
			state = "⚫"
		}
		value := toman(d.Amount)
		if d.Percent > 0 {
			value = fmt.Sprintf("%d%%", d.Percent)
		}
		uses := fmt.Sprintf("%d بار", d.Used)
		if d.MaxUses > 0 {
			uses = fmt.Sprintf("%d از %d", d.Used, d.MaxUses)
		}
		text += fmt.Sprintf("\n%s <code>%s</code> | %s | استفاده: %s", state, esc(d.Code), value, uses)
		if d.ExpiresAt > 0 {
			text += " | تا " + time.Unix(d.ExpiresAt, 0).Format("2006-01-02")
		}
		rows = append(rows, row(btn(state+" "+d.Code, "a:dctog:"+d.Code)))
	}
	rows = append(rows, row(btn("➕ افزودن کد", "a:dcadd")), row(btn("🔙 پنل مدیریت", "a")))
	b.show(ctx, admin.ID, msgID, text, kb(rows...))
}

// ── users ───────────────────────────────────────────────────────────────────

func (b *Bot) showUserCard(ctx context.Context, adminID int64, msgID int, userID int64) {
	u, err := b.st.GetUser(ctx, userID)
	if err != nil {
		b.show(ctx, adminID, msgID, "❌ کاربری با این آیدی پیدا نشد.", kbAdminBack())
		return
	}
	svcs, _ := b.st.ListServices(ctx, u.ID)
	refs, _ := b.st.CountReferrals(ctx, u.ID)
	name := esc(u.FirstName)
	if u.Username != "" {
		name += " (@" + esc(u.Username) + ")"
	}
	role := "👤 مشتری"
	if u.IsAgent {
		role = "🤝 نماینده"
	}
	blocked := ""
	if u.IsBlocked {
		blocked = "\n⛔️ <b>مسدود</b>"
	}
	text := fmt.Sprintf("👤 <b>%s</b>\n🆔 <code>%d</code>\n%s%s\n💰 موجودی: %s\n📦 سرویس‌ها: %d\n👥 دعوت‌ها: %d",
		name, u.ID, role, blocked, toman(u.Balance), len(svcs), refs)

	agentLabel := "🤝 نماینده کن"
	if u.IsAgent {
		agentLabel = "👤 مشتری عادی کن"
	}
	blockLabel := "⛔️ مسدود کن"
	if u.IsBlocked {
		blockLabel = "✅ رفع مسدودی"
	}
	b.show(ctx, adminID, msgID, text, kb(
		row(btn(agentLabel, fmt.Sprintf("a:ua:%d", u.ID)), btn(blockLabel, fmt.Sprintf("a:ub:%d", u.ID))),
		row(btn("💰 تغییر موجودی", fmt.Sprintf("a:um:%d", u.ID))),
		row(btn("🔙 پنل مدیریت", "a")),
	))
}

// ── settings ────────────────────────────────────────────────────────────────

var settingOrder = []string{
	shop.SettingCardNumber, shop.SettingCardHolder, shop.SettingBankName, shop.SettingSupport,
	shop.SettingTrialEnabled, shop.SettingTrialGB, shop.SettingTrialHours,
	shop.SettingReferralReward, shop.SettingTopupMin, shop.SettingTopupMax,
}

var settingLabels = map[string]string{
	shop.SettingCardNumber:     "شماره کارت",
	shop.SettingCardHolder:     "نام صاحب کارت",
	shop.SettingBankName:       "نام بانک",
	shop.SettingSupport:        "یوزرنیم پشتیبانی (بدون @)",
	shop.SettingTrialEnabled:   "اکانت تست (1 روشن / 0 خاموش)",
	shop.SettingTrialGB:        "حجم تست (گیگ)",
	shop.SettingTrialHours:     "مدت تست (ساعت)",
	shop.SettingReferralReward: "پاداش دعوت (تومان)",
	shop.SettingTopupMin:       "حداقل شارژ (تومان)",
	shop.SettingTopupMax:       "حداکثر شارژ (تومان)",
}

// numericSettings must be whole numbers; anything else is refused on input
// rather than silently read as 0 later.
var numericSettings = map[string]bool{
	shop.SettingTrialEnabled: true, shop.SettingTrialGB: true, shop.SettingTrialHours: true,
	shop.SettingReferralReward: true, shop.SettingTopupMin: true, shop.SettingTopupMax: true,
}

func (b *Bot) listSettings(ctx context.Context, admin store.User, msgID int) {
	text := "⚙️ <b>تنظیمات</b>\n"
	var rows [][]models.InlineKeyboardButton
	for _, key := range settingOrder {
		v := b.shop.Setting(ctx, key)
		if v == "" {
			v = "—"
		}
		text += fmt.Sprintf("\n• %s: <code>%s</code>", settingLabels[key], esc(v))
		rows = append(rows, row(btn("✏️ "+settingLabels[key], "a:se:"+key)))
	}
	rows = append(rows, row(btn("🔙 پنل مدیریت", "a")))
	b.show(ctx, admin.ID, msgID, text, kb(rows...))
}

// ── admin input ─────────────────────────────────────────────────────────────

func (b *Bot) onAdminInput(ctx context.Context, admin store.User, s *session, text string) {
	num := func() (int64, bool) {
		n, ok := parseAmount(text)
		if !ok {
			b.send(ctx, admin.ID, txtNotANum, nil)
		}
		return n, ok
	}

	switch s.step {
	// new plan: name -> GB -> days -> price -> agent price
	case "admin_plan_name":
		s.fields["name"] = text
		s.step = "admin_plan_gb"
		b.send(ctx, admin.ID, "📊 حجم به <b>گیگ</b> (برای نامحدود <code>0</code>):", nil)
	case "admin_plan_gb":
		if n, ok := num(); ok {
			s.fields["gb"] = fmt.Sprint(n)
			s.step = "admin_plan_days"
			b.send(ctx, admin.ID, "⏳ مدت به <b>روز</b> (برای بدون محدودیت <code>0</code>):", nil)
		}
	case "admin_plan_days":
		if n, ok := num(); ok {
			s.fields["days"] = fmt.Sprint(n)
			s.step = "admin_plan_price"
			b.send(ctx, admin.ID, "💰 قیمت به <b>تومان</b>:", nil)
		}
	case "admin_plan_price":
		if n, ok := num(); ok {
			s.fields["price"] = fmt.Sprint(n)
			s.step = "admin_plan_agent"
			b.send(ctx, admin.ID, "🤝 قیمت نماینده به <b>تومان</b> (برای همان قیمت عادی <code>0</code>):", nil)
		}
	case "admin_plan_agent":
		agent, ok := num()
		if !ok {
			return
		}
		gb, _ := parseAmount(s.fields["gb"])
		days, _ := parseAmount(s.fields["days"])
		price, _ := parseAmount(s.fields["price"])
		b.sessions.clear(admin.ID)
		p, err := b.st.CreatePlan(ctx, store.Plan{Name: s.fields["name"], DataGB: gb, Days: days, Price: price, AgentPrice: agent})
		if err != nil {
			b.send(ctx, admin.ID, txtSomethingWent, kbAdminBack())
			return
		}
		b.send(ctx, admin.ID, fmt.Sprintf("✅ پلن «%s» ساخته شد.", esc(p.Name)), nil)
		b.listPlans(ctx, admin, 0)

	// new code: code -> percent or amount -> max uses -> valid days
	case "admin_dc_code":
		code := strings.ToUpper(strings.TrimSpace(text))
		if code == "" || strings.ContainsAny(code, " :") || len(code) > 32 {
			b.send(ctx, admin.ID, "⚠️ کد باید یک کلمه‌ی بدون فاصله و حداکثر ۳۲ کاراکتر باشد.", nil)
			return
		}
		if _, err := b.st.GetDiscount(ctx, code); err == nil {
			b.send(ctx, admin.ID, "⚠️ این کد قبلاً ساخته شده. کد دیگری بفرستید.", nil)
			return
		}
		s.fields["code"] = code
		s.step = "admin_dc_value"
		b.send(ctx, admin.ID, "💯 مقدار تخفیف:\n• درصدی: عدد با % مثل <code>20%</code>\n• مبلغی: فقط عدد به تومان مثل <code>30000</code>", nil)
	case "admin_dc_value":
		v := normalizeDigits(text)
		if strings.HasSuffix(v, "%") || strings.HasSuffix(v, "٪") {
			pct, ok := parseAmount(strings.TrimRight(v, "%٪"))
			if !ok || pct < 1 || pct > 100 {
				b.send(ctx, admin.ID, "⚠️ درصد باید بین ۱ و ۱۰۰ باشد.", nil)
				return
			}
			s.fields["percent"] = fmt.Sprint(pct)
		} else {
			amt, ok := parseAmount(v)
			if !ok || amt < 1 {
				b.send(ctx, admin.ID, txtNotANum, nil)
				return
			}
			s.fields["amount"] = fmt.Sprint(amt)
		}
		s.step = "admin_dc_max"
		b.send(ctx, admin.ID, "🔢 حداکثر تعداد استفاده (برای نامحدود <code>0</code>):", nil)
	case "admin_dc_max":
		if n, ok := num(); ok {
			s.fields["max"] = fmt.Sprint(n)
			s.step = "admin_dc_days"
			b.send(ctx, admin.ID, "📅 اعتبار به روز (برای همیشگی <code>0</code>):", nil)
		}
	case "admin_dc_days":
		days, ok := num()
		if !ok {
			return
		}
		pct, _ := parseAmount(s.fields["percent"])
		amt, _ := parseAmount(s.fields["amount"])
		max, _ := parseAmount(s.fields["max"])
		var expires int64
		if days > 0 {
			expires = b.now().Add(time.Duration(days) * 24 * time.Hour).Unix()
		}
		code := s.fields["code"]
		b.sessions.clear(admin.ID)
		if err := b.st.CreateDiscount(ctx, store.DiscountCode{Code: code, Percent: pct, Amount: amt, MaxUses: max, ExpiresAt: expires}); err != nil {
			b.send(ctx, admin.ID, txtSomethingWent, kbAdminBack())
			return
		}
		b.send(ctx, admin.ID, fmt.Sprintf("✅ کد <code>%s</code> ساخته شد.", esc(code)), nil)
		b.listDiscounts(ctx, admin, 0)

	case "admin_user_lookup":
		uid, ok := parseAmount(text)
		if !ok {
			b.send(ctx, admin.ID, txtNotANum, nil)
			return
		}
		b.sessions.clear(admin.ID)
		b.showUserCard(ctx, admin.ID, 0, uid)

	case "admin_user_balance":
		raw := normalizeDigits(text)
		neg := strings.HasPrefix(raw, "-")
		n, ok := parseAmount(strings.TrimPrefix(strings.TrimPrefix(raw, "-"), "+"))
		if !ok || n == 0 {
			b.send(ctx, admin.ID, txtNotANum, nil)
			return
		}
		if neg {
			n = -n
		}
		target := s.target
		b.sessions.clear(admin.ID)
		err := b.st.AdjustBalance(ctx, target, n, "admin", 0)
		switch {
		case errors.Is(err, store.ErrInsufficientBalance):
			b.send(ctx, admin.ID, "⚠️ موجودی کاربر برای این کاهش کافی نیست.", nil)
		case err != nil:
			b.send(ctx, admin.ID, txtSomethingWent, nil)
		default:
			fresh, _ := b.st.GetUser(ctx, target)
			b.send(ctx, target, fmt.Sprintf("💰 موجودی کیف پول شما تغییر کرد.\nموجودی فعلی: <b>%s</b>", toman(fresh.Balance)), nil)
		}
		b.showUserCard(ctx, admin.ID, 0, target)

	case "admin_setting":
		key := s.fields["key"]
		value := strings.TrimSpace(text)
		if value == "-" {
			value = ""
		}
		if numericSettings[key] && value != "" {
			n, ok := parseAmount(value)
			if !ok {
				b.send(ctx, admin.ID, txtNotANum, nil)
				return
			}
			value = fmt.Sprint(n)
		}
		if key == shop.SettingSupport {
			value = strings.TrimPrefix(value, "@")
		}
		b.sessions.clear(admin.ID)
		if err := b.st.SetSetting(ctx, key, value); err != nil {
			b.send(ctx, admin.ID, txtSomethingWent, nil)
			return
		}
		b.send(ctx, admin.ID, "✅ ذخیره شد.", nil)
		b.listSettings(ctx, admin, 0)

	case "admin_broadcast":
		s.fields["text"] = text
		s.step = "admin_broadcast_confirm"
		ids, _ := b.st.ListUserIDs(ctx)
		b.send(ctx, admin.ID, fmt.Sprintf("📣 <b>پیش‌نمایش</b> - برای %d کاربر ارسال می‌شود:\n\n%s", len(ids), esc(text)),
			kb(row(btn("✅ ارسال", "a:bcgo"), btn("❌ انصراف", "a"))))

	default:
		b.sessions.clear(admin.ID)
		b.adminHome(ctx, admin.ID, 0)
	}
}

// broadcast sends a message to every customer, paced under Telegram's
// ~30-messages-a-second limit so the bot is not rate-limited mid-send.
func (b *Bot) broadcast(ctx context.Context, adminID int64, text string) {
	ids, err := b.st.ListUserIDs(ctx)
	if err != nil {
		b.send(ctx, adminID, txtSomethingWent, nil)
		return
	}
	sent, failed := 0, 0
	for _, uid := range ids {
		if _, err := b.tg.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: uid, Text: esc(text), ParseMode: models.ParseModeHTML}); err != nil {
			failed++
		} else {
			sent++
		}
		time.Sleep(40 * time.Millisecond)
	}
	b.send(ctx, adminID, fmt.Sprintf("📣 ارسال تمام شد.\n✅ موفق: %d\n❌ ناموفق (ربات را بلاک کرده‌اند): %d", sent, failed), kbAdminBack())
}
