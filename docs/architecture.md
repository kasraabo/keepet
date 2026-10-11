# معماری

- `cmd/server`: startup، timeoutهای HTTP و shutdown با SIGTERM.
- `internal/app`: session middleware، auth، گردش رزرو، قوانین قیمت/کنسلی، عملیات، ارائه‌دهنده و صفحات عمومی.
- `internal/ui/layout.templ`: پوستهٔ HTML و دارایی‌های صفحه؛ `layout_templ.go` generated.
- `web/templates`: صفحات server-rendered از `html/template`. خروجی این موتور بعد از escaping به پوستهٔ templ داده می‌شود؛ دادهٔ کاربر مستقیم به `templ.Raw` داده نمی‌شود.
- `web/static`: CSS، HTMX، Alpine، فونت وزیرمتن، SVGها، manifest و service worker. وابستگی runtime به CDN وجود ندارد.
- `migrations`: فایل‌های up/down برای golang-migrate؛ startup فقط Up را اجرا می‌کند. rollback باید آگاهانه و با بکاپ انجام شود.
- `config/triage.json`: پرسشنامهٔ شرطی. دادهٔ schema تشخیص یا شدت بالینی تولید نمی‌کند.

## داده

`users` نقش و شمارهٔ یکتا؛ `sessions` شناسهٔ تصادفی و CSRF؛ `otp_challenges` هش challenge+code، شمارنده و expiry؛ `providers` صلاحیت، گونه و پوشش؛ `availability` روز، بازه و ظرفیت؛ `pets` متعلق به کاربر؛ `bookings` پیش‌نویس و چرخهٔ رزرو؛ `booking_events` تایم‌لاین؛ `outbox` رویدادهای محلی؛ `applications` درخواست همکاری؛ `tickets` پشتیبانی؛ `reviews` بازخورد غیرعمومی.

PostgreSQL منبع حقیقت است. زمان‌ها در `timestamptz` ذخیره و در تهران نمایش داده می‌شوند. مبالغ در تومان و عدد صحیح‌اند؛ مبدل ریال/تومان هنگام اتصال پرداخت واقعی باید جداگانه تست شود.

## تراکنش‌های مهم

پرداخت booking و availability را قفل می‌کند، مالکیت، قیمت، آینده بودن و ظرفیت را دوباره بررسی می‌کند و وضعیت و event/outbox را با هم commit می‌کند. پرداخت دوم روی همان رزرو، رویداد تازه ایجاد نمی‌کند. تأیید OTP challenge را قفل و مصرف می‌کند و سشن را می‌چرخاند. throttle ارسال با قفل سشن و advisory lock شماره، بین درخواست‌های هم‌زمان سریال می‌شود. اقدامات ops/provider قفل booking و بررسی نقش، مالکیت و وضعیت دارند.

HTMX برای ارسال فرم و دریافت HTML استفاده می‌شود. فرم‌ها بدون JS هم کار می‌کنند؛ قابلیت‌های دینامیک، شمارنده و تبدیل تاریخ از Alpine/JS استفاده می‌کنند. سرور در پاسخ HTMX یا redirect صریح می‌دهد یا بدنهٔ صفحه را بازتعویض می‌کند؛ موتور SPA وجود ندارد.

## پیام

رویدادهای رزرو در همان تراکنش در outbox ذخیره می‌شوند و `status=local` دارند. ارسال، retry، delivery callback و cron هنوز اجرا نمی‌شوند. OTP مسیر مستقلی دارد؛ در حالت توسعه به جای ارسال، کد به کاربر تست نشان داده می‌شود. ماتریس ۱۶ پیام در سند سفر مرجع توسعهٔ worker بعدی است.
