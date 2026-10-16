مرزهای واقعی مناطق ۲۲گانهٔ تهران — کیپت

منبع هندسه: مشارکت‌کنندگان OpenStreetMap، خروجی Mapzen منتشرشده در مخزن iran-map.
نسخهٔ منبع: مارس ۲۰۱۷؛ این داده مرز جغرافیایی واقعی است، نه نقشهٔ شماتیک.
ادعای به‌روزترین تقسیمات رسمی شهرداری یا دقت کاداستری ندارد.
شناسه‌های رابطهٔ OpenStreetMap در فایل GeoJSON نگهداری شده‌اند.

Pinned upstream:
https://github.com/yassermzh/iran-map/tree/dae7d65757e6935c3ad12a97056a4480211742d9
https://raw.githubusercontent.com/yassermzh/iran-map/dae7d65757e6935c3ad12a97056a4480211742d9/dist/tehran_areas.geojson

Copyright: © OpenStreetMap contributors
License: Open Data Commons Open Database License 1.0 (ODbL)
https://www.openstreetmap.org/copyright
https://opendatacommons.org/licenses/odbl/1-0/
متن مجوز در ODbL-1.0.txt همین پوشه قرار دارد.

tehran-districts.geojson شامل تمام مختصات اصلی است؛ فقط ویژگی‌های توصیفی استاندارد شده و نقطهٔ برچسب داخلی اضافه شده است.
config/tehran-district-paths.json نمایش SVG همان هندسه با پروجکشن Web Mercator است.
برای بازتولید: python3 scripts/build-tehran-map.py
دادهٔ مرزی محلی و قابل دریافت است؛ زیرلایهٔ خیابانی از tile.openstreetmap.org دریافت می‌شود.
نیازی به کلید API ندارد. اگر اینترنت زیرلایه در دسترس نباشد، مرزهای واقعی محلی کار می‌کنند.
ذخیره‌سازی یا پیش‌دانلود آفلاین کاشی‌های OpenStreetMap انجام نمی‌شود.

Leaflet 1.9.4: https://leafletjs.com — BSD-2-Clause، مجوز در /static/vendor/leaflet/LICENSE.
