package bot

import (
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
)

// toman formats a whole-toman amount with thousands separators: 1,250,000.
func toman(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String() + " تومان"
	}
	return b.String() + " تومان"
}

// bytesHuman renders a byte count in GB or MB, whichever reads better.
func bytesHuman(n int64) string {
	const gb = 1024 * 1024 * 1024
	const mb = 1024 * 1024
	switch {
	case n >= gb:
		return trimZero(fmt.Sprintf("%.2f", float64(n)/gb)) + " GB"
	case n >= mb:
		return trimZero(fmt.Sprintf("%.1f", float64(n)/mb)) + " MB"
	default:
		return "0 MB"
	}
}

func trimZero(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

func gbLabel(gb int64) string {
	if gb == 0 {
		return "نامحدود"
	}
	return fmt.Sprintf("%d گیگ", gb)
}

func daysLabel(days int64) string {
	if days == 0 {
		return "بدون محدودیت زمانی"
	}
	return fmt.Sprintf("%d روزه", days)
}

// remaining describes the time left until expire, or that it has passed.
func remaining(expire int64, now time.Time) string {
	if expire == 0 {
		return "نامحدود"
	}
	left := time.Unix(expire, 0).Sub(now)
	if left <= 0 {
		return "منقضی شده"
	}
	days := int(left.Hours()) / 24
	hours := int(left.Hours()) % 24
	switch {
	case days > 0:
		return fmt.Sprintf("%d روز و %d ساعت", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d ساعت", hours)
	default:
		return fmt.Sprintf("%d دقیقه", int(left.Minutes()))
	}
}

// esc makes user-supplied text safe inside an HTML-mode message. Telegram
// rejects the WHOLE message on a stray "<", so a name like "<3" would
// otherwise silently stop that customer from ever seeing a menu.
func esc(s string) string { return html.EscapeString(s) }

// normalizeDigits turns Persian and Arabic-Indic digits into ASCII and drops
// the separators people type, so "۱۵۰,۰۰۰" and "150000" parse the same. A
// customer on a Persian keyboard would otherwise be told their amount is not
// a number.
func normalizeDigits(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		case r == ',' || r == '،' || r == '٬' || r == ' ' || r == '_':
			// thousands separators - ignore
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseAmount reads a non-negative whole number typed by a person.
func parseAmount(s string) (int64, bool) {
	n, err := strconv.ParseInt(normalizeDigits(s), 10, 64)
	return n, err == nil && n >= 0
}

func statusLabel(status string) string {
	switch status {
	case "active":
		return "🟢 فعال"
	case "limited":
		return "🟠 حجم تمام شده"
	case "expired":
		return "🔴 منقضی شده"
	case "disabled":
		return "⚫ غیرفعال"
	case "on_hold":
		return "🟡 در انتظار اولین اتصال"
	default:
		return status
	}
}

// usageBar draws a 10-cell bar of how much of the allowance is used.
func usageBar(used, limit int64) string {
	if limit <= 0 {
		return ""
	}
	filled := int(used * 10 / limit)
	if filled > 10 {
		filled = 10
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", 10-filled)
}
