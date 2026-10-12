# اجرا و استقرار

## محیط cloud فعلی

`make setup` ابزار Go را در `/workspace/tools/go` و cacheها را خارج از checkout قرار می‌دهد. artifact دانلودشده با SHA256 رسمی تأیید می‌شود؛ go.sum و module verification فعال‌اند. PostgreSQL در کانتینر `keepet-db` روی loopback و volume `keepet-postgres` است. `make run` وب را اجرا و migrations/seed را آماده می‌کند. `make test` به `keepet_test` محدود است.

فایل‌ها و dependencyهای نصب‌شده می‌توانند snapshot شوند؛ فرآیند وب و Docker database لزوماً با task جدید زنده نمی‌مانند. ابتدا `scripts/db.sh`، سپس `scripts/start.sh` اجرا و `/health` بررسی شود. برای task جدید worktree نسازید مگر کاربر صریحاً بخواهد.

## Docker

Dockerfile چندمرحله‌ای، build بدون CGO، اجرای برنامه با کاربر non-root و دارایی‌های embed دارد. compose شامل Postgres با healthcheck و برنامهٔ توسعه است. اگر home در cloud فقط‌خواندنی است:

```bash
DOCKER_CONFIG=/workspace/.cache/docker docker build -t keepet:dev .
```

compose دارای password عمومیِ **فقط توسعهٔ محلی** است؛ secret production محسوب نمی‌شود. `APP_MODE=production` اکنون fail-closed است و برنامه production-ready نیست. نسخهٔ آزمایشی با پذیرش کاربر برای تست روی آروان قابل اجراست؛ تنظیم دسترسی شبکه باید مطابق مخاطبان همان محیط تست باشد.

وابستگی‌های Go در `vendor/` همراه پروژه نگهداری می‌شوند. مرحلهٔ کامپایل Docker با `-mod=vendor`، `GOPROXY=off` و شبکهٔ غیرفعال اجرا می‌شود؛ دانلود فایل‌های ماژول از proxy.golang.org روی سرور لازم نیست. دریافت imageهای پایهٔ Docker هنوز به اتصال رجیستری نیاز دارد. هنگام تغییر وابستگی‌ها، ابتدا `go mod download` و `go mod verify`، سپس `go mod vendor` اجرا و تغییرات vendor همراه go.mod/go.sum بررسی شوند. نسخهٔ فعلی vendor از cache ماژول‌های تأییدشده تولید شده است؛ بررسی TLS یا checksum برای رفع خطای شبکه غیرفعال نشده است.

## مرحلهٔ ابرآروان

staging → production، TLS در Caddy، Postgres مدیریت‌شده با TLS، بکاپ و restore، healthcheck و محدودیت‌های proxy. در این کار deploy یا publish سایت به بیرون انجام نشده است. نمونهٔ config نهایی باید پس از تعیین دامنه و سرویس ساخته شود.

## اتصال سرویس‌ها

OTP مستقیم از برنامه به کاوه‌نگار، سایر پیام‌ها از outbox به n8n؛ پرداخت زرین‌پال request → redirect → verify سرور، idempotency، واحد تومان/ریال، تست failure/retry و refund دستی. هیچ key، placeholder credential یا API غیرواقعی اضافه نشده است. برای هر integration ابتدا دسترسی موجود تست و بعد secret در تنظیمات امن محیط ثبت شود؛ مقادیر در چت/کد/لاگ نوشته نشوند.

گفتگوی دامپزشکی هنگام شروع ویزیت در خود سایت ایجاد می‌شود و به فروشندهٔ چت خارجی نیاز ندارد. گرومینگ همچنان لینک ارتباط خارجی ثبت‌شده توسط ops دارد؛ اتصال خودکار فروشنده و پیامکِ لینک بعداً اضافه خواهد شد. ویدیو و تماس زنده پیاده نشده‌اند.

پروندهٔ گفتگو، پیام‌ها، تصویرها، پیام‌های صوتی و پرسشنامه‌ها در PostgreSQL نگهداری می‌شوند و با بازسازی image از بین نمی‌روند. به‌روزرسانی با `docker compose down` و سپس `up --build` volumeها را حفظ می‌کند؛ `down -v` داده را حذف می‌کند. برای حفظ فایل‌های گفتگو هم از volume دیتابیس بکاپ/restore بگیرید. محدودیت بدنه و timeoutهای reverse proxy باید با فایل صوتی ۱۲MiB و سربار multipart سازگار باشند. ضبط میکروفون در مرورگر به HTTPS یا localhost، اجازهٔ کاربر و پشتیبانی MediaRecorder نیاز دارد؛ ارسال فایل صوتی جایگزین است. [گفتگو و محدودیت‌ها](consultation-chat.md).

در محیط cloud، رجیستری Docker از تنظیمات پراکسی daemon استفاده می‌کند. نسخهٔ فعلی Dockerfile دانلود Go ندارد و برای مرحلهٔ کامپایل به proxy یا گواهی اضافه نیاز ندارد. دستور ساخت:

```bash
DOCKER_CONFIG=/workspace/.cache/docker docker build -t keepet:dev .
```

تأیید TLS هنگام دریافت imageها باقی می‌ماند. در ماشین دارای تنظیم Docker عادی، `docker build -t keepet:dev .` کافی است.

تنظیمات رمزگذاری‌شدهٔ درگاه، SMS و SMTP در پنل مدیریت ذخیره می‌شوند، ولی آداپترهای ارسال و پرداخت واقعی هنوز فعال نیستند. در Docker، volume جدید `app-secrets` کلید را نگه می‌دارد؛ پشتیبان این volume مستقل از PostgreSQL لازم است. [راهنمای مدیریت و نگهداری اعتبارنامه](platform-management.md).
