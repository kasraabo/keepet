# اجرا و استقرار

## محیط cloud فعلی

`make setup` ابزار Go را در `/workspace/tools/go` و cacheها را خارج از checkout قرار می‌دهد. artifact دانلودشده با SHA256 رسمی تأیید می‌شود؛ go.sum و module verification فعال‌اند. PostgreSQL در کانتینر `keepet-db` روی loopback و volume `keepet-postgres` است. `make run` وب را اجرا و migrations/seed را آماده می‌کند. `make test` به `keepet_test` محدود است.

فایل‌ها و dependencyهای نصب‌شده می‌توانند snapshot شوند؛ فرآیند وب و Docker database لزوماً با task جدید زنده نمی‌مانند. ابتدا `scripts/db.sh`، سپس `scripts/start.sh` اجرا و `/health` بررسی شود. برای task جدید worktree نسازید مگر کاربر صریحاً بخواهد.

## Docker

Dockerfile چندمرحله‌ای، build بدون CGO، اجرای برنامه با کاربر non-root و دارایی‌های embed دارد. compose شامل Postgres با healthcheck و برنامهٔ توسعه است. اگر home در cloud فقط‌خواندنی است:

```bash
DOCKER_CONFIG=/workspace/.cache/docker docker build -t keepet:dev .
```

compose دارای password عمومیِ **فقط توسعهٔ محلی** است؛ secret production محسوب نمی‌شود. قبل از deployment عمومی این mode و seedها باید با integrationهای واقعی جایگزین شوند. `APP_MODE=production` اکنون fail-closed است و برنامه production-ready نیست.

## مرحلهٔ ابرآروان

staging → production، TLS در Caddy، Postgres مدیریت‌شده با TLS، بکاپ و restore، healthcheck و محدودیت‌های proxy. در این کار deploy یا publish سایت به بیرون انجام نشده است. نمونهٔ config نهایی باید پس از تعیین دامنه و سرویس ساخته شود.

## اتصال سرویس‌ها

OTP مستقیم از برنامه به کاوه‌نگار، سایر پیام‌ها از outbox به n8n؛ پرداخت زرین‌پال request → redirect → verify سرور، idempotency، واحد تومان/ریال، تست failure/retry و refund دستی. هیچ key، placeholder credential یا API غیرواقعی اضافه نشده است. برای هر integration ابتدا دسترسی موجود تست و بعد secret در تنظیمات امن محیط ثبت شود؛ مقادیر در چت/کد/لاگ نوشته نشوند.

لینک جلسه فعلاً توسط ops ثبت می‌شود؛ اتصال خودکار فروشندهٔ ایرانی و پیامکِ لینک بعداً اضافه خواهد شد.

در cloud، Docker build ممکن است DNS کانتینر پراکسی و CA آن را به ارث نبرد. مسیر آزمایش‌شدهٔ build با bundle معتبر همان ماشین، بدون غیرفعال‌سازی TLS:

```bash
DOCKER_CONFIG=/workspace/.cache/docker docker build \
  --network host \
  --add-host "proxy:$(getent ahostsv4 proxy | awk 'NR==1{print $1}')" \
  --build-arg HTTP_PROXY --build-arg HTTPS_PROXY --build-arg NO_PROXY \
  --secret id=build_ca,src=/etc/ssl/certs/ca-certificates.crt \
  -t keepet:dev .
```

این تنظیم مخصوص پراکسی cloud است. bundle از secret mount فقط هنگام build استفاده می‌شود و در image یا repository ذخیره نمی‌شود. در ماشین دارای اتصال عادی، `docker build -t keepet:dev .` کافی است.
