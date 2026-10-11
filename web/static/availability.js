(() => {
  const digits = value => String(value).replace(/\d/g, digit => '۰۱۲۳۴۵۶۷۸۹'[digit]);
  const dayFormat = new Intl.DateTimeFormat('fa-IR-u-ca-persian', {
    day: 'numeric', month: 'long', year: 'numeric', timeZone: 'Asia/Tehran'
  });
  const tehranClock = new Intl.DateTimeFormat('en-CA-u-ca-gregory-nu-latn', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
    hourCycle: 'h23', timeZone: 'Asia/Tehran'
  });
  function currentTehran() {
    const parts = Object.fromEntries(tehranClock.formatToParts(new Date()).map(part => [part.type, part.value]));
    return {day: `${parts.year}-${parts.month}-${parts.day}`, minute: Number(parts.hour) * 60 + Number(parts.minute)};
  }
  function clockMinute(value) {
    const match = /^(\d{2}):(\d{2})$/.exec(value || '');
    if (!match || Number(match[1]) > 23 || Number(match[2]) > 59) return null;
    return Number(match[1]) * 60 + Number(match[2]);
  }
  function clockText(minute) {
    return `${String(Math.floor(minute / 60)).padStart(2, '0')}:${String(minute % 60).padStart(2, '0')}`;
  }
  function localDay(day) {
    const date = new Date(`${day}T12:00:00+03:30`);
    return Number.isNaN(date.getTime()) ? day : dayFormat.format(date);
  }
  function bindAvailability(form) {
    if (form.dataset.availabilityBound) return;
    form.dataset.availabilityBound = 'true';
    const period = form.querySelector('[data-availability-period]');
    const custom = form.querySelector('[data-custom-window]');
    const start = form.querySelector('[data-window-start]');
    const end = form.querySelector('[data-window-end]');
    const day = form.querySelector('[data-availability-day]');
    if (!period || !custom || !start || !end) return;
    if (day) day.min = currentTehran().day;
    const validate = () => {
      const from = clockMinute(start.value);
      const to = clockMinute(end.value);
      end.setCustomValidity(period.value === 'custom' && from !== null && to !== null && to <= from
        ? 'ساعت پایان باید بعد از شروع و در همان روز باشد.' : '');
    };
    const update = () => {
      const active = period.value === 'custom';
      custom.hidden = !active;
      custom.style.display = active ? '' : 'none';
      [start, end].forEach(input => {input.disabled = !active; input.required = active;});
      validate();
    };
    period.addEventListener('change', update);
    start.addEventListener('input', validate);
    end.addEventListener('input', validate);
    form.addEventListener('submit', validate);
    update();
  }
  function bindAssignment(form) {
    if (form.dataset.assignmentBound) return;
    form.dataset.assignmentBound = 'true';
    const select = form.querySelector('[data-assignment-slot]');
    const clock = form.querySelector('[data-assignment-clock]');
    const summary = form.querySelector('[data-assignment-summary]');
    if (!select || !clock) return;
    Array.from(select.options).forEach(option => {
      const data = option.dataset;
      if (!data.slotDay) return;
      option.textContent = `${data.slotProvider} · ${localDay(data.slotDay)} · ${data.slotLabel} · ${digits(data.slotRemaining)} جای خالی`;
    });
    const update = autofill => {
      const option = select.selectedOptions[0];
      if (!option) return;
      const data = option.dataset;
      const from = clockMinute(data.slotStart);
      const to = clockMinute(data.slotEnd);
      const now = currentTehran();
      let minimum = from;
      if (data.slotDay === now.day && minimum !== null) minimum = Math.max(minimum, now.minute + 1);
      const maximum = to === null ? null : to - 1;
      const usable = minimum !== null && maximum !== null && minimum <= maximum && data.slotDay >= now.day;
      if (!usable) {
        clock.removeAttribute('min');
        clock.removeAttribute('max');
        if (autofill) clock.value = '';
        clock.setCustomValidity('این بازه دیگر زمان آیندهٔ قابل انتخاب ندارد. بازهٔ دیگری را انتخاب کنید.');
        if (summary) summary.textContent = 'این بازه دیگر زمان آیندهٔ قابل انتخاب ندارد؛ بازهٔ دیگری را انتخاب کنید یا صفحه را تازه کنید.';
        return;
      }
      clock.min = clockText(minimum);
      clock.max = clockText(maximum);
      let chosen = clockMinute(clock.value);
      if (autofill && (chosen === null || chosen < minimum || chosen > maximum)) {
        clock.value = clockText(minimum);
        chosen = minimum;
      }
      clock.setCustomValidity(chosen !== null && (chosen < minimum || chosen > maximum)
        ? 'ساعت دقیق باید در آینده و داخل بازهٔ انتخابی باشد؛ ساعت پایان بازه قابل انتخاب نیست.' : '');
      if (summary) summary.textContent = `${data.slotProvider} · ${localDay(data.slotDay)} · از ${digits(data.slotStart)} تا قبل از ${digits(data.slotEnd)} به وقت تهران. ساعت دقیق را داخل این بازه انتخاب کنید.`;
    };
    select.addEventListener('change', () => update(true));
    clock.addEventListener('input', () => update(false));
    form.addEventListener('submit', event => {
      update(false);
      if (!clock.checkValidity()) {event.preventDefault(); clock.reportValidity();}
    });
    update(true);
  }
  function enhance(root = document) {
    root.querySelectorAll('[data-availability-form]').forEach(bindAvailability);
    root.querySelectorAll('[data-assignment-window]').forEach(bindAssignment);
  }
  document.addEventListener('DOMContentLoaded', () => enhance());
  document.addEventListener('htmx:afterSwap', () => enhance());
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) document.querySelectorAll('[data-assignment-window] [data-assignment-clock]').forEach(input => input.dispatchEvent(new Event('input')));
  });
  if (document.readyState !== 'loading') enhance();
})();
