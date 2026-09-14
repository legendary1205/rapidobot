package bot

import (
	"errors"
	"fmt"

	"github.com/legendary1205/rapidobot/internal/shop"
)

// Every customer-facing string lives here, so wording (or a translation) is
// changed in one file without touching any logic.

const (
	txtNotAllowed    = "⛔️ دسترسی ندارید."
	txtOwnersOnly    = "⛔️ فقط مالک ربات می‌تواند ادمین‌ها را مدیریت کند."
	txtCancelled     = "لغو شد."
	txtSomethingWent = "⚠️ مشکلی پیش آمد. لطفاً دوباره تلاش کنید."
	txtPanelDown     = "⚠️ ارتباط با سرور برقرار نشد. چند دقیقه بعد دوباره امتحان کنید."

	txtNoPlans       = "در حال حاضر پلنی برای فروش موجود نیست."
	txtChoosePlan    = "🛒 <b>یکی از پلن‌ها را انتخاب کنید:</b>"
	txtChooseRenew   = "🔄 <b>پلن تمدید را انتخاب کنید:</b>"
	txtPlanGone      = "این پلن دیگر موجود نیست."
	txtNoServices    = "📦 هنوز سرویسی ندارید.\nاز «خرید سرویس» یکی بگیرید."
	txtServicesTitle = "📦 <b>سرویس‌های شما:</b>"

	txtAskCode       = "🎟 کد تخفیف را بفرستید.\nبرای انصراف /cancel"
	txtCardMissing   = "⚠️ پرداخت کارت‌به‌کارت هنوز تنظیم نشده. لطفاً با پشتیبانی تماس بگیرید."
	txtNoOpenReceipt = "📸 سفارشی در انتظار رسید ندارید.\nاول از منو خرید یا شارژ را شروع کنید."
	txtReceiptThanks = "✅ رسید دریافت شد.\nبعد از تأیید مدیر، سرویس خودکار برایتان ارسال می‌شود."
	txtSendAsPhoto   = "📸 لطفاً رسید را به‌صورت <b>عکس</b> بفرستید، نه فایل.\n(در ارسال، گزینه‌ی «فشرده‌سازی تصویر» را روشن بگذارید.)"

	txtInsufficient = "💸 موجودی کیف پول کافی نیست."
	txtOrderMoved   = "این سفارش دیگر قابل پرداخت نیست."

	txtAskTopup = "💳 مبلغ شارژ را به <b>تومان</b> بفرستید (فقط عدد).\nبرای انصراف /cancel"
	txtNotANum  = "⚠️ فقط عدد بفرستید. مثلاً <code>200000</code>"

	txtTrialDisabled = "🎁 اکانت تست در حال حاضر فعال نیست."
	txtTrialUsed     = "🎁 شما قبلاً از اکانت تست استفاده کرده‌اید."

	txtNoSupport = "📞 پشتیبانی هنوز تنظیم نشده."
)

func txtWelcome(name string) string {
	return fmt.Sprintf("سلام <b>%s</b> 👋\nبه فروشگاه خوش آمدید. از منوی زیر انتخاب کنید:", esc(name))
}

func txtReferralJoined(name string) string {
	return fmt.Sprintf("👥 <b>%s</b> با لینک دعوت شما عضو شد.\nبا اولین خریدش پاداش به کیف پولتان اضافه می‌شود.", esc(name))
}

func txtPlanCheckout(planName string, dataGB, days, base, discount, amount int64, code string, balance int64) string {
	s := fmt.Sprintf("🧾 <b>پیش‌فاکتور</b>\n\n📦 پلن: <b>%s</b>\n📊 حجم: %s\n⏳ مدت: %s\n\n💰 قیمت: %s",
		esc(planName), gbLabel(dataGB), daysLabel(days), toman(base))
	if discount > 0 {
		s += fmt.Sprintf("\n🎟 تخفیف (%s): -%s\n✅ <b>مبلغ نهایی: %s</b>", esc(code), toman(discount), toman(amount))
	} else {
		s += fmt.Sprintf("\n✅ <b>مبلغ نهایی: %s</b>", toman(amount))
	}
	return s + fmt.Sprintf("\n\n👛 موجودی کیف پول: %s\n\nروش پرداخت را انتخاب کنید:", toman(balance))
}

func txtCardInstructions(amount int64, card, holder, bank string, orderID int64) string {
	s := fmt.Sprintf("💳 <b>پرداخت کارت‌به‌کارت</b>\n\nلطفاً مبلغ <b>%s</b> را به کارت زیر واریز کنید:\n\n<code>%s</code>",
		toman(amount), esc(card))
	if holder != "" {
		s += "\n👤 به نام: " + esc(holder)
	}
	if bank != "" {
		s += "\n🏦 بانک: " + esc(bank)
	}
	return s + fmt.Sprintf("\n\n📸 سپس <b>عکس رسید</b> را همین‌جا بفرستید.\n🔖 شماره سفارش: <code>%d</code>", orderID)
}

func txtDelivered(title, url string, dataGB int64, expire string) string {
	return fmt.Sprintf("🎉 <b>%s</b>\n\n📊 حجم: %s\n⏳ اعتبار: %s\n\n🔗 لینک اشتراک:\n<code>%s</code>\n\nلینک را در برنامه‌ی V2rayNG، Hiddify، Streisand یا v2box وارد کنید.",
		esc(title), gbLabel(dataGB), expire, esc(url))
}

func txtDeliveryFailed(orderID int64, refunded bool) string {
	s := fmt.Sprintf("⚠️ پرداخت شما ثبت شد اما ساخت سرویس با خطا مواجه شد (سفارش <code>%d</code>).", orderID)
	if refunded {
		s += "\n💰 مبلغ به کیف پولتان برگشت داده شد."
	} else {
		s += "\nپشتیبانی در اسرع وقت پیگیری می‌کند."
	}
	return s
}

func txtRejected(orderID int64) string {
	return fmt.Sprintf("❌ رسید سفارش <code>%d</code> تأیید نشد.\nدر صورت نیاز با پشتیبانی تماس بگیرید.", orderID)
}

func txtWallet(balance int64) string {
	return fmt.Sprintf("👛 <b>کیف پول</b>\n\n💰 موجودی: <b>%s</b>\n\nبا شارژ کیف پول، خرید و تمدید بدون انتظار برای تأیید انجام می‌شود.", toman(balance))
}

func txtTopupRange(min, max int64) string {
	return fmt.Sprintf("⚠️ مبلغ باید بین %s و %s باشد.", toman(min), toman(max))
}

func txtTopupDone(amount, balance int64) string {
	return fmt.Sprintf("✅ کیف پول شما <b>%s</b> شارژ شد.\n💰 موجودی فعلی: %s", toman(amount), toman(balance))
}

func txtReferral(link string, count, reward int64) string {
	return fmt.Sprintf("👥 <b>دعوت از دوستان</b>\n\nبا هر دوستی که از لینک شما عضو شود و اولین خریدش را انجام دهد، <b>%s</b> به کیف پولتان اضافه می‌شود.\n\n🔗 لینک دعوت شما:\n<code>%s</code>\n\n👤 تعداد دعوت‌شده‌ها: %d",
		toman(reward), esc(link), count)
}

func txtReferralReward(amount int64) string {
	return fmt.Sprintf("🎁 یکی از دعوت‌شده‌های شما خرید کرد!\n<b>%s</b> به کیف پولتان اضافه شد.", toman(amount))
}

func txtSupport(username string) string {
	return "📞 <b>پشتیبانی</b>\n\nبرای ارتباط پیام بدهید به: @" + esc(username)
}

func txtCodeError(err error) string {
	switch {
	case errors.Is(err, shop.ErrCodeInvalid):
		return "❌ کد تخفیف معتبر نیست."
	case errors.Is(err, shop.ErrCodeExpired):
		return "⌛️ این کد تخفیف منقضی شده."
	case errors.Is(err, shop.ErrCodeUsedUp):
		return "🚫 ظرفیت این کد تخفیف تمام شده."
	case errors.Is(err, shop.ErrCodeAlreadyUsed):
		return "🚫 شما قبلاً از این کد استفاده کرده‌اید."
	case errors.Is(err, shop.ErrCodeNotApplicable):
		return "🚫 این کد روی این سفارش قابل استفاده نیست."
	}
	return txtSomethingWent
}
