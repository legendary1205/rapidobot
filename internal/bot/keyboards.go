package bot

import (
	"fmt"

	"github.com/go-telegram/bot/models"

	"github.com/legendary1205/rapidobot/internal/store"
)

// Callback data is capped at 64 bytes by Telegram, so every action is a
// short code plus ids, split on ":" by the router.

func btn(text, data string) models.InlineKeyboardButton {
	return models.InlineKeyboardButton{Text: text, CallbackData: data}
}

func kb(rows ...[]models.InlineKeyboardButton) *models.InlineKeyboardMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func row(buttons ...models.InlineKeyboardButton) []models.InlineKeyboardButton { return buttons }

func kbMainMenu(isAdmin, trialOn bool) *models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{
		row(btn("🛒 خرید سرویس", "buy"), btn("📦 سرویس‌های من", "svc")),
		row(btn("👛 کیف پول", "wal"), btn("👥 دعوت دوستان", "ref")),
	}
	last := row(btn("📞 پشتیبانی", "sup"))
	if trialOn {
		last = row(btn("🎁 اکانت تست", "trial"), btn("📞 پشتیبانی", "sup"))
	}
	rows = append(rows, last)
	if isAdmin {
		rows = append(rows, row(btn("⚙️ پنل مدیریت", "a")))
	}
	return kb(rows...)
}

func kbBackToMenu() *models.InlineKeyboardMarkup {
	return kb(row(btn("🏠 منوی اصلی", "m")))
}

// kbPlans lists plans; action is "p" for a new purchase or "rp:<serviceID>"
// for a renewal of that service.
func kbPlans(plans []store.Plan, u store.User, action string) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	for _, p := range plans {
		label := fmt.Sprintf("%s | %s | %s", p.Name, gbLabel(p.DataGB), toman(p.PriceFor(u)))
		rows = append(rows, row(btn(label, fmt.Sprintf("%s:%d", action, p.ID))))
	}
	back := "m"
	if action != "p" {
		back = "svc"
	}
	rows = append(rows, row(btn("🔙 بازگشت", back)))
	return kb(rows...)
}

func kbCheckout(orderID int64, canUseWallet bool) *models.InlineKeyboardMarkup {
	pay := row(btn("💳 کارت‌به‌کارت", fmt.Sprintf("pc:%d", orderID)))
	if canUseWallet {
		pay = row(btn("👛 پرداخت از کیف پول", fmt.Sprintf("pw:%d", orderID)), btn("💳 کارت‌به‌کارت", fmt.Sprintf("pc:%d", orderID)))
	}
	return kb(
		pay,
		row(btn("🎟 کد تخفیف دارم", fmt.Sprintf("dc:%d", orderID))),
		row(btn("❌ انصراف", "m")),
	)
}

func kbServices(svcs []store.Service) *models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	for _, sv := range svcs {
		label := "📦 " + sv.PanelUsername
		if sv.IsTrial {
			label = "🎁 " + sv.PanelUsername + " (تست)"
		}
		rows = append(rows, row(btn(label, fmt.Sprintf("s:%d", sv.ID))))
	}
	rows = append(rows, row(btn("🔙 بازگشت", "m")))
	return kb(rows...)
}

func kbService(serviceID int64, canRenew bool) *models.InlineKeyboardMarkup {
	rows := [][]models.InlineKeyboardButton{
		row(btn("🔗 لینک و QR", fmt.Sprintf("sl:%d", serviceID)), btn("🔄 به‌روزرسانی", fmt.Sprintf("s:%d", serviceID))),
	}
	if canRenew {
		rows = append(rows, row(btn("♻️ تمدید سرویس", fmt.Sprintf("rn:%d", serviceID))))
	}
	rows = append(rows, row(btn("🔙 سرویس‌ها", "svc")))
	return kb(rows...)
}

func kbWallet() *models.InlineKeyboardMarkup {
	return kb(row(btn("➕ شارژ کیف پول", "top")), row(btn("🔙 بازگشت", "m")))
}

// ── admin ───────────────────────────────────────────────────────────────────

func kbAdminHome(pending int64) *models.InlineKeyboardMarkup {
	reviewLabel := "🧾 رسیدهای در انتظار"
	if pending > 0 {
		reviewLabel = fmt.Sprintf("🧾 رسیدهای در انتظار (%d)", pending)
	}
	return kb(
		row(btn(reviewLabel, "a:rev")),
		row(btn("📦 پلن‌ها", "a:pl"), btn("🎟 کدهای تخفیف", "a:dc")),
		row(btn("👤 مدیریت کاربر", "a:u"), btn("📊 آمار", "a:st")),
		row(btn("⚙️ تنظیمات", "a:set"), btn("📣 پیام همگانی", "a:bc")),
		row(btn("🏠 منوی اصلی", "m")),
	)
}

func kbReview(orderID int64) *models.InlineKeyboardMarkup {
	return kb(row(
		btn("✅ تأیید", fmt.Sprintf("ok:%d", orderID)),
		btn("❌ رد", fmt.Sprintf("no:%d", orderID)),
	))
}

func kbAdminBack() *models.InlineKeyboardMarkup {
	return kb(row(btn("🔙 پنل مدیریت", "a")))
}
