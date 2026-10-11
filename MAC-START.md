# اجرای کیپت روی مک

۱. از منوی Apple → About This Mac، نوع پردازنده را ببینید.

- Apple M1/M2/M3/M4/...: Docker Desktop برای Apple Silicon
  https://desktop.docker.com/mac/main/arm64/Docker.dmg
- Intel: Docker Desktop برای Intel
  https://desktop.docker.com/mac/main/amd64/Docker.dmg
- راهنمای رسمی نصب: https://docs.docker.com/desktop/setup/install/mac-install/

۲. فایل Docker.dmg را باز کنید، Docker را به Applications بکشید و از Applications باز کنید. مراحل اولیه را انجام دهید و منتظر آماده‌شدن Docker بمانید.

۳. فایل [keepet-main.zip](https://github.com/kasraabo/keepet/archive/refs/heads/main.zip) را در Downloads دانلود و با دوبار کلیک باز کنید. پوشهٔ keepet-main شامل تمام فایل‌های لازم پروژه است. نیازی به نصب جداگانهٔ Go یا PostgreSQL، خرید سرور یا کلون مخزن نیست.

۴. Terminal را با Spotlight باز و اجرا کنید:

```bash
cd "$HOME/Downloads/keepet-main"
docker compose up --build
```

اگر پوشه جای دیگری است، `cd ` را تایپ کنید، پوشهٔ keepet-main را از Finder داخل Terminal بکشید و Enter بزنید؛ سپس دستور docker compose را اجرا کنید. این پوشه باید فایل compose.yaml داشته باشد. اولین اجرا اینترنت می‌خواهد و چند دقیقه زمان می‌برد.

۵. وقتی پیام `Keepet development server on :8080` نمایش داده شد، در Safari یا Chrome آدرس زیر را وارد کنید:

```text
http://localhost:8080
```

این آدرس روی همان مک که برنامه را اجرا کرده‌اید کار می‌کند.

۶. ورود را بزنید، شمارهٔ موبایل معتبر وارد و کد شش‌رقمی نمایش‌داده‌شده را استفاده کنید. پیامک و پرداخت واقعی نداریم. می‌توانید هر سه خدمت را رزرو کنید و در رزروهای من پیگیری کنید.

حساب‌های نمونهٔ عملیات و ارائه‌دهنده را در صفحهٔ ورود و README.md ببینید؛ برای بررسی پنل عملیات از شمارهٔ 09120000001 و یک پنجرهٔ خصوصی مرورگر استفاده کنید.

## توقف و اجرای مجدد

برای توقف در همان Terminal، Control+C بزنید؛ برای ادامه بعداً:

```bash
cd "$HOME/Downloads/keepet-main"
docker compose up
```

اطلاعات در volume دیتابیس حفظ می‌شوند. دستور `docker compose down -v` اطلاعات را حذف می‌کند؛ برای توقف معمولی به آن نیاز ندارید.

## خطاهای رایج

- `Cannot connect to the Docker daemon`: برنامهٔ Docker Desktop را باز کنید و تا آماده‌شدنش صبر کنید.
- `no configuration file provided`: Terminal در پوشهٔ keepet نیست؛ فایل compose.yaml باید در پوشهٔ فعلی باشد.
- `port is already allocated`: پورت 8080 توسط برنامهٔ دیگری استفاده می‌شود؛ در compose.yaml فقط نگاشت پورت را به `127.0.0.1:8082:8080` تغییر دهید و سایت را روی localhost:8082 باز کنید.
- خطای دانلود image/module: متن دقیق خطا را بفرستید؛ بررسی TLS یا checksum را خاموش نکنید.

این بسته نسخهٔ توسعه است؛ برای کار واقعی عمومی باید اتصال‌ها و مراحل سخت‌سازی تکمیل شوند. جزئیات قابلیت‌ها و مراحل بعدی در README.md و docs/roadmap.md است.


## پنل مدیریت کسب‌وکار

در سایت از حساب فعلی خارج شوید و با شمارهٔ `09120000005` و کد نمایش‌داده‌شده وارد شوید. ورود مستقل مدیر در `http://localhost:8080/management/login` است؛ سپس `http://localhost:8080/management` را باز کنید. راهنمای قابلیت‌ها در [مدیریت کسب‌وکار](docs/business-admin.md) است.

## به‌روزرسانی نسخهٔ قبلی بدون حذف اطلاعات

Docker Desktop را باز نگه دارید. در Terminal قبلی Control+C بزنید. از Downloads فایل ZIP تازهٔ مخزن را دانلود و باز کنید. Finder ممکن است نام پوشه را `keepet-main 2` بگذارد. محتویات پوشهٔ تازه را در پوشهٔ قبلی `keepet-main` کپی کنید و جایگزینی فایل‌ها را بپذیرید. نام پوشهٔ اصلی را عوض نکنید تا volume دیتابیس همان قبلی بماند. سپس عیناً اجرا کنید:

```bash
cd "$HOME/Downloads/keepet-main"
docker compose up --build
```

مهاجرت پنل خودکار اجرا می‌شود و رزروهای قبلی حفظ می‌شوند. اگر `auth.docker.io` timeout داد، دسترسی شبکهٔ Docker به Docker Hub را برقرار کنید و همین دستور را دوباره اجرا کنید.
