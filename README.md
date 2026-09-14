# RapidoBot

A Telegram shop that sells [Rapido-Go](https://github.com/legendary1205/rapido-go) VPN accounts. Customers buy, pay and get their subscription link without an admin typing anything; admins run the whole shop from inside Telegram.

A single Go binary with an embedded SQLite database - no separate database server, nothing else to run.

**[English](#english)** | **[فارسی](#فارسی)**

---

## English

### What it does

- **Sell plans** - customers pick a plan, pay, and receive their subscription link with a QR code the moment an admin approves.
- **Card-to-card payments** - the customer sends a photo of the receipt; every admin gets it with approve / reject buttons. Two admins tapping at once can never deliver twice.
- **Wallet** - top up once, then buy and renew instantly with no approval step.
- **Renewals** - stack on top of the time left, so renewing early never wastes days.
- **Free trial** - one small account per customer, size and duration set by the admin.
- **Agents** - mark a customer as an agent and they see a separate wholesale price.
- **Discount codes** - percent or fixed amount, with a use limit and an expiry; spent only when an order actually completes.
- **Referrals** - a personal invite link; the inviter's wallet is credited once, on their friend's first purchase.
- **Admin panel in the bot** - review receipts, manage plans, codes and customers, change settings, see sales, and broadcast.

### Install

On any server that can reach your Rapido-Go panel (the panel's own server is fine - the bot opens no ports):

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapidobot/master/rapidobot.sh) install
```

It asks for the bot token from [@BotFather](https://t.me/BotFather), your numeric Telegram id, and your panel's URL and sudo admin login - then checks all of them before starting, so a typo is caught right away instead of at the first sale.

Then open your bot, send `/start`, and go to **⚙️ پنل مدیریت → ⚙️ تنظیمات** to set the card number. `rapidobot` on its own lists the management commands (`update`, `logs`, `backup`, `restore`, ...).

### Running it by hand

```bash
cp .env.example .env    # fill it in
docker compose up -d
```

Or without Docker: `go build ./cmd/rapidobot`, set the same variables, and run it. `rapidobot check` validates the settings and exits.

### Development

```bash
go test ./...
bash scripts/installer_test.sh
```

```
cmd/rapidobot/     entrypoint
internal/store/    SQLite: customers, plans, orders, wallet, codes, settings
internal/panel/    the Rapido-Go API client
internal/shop/     selling rules - pricing, payment, delivery, trials, referrals
internal/bot/      Telegram menus and conversations
```

---

## فارسی

### این پروژه چیست

یک ربات فروش تلگرامی برای اکانت‌های [Rapido-Go](https://github.com/legendary1205/rapido-go). مشتری خودش پلن را انتخاب می‌کند، پرداخت می‌کند و لینک اشتراکش را می‌گیرد؛ مدیر هم کل فروشگاه را از داخل خود تلگرام اداره می‌کند.

یک باینری Go با دیتابیس SQLite داخلی — بدون نیاز به سرور دیتابیس جدا.

### امکانات

- **فروش پلن** — مشتری پلن را انتخاب و پرداخت می‌کند و بلافاصله بعد از تأیید، لینک اشتراک را همراه QR می‌گیرد.
- **کارت‌به‌کارت** — مشتری عکس رسید را می‌فرستد و همه‌ی ادمین‌ها آن را با دکمه‌ی تأیید/رد می‌بینند. اگر دو ادمین هم‌زمان تأیید بزنند، سرویس فقط یک بار ساخته می‌شود.
- **کیف پول** — یک بار شارژ، بعد خرید و تمدید فوری بدون انتظار برای تأیید.
- **تمدید** — روی زمان باقی‌مانده اضافه می‌شود، پس تمدید زودهنگام روزی را هدر نمی‌دهد.
- **اکانت تست** — برای هر مشتری یک بار، با حجم و مدتی که مدیر تعیین می‌کند.
- **نمایندگی** — مشتری نماینده، قیمت عمده‌ی جدا می‌بیند.
- **کد تخفیف** — درصدی یا مبلغی، با سقف استفاده و تاریخ انقضا؛ فقط وقتی سفارش واقعاً کامل شود مصرف می‌شود.
- **دعوت از دوستان** — لینک اختصاصی؛ با اولین خرید دوستِ دعوت‌شده، یک بار پاداش به کیف پول دعوت‌کننده اضافه می‌شود.
- **پنل مدیریت داخل ربات** — بررسی رسیدها، مدیریت پلن‌ها و کدها و کاربران، تنظیمات، آمار فروش و پیام همگانی.

### نصب

روی هر سروری که به پنل Rapido-Go دسترسی دارد (همان سرور پنل هم خوب است — ربات هیچ پورتی باز نمی‌کند):

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/legendary1205/rapidobot/master/rapidobot.sh) install
```

توکن ربات از [@BotFather](https://t.me/BotFather)، آیدی عددی تلگرام شما، و آدرس و یوزر/پسورد ادمین پنل را می‌پرسد — و قبل از روشن‌کردن ربات همه را امتحان می‌کند تا اشتباه تایپی همان لحظه معلوم شود، نه موقع اولین فروش.

بعد ربات را باز کنید، `/start` بزنید و از **⚙️ پنل مدیریت ← ⚙️ تنظیمات** شماره‌ی کارت را وارد کنید. دستور `rapidobot` به‌تنهایی همه‌ی دستورهای مدیریتی را نشان می‌دهد.
