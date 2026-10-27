(function () {
  'use strict';
  if (window.KeepetChat) {
    window.KeepetChat.init();
    return;
  }

  const digits = value => String(value).replace(/\d/g, d => '۰۱۲۳۴۵۶۷۸۹'[Number(d)]);
  const dateFormat = new Intl.DateTimeFormat('fa-IR-u-ca-persian', {
    day: 'numeric', month: 'long', hour: '2-digit', minute: '2-digit', timeZone: 'Asia/Tehran'
  });
  const imageLimit = 8 * 1024 * 1024;
  const audioLimit = 12 * 1024 * 1024;
  const imageTypes = new Set(['image/jpeg', 'image/png', 'image/webp']);
  const audioTypes = new Set(['audio/webm', 'audio/ogg', 'audio/mp4', 'audio/mpeg', 'audio/wav', 'audio/x-wav', 'video/webm']);
  const recorderTypes = ['audio/webm;codecs=opus', 'audio/ogg;codecs=opus', 'audio/mp4', 'audio/webm'];

  function element(tag, className, content) {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (content !== undefined) el.textContent = content;
    return el;
  }

  function readableTime(value) {
    const at = new Date(value);
    return Number.isNaN(at.getTime()) ? '' : dateFormat.format(at);
  }

  function formatDates(root) {
    root.querySelectorAll('[data-datetime]').forEach(el => {
      const display = readableTime(el.dataset.datetime);
      if (display) el.textContent = display;
    });
  }

  function mediaType(file) {
    const type = file.type.split(';')[0].toLowerCase();
    if (imageTypes.has(type)) return 'image';
    if (audioTypes.has(type)) return 'audio';
    if (!type && /\.(webm|ogg|m4a|mp3|wav)$/i.test(file.name)) return 'audio';
    return '';
  }

  function initChat(root) {
    if (root.dataset.chatBound) return;
    root.dataset.chatBound = 'true';
    const id = root.dataset.chatId;
 const medical = root.dataset.chatKind === 'vet';
    const list = root.querySelector('[data-chat-messages]');
    const form = root.querySelector('[data-chat-composer]');
    const text = root.querySelector('[data-chat-text]');
    const fileInput = root.querySelector('[data-chat-file]');
    const preview = root.querySelector('[data-chat-file-preview]');
    const send = root.querySelector('[data-chat-send]');
    const record = root.querySelector('[data-chat-record]');
    const recordBox = root.querySelector('[data-chat-recording]');
    const connection = root.querySelector('[data-chat-connection]');
    const error = root.querySelector('[data-chat-error]');
    const timer = root.querySelector('[data-chat-timer]');
    const timerCaption = root.querySelector('[data-chat-timer-caption]');
    const stateLabel = root.querySelector('[data-chat-state-label]');
    const notice = root.querySelector('[data-chat-room-notice]');
    const newMessage = root.querySelector('[data-chat-scroll]');
    const participant = root.dataset.chatParticipant === 'true';
    const seen = new Set(Array.from(list.querySelectorAll('[data-message-id]'), el => el.dataset.messageId));
    let state = root.dataset.chatState;
    let expires = root.dataset.chatExpires;
    let done = root.dataset.chatQuestionnaireDone === 'true';
    let canSend = !!send && !send.disabled;
    let busy = false;
    let fetching = false;
    let disposed = false;
    let redirecting = false;
    let selectedFile = null;
    let previewURL = '';
    let stream = null;
    let recorder = null;
    let recording = false;
    let recordPending = false;
    let recordStarted = 0;
    let recordInterval = null;
    let recordChunks = [];
    let recordBytes = 0;
    let discardRecording = false;
    let abort = null;
    let sendAbort = null;

    const messagesURL = '/chats/' + encodeURIComponent(id) + '/messages';
    const questionnaireURL = '/chats/' + encodeURIComponent(id) + '/questionnaire';

let acked='0', acknowledging=false;
 async function acknowledge(){
 if(!participant||document.hidden||!atBottom()||acknowledging||disposed)return;
 const last=Array.from(list.querySelectorAll('[data-message-id]')).reduce((a,e)=>BigInt(e.dataset.messageId)>BigInt(a)?e.dataset.messageId:a,'0');
 if(last==='0'||BigInt(last)<=BigInt(acked))return;
 const csrf=root.querySelector('input[name="csrf"]');if(!csrf)return;
 acknowledging=true;try{const res=await fetch('/chats/'+encodeURIComponent(id)+'/read',{method:'POST',credentials:'same-origin',headers:{'Accept':'application/json','Content-Type':'application/x-www-form-urlencoded'},body:new URLSearchParams({csrf:csrf.value,last_message_id:last})});if(res.ok)acked=last;}catch(e){}finally{acknowledging=false;}
 }
    function showError(message) {
      error.textContent = message;
      error.hidden = !message;
    }

    function updateControls() {
      if (!form) return;
      const disabled = !canSend || busy;
      text.disabled = disabled;
      fileInput.disabled = disabled || recording;
      if (fileInput.closest('label')) fileInput.closest('label').setAttribute('aria-disabled', String(fileInput.disabled));
      send.disabled = disabled || recording;
      record.disabled = disabled || recording || recordPending;
      send.firstChild.textContent = busy ? 'در حال ارسال… ' : 'ارسال ';
    }

    function clearPreview() {
      selectedFile = null;
      if (fileInput) fileInput.value = '';
      if (previewURL) URL.revokeObjectURL(previewURL);
      previewURL = '';
      if (preview) {
        preview.replaceChildren();
        preview.hidden = true;
      }
    }

    function chooseFile(file) {
      if (!file) {
        clearPreview();
        return;
      }
      const kind = mediaType(file);
      if (!kind) {
        showError('فقط تصویر JPG، PNG یا WebP و فایل صوتی WebM، OGG، MP3، M4A یا WAV قابل ارسال است.');
        clearPreview();
        return;
      }
      if (file.size === 0 || file.size > (kind === 'image' ? imageLimit : audioLimit)) {
        showError(kind === 'image' ? 'اندازهٔ تصویر باید بین ۱ بایت و ۸ مگابایت باشد.' : 'اندازهٔ فایل صوتی باید بین ۱ بایت و ۱۲ مگابایت باشد.');
        clearPreview();
        return;
      }
      if (previewURL) URL.revokeObjectURL(previewURL);
      selectedFile = file;
      previewURL = URL.createObjectURL(file);
      preview.replaceChildren();
      if (kind === 'image') {
        const img = element('img');
        img.src = previewURL;
        img.alt = 'پیش‌نمایش تصویر انتخاب‌شده';
        preview.append(img);
      } else {
        const audio = element('audio');
        audio.src = previewURL;
        audio.controls = true;
        audio.preload = 'metadata';
        preview.append(audio);
      }
      const details = element('div', 'chat-file-description');
      details.append(element('strong', '', file.name));
      details.append(element('small', '', digits((file.size / 1024 / 1024).toFixed(1)) + ' مگابایت · هنوز ارسال نشده'));
      const remove = element('button', 'chat-remove-file', 'حذف پیوست');
      remove.type = 'button';
      remove.addEventListener('click', clearPreview);
      preview.append(details, remove);
      preview.hidden = false;
      showError('');
    }

    function closeStream() {
      if (stream) stream.getTracks().forEach(track => track.stop());
      stream = null;
    }

    function stopRecording(discard) {
      discardRecording = !!discard;
      if (recordInterval) clearInterval(recordInterval);
      recordInterval = null;
      if (recorder && recorder.state !== 'inactive') recorder.stop();
      else closeStream();
      recording = false;
      if (recordBox) recordBox.hidden = true;
      updateControls();
    }

    async function startRecording() {
      if (!canSend || busy || recording || recordPending) return;
      if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia || !window.MediaRecorder) {
        showError('ضبط صدا در این مرورگر در دسترس نیست. می‌توانید فایل صوتی را از دکمهٔ «پیوست» انتخاب کنید.');
        return;
      }
      recordPending = true;
      try {
        updateControls();
        stream = await navigator.mediaDevices.getUserMedia({audio: true});
        if (disposed || !canSend || !root.isConnected) {
          closeStream();
          return;
        }
        const mimeType = recorderTypes.find(type => MediaRecorder.isTypeSupported(type));
        recorder = mimeType ? new MediaRecorder(stream, {mimeType}) : new MediaRecorder(stream);
        recordChunks = [];
        recordBytes = 0;
        discardRecording = false;
        recorder.addEventListener('dataavailable', event => {
          if (!event.data.size) return;
          recordChunks.push(event.data);
          recordBytes += event.data.size;
          if (recordBytes > audioLimit) {
            showError('ضبط صدا به سقف ۱۲ مگابایت رسید. لطفاً یک پیام کوتاه‌تر ضبط کنید.');
            stopRecording(true);
          }
        });
        recorder.addEventListener('stop', () => {
          closeStream();
          const type = recorder.mimeType || mimeType || 'audio/webm';
          if (!discardRecording && canSend && !disposed && recordChunks.length) {
            const blob = new Blob(recordChunks, {type});
            const ext = type.includes('mp4') ? 'm4a' : type.includes('ogg') ? 'ogg' : 'webm';
            const file = new File([blob], 'voice-' + Date.now() + '.' + ext, {type});
            chooseFile(file);
            // The hidden file input also holds the recording when the browser supports it,
            // so a normal multipart submission can still send it without fetch.
            if (typeof DataTransfer !== 'undefined') {
              try {
                const transfer = new DataTransfer();
                transfer.items.add(file);
                fileInput.files = transfer.files;
              } catch (_) { /* FormData below supplies the File regardless. */ }
            }
          }
          recordChunks = [];
          recording = false;
          recordBox.hidden = true;
          updateControls();
        });
        recorder.addEventListener('error', () => {
          stopRecording(true);
          showError('ضبط صدا انجام نشد؛ دوباره امتحان کنید یا فایل صوتی را پیوست کنید.');
        });
        clearPreview();
        showError('');
        recording = true;
        recordPending = false;
        recordStarted = Date.now();
        recordBox.hidden = false;
        root.querySelector('[data-chat-record-time]').textContent = '۰۰:۰۰';
        recorder.start(1000);
        recordInterval = setInterval(() => {
          const seconds = Math.floor((Date.now() - recordStarted) / 1000);
          root.querySelector('[data-chat-record-time]').textContent = digits(String(Math.floor(seconds / 60)).padStart(2, '0') + ':' + String(seconds % 60).padStart(2, '0'));
          // Bound microphone capture even if the sender leaves the page unattended.
          if (seconds >= 300) stopRecording(false);
        }, 1000);
        updateControls();
      } catch (exception) {
        closeStream();
        showError(exception.name === 'NotAllowedError' ? 'برای ضبط صدا، دسترسی به میکروفون را در مرورگر فعال کنید؛ انتخاب فایل صوتی هم در دسترس است.' : 'میکروفون در دسترس نیست. می‌توانید یک فایل صوتی پیوست کنید.');
        recordPending = false;
        updateControls();
      }
    }

    function atBottom() {
      return list.scrollHeight - list.scrollTop - list.clientHeight < 100;
    }

    function scrollBottom() {
      list.scrollTop = list.scrollHeight;
      newMessage.hidden = true;
    }

    function appendMessage(message) {
      const messageID = String(message.id);
      if (seen.has(messageID)) return false;
      seen.add(messageID);
      const empty = list.querySelector('[data-chat-empty]');
      if (empty) empty.remove();
      const article = element('article', 'chat-message' + (message.own ? ' chat-message-own' : ''));
      article.dataset.messageId = messageID;
      const meta = element('div', 'chat-message-meta');
      meta.append(element('strong', '', message.sender_name));
      meta.append(element('span', '', message.role === 'vet' ? 'دامپزشک' : message.role === 'owner' ? 'صاحب حیوان' : message.role==='provider' ? 'متخصص' : 'مدیریت'));
      if(message.unread){article.classList.add('chat-message-new');meta.append(element('span','chat-unread-label','پیام جدید'));}
 article.append(meta);
      if (message.body) {
        const body = element('p', 'chat-message-body', message.body);
        body.dir = 'auto';
        article.append(body);
      }
      if (message.attachment_id) {
        const url = '/chat-media/' + encodeURIComponent(message.attachment_id);
        if (message.kind === 'image') {
          const link = element('a', 'chat-image-link');
          link.href = url;
          link.target = '_blank';
          link.rel = 'noopener';
          const img = element('img', 'chat-message-image');
          img.src = url;
          img.alt = 'تصویر ارسالی ' + message.sender_name;
          img.loading = 'lazy';
          img.addEventListener('load', () => { if (atBottom()) scrollBottom(); });
          link.append(img);
          article.append(link);
        } else {
          const audio = element('audio', 'chat-message-audio');
          audio.controls = true;
          audio.preload = 'metadata';
          audio.src = url;
          article.append(audio);
        }
        const filename = element('a', 'chat-attachment-name', message.filename || 'مشاهدهٔ پیوست');
        filename.href = url;
        filename.target = '_blank';
        filename.rel = 'noopener';
        article.append(filename);
      }
      const at = element('time', 'chat-message-time', readableTime(message.at));
      at.dateTime = message.at;
      article.append(at);
      list.append(article);
      return true;
    }

    function updateState(data) {
      const previous = state;
      state = data.state;
      expires = data.expires_at || '';
      done = !!data.questionnaire_done;
      canSend = !!data.can_send;
      root.dataset.chatState = state;
      stateLabel.className = 'chat-status chat-status-' + state;
      stateLabel.textContent = state === 'active' ? 'گفت‌وگوی در حال انجام' : state === 'scheduled' ? 'در انتظار شروع دامپزشک' : 'گفت‌وگوی بایگانی‌شده';
      if (state === 'archived') {
        notice.hidden = false;
        notice.textContent = 'گفت‌وگو پایان یافته و در پرونده بایگانی شده است. پیام‌ها و پیوست‌ها محفوظ‌اند.';
        root.querySelectorAll('[data-chat-end]').forEach(el => { el.hidden = true; });
        root.querySelectorAll('.chat-admin-duration').forEach(el => { el.hidden = true; });
        if (recording) stopRecording(true);
        if (previous !== 'archived' && participant && (root.dataset.chatRole==='owner' || (medical && !done)) && !redirecting) {
          redirecting = true;
          window.location.assign(root.dataset.chatRole==='owner' ? '/dashboard' : questionnaireURL);
        }
      } else if (state === 'scheduled') {
        notice.hidden = false;
        notice.textContent = 'بعد از شروع ویزیت توسط دامپزشک، ارسال پیام فعال می‌شود.';
      } else {
        notice.hidden = true;
      }
      updateControls();
      updateTimer();
    }

    function updateTimer() {
      if (state === 'archived') {
        timer.textContent = 'در پرونده محفوظ است';
        timerCaption.textContent = 'ارسال پیام تازه بسته شده است.';
        return;
      }
      if(!medical && state==='active'){timer.textContent='هماهنگی خدمت فعال است';timerCaption.textContent='پس از پایان فرایند خدمت بایگانی می‌شود.';return;}
 if (state !== 'active' || !expires) return;
      const expiry = new Date(expires).getTime();
      if (Number.isNaN(expiry)) return;
      const seconds = Math.max(0, Math.ceil((expiry - Date.now()) / 1000));
      timer.textContent = digits(String(Math.floor(seconds / 60)).padStart(2, '0') + ':' + String(seconds % 60).padStart(2, '0'));
      timerCaption.textContent = seconds ? 'زمان باقی‌ماندهٔ ارسال پیام' : 'مهلت ارسال تمام شد؛ در حال ثبت بایگانی…';
      if (seconds === 0 && canSend) {
        canSend = false;
        updateControls();
        poll();
      }
    }

    async function poll(forceScroll) {
      if (disposed || fetching || !root.isConnected || (document.hidden && !forceScroll)) return;
      fetching = true;
      const controller = new AbortController();
      abort = controller;
      const requestTimeout = setTimeout(() => controller.abort(), 15000);
      try {
        const response = await fetch(messagesURL, {headers: {'Accept': 'application/json'}, credentials: 'same-origin', cache: 'no-store', signal: controller.signal});
        if (!response.ok || !(response.headers.get('Content-Type') || '').includes('application/json')) {
          if (response.status === 401 || response.status === 403) showError('دسترسی گفت‌وگو یا ورود شما تغییر کرده است؛ صفحه را دوباره باز کنید.');
          throw new Error('poll');
        }
        const data = await response.json();
        const shouldScroll = atBottom() || forceScroll;
        let added = false;
        (data.messages || []).forEach(message => { added = appendMessage(message) || added; });
        if (added) {
          if (shouldScroll) scrollBottom();
          else newMessage.hidden = false;
        }
        connection.textContent = data.state === 'archived' ? 'بایگانی' : 'متصل';
        connection.dataset.offline = 'false';
        updateState(data);
 acknowledge();
      } catch (exception) {
        if (!disposed) {
          connection.textContent = 'در انتظار اتصال';
          connection.dataset.offline = 'true';
        }
      } finally {
        clearTimeout(requestTimeout);
        fetching = false;
        abort = null;
      }
    }

    if (form) {
      fileInput.addEventListener('change', () => chooseFile(fileInput.files[0]));
      record.addEventListener('click', startRecording);
      root.querySelector('[data-chat-record-stop]').addEventListener('click', () => stopRecording(false));
      root.querySelector('[data-chat-record-discard]').addEventListener('click', () => stopRecording(true));
      text.addEventListener('keydown', event => {
        if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && canSend && !busy) {
          event.preventDefault();
          form.requestSubmit();
        }
      });
      if (window.fetch && window.FormData) form.addEventListener('submit', async event => {
        event.preventDefault();
        if (busy || !canSend || recording) return;
        if (!text.value.trim() && !selectedFile) {
          showError('یک پیام بنویسید یا تصویر و فایل صوتی انتخاب کنید.');
          text.focus();
          return;
        }
        busy = true;
        showError('');
        const body = new FormData(form);
        if (selectedFile) body.set('file', selectedFile, selectedFile.name);
        else body.delete('file');
        updateControls();
        const controller = new AbortController();
        sendAbort = controller;
        const requestTimeout = setTimeout(() => controller.abort(), 90000);
        try {
          const response = await fetch(form.action, {method: 'POST', body, credentials: 'same-origin', headers: {'Accept': 'application/json'}, signal: controller.signal});
          const contentType = response.headers.get('Content-Type') || '';
          if (!contentType.includes('application/json')) throw new Error('پاسخ ارسال دریافت نشد؛ اتصال و وضعیت ورود را بررسی کنید.');
          const data = await response.json();
          if (!response.ok || data.error) throw new Error(data.error || 'پیام ارسال نشد؛ دوباره امتحان کنید.');
          clearTimeout(requestTimeout);
          text.value = '';
          clearPreview();
          await poll(true);
          if (canSend) text.focus();
        } catch (exception) {
          if (!disposed) {
            const unknown = exception.name === 'AbortError' || exception.name === 'TypeError';
            showError(unknown ? 'پاسخ ارسال دریافت نشد؛ ممکن است پیام ذخیره شده باشد. پیش از ارسال دوباره، تاریخچه را بررسی کنید. متن و پیوست شما حفظ شده‌اند.' : exception.message);
            poll();
          }
        } finally {
          clearTimeout(requestTimeout);
          sendAbort = null;
          busy = false;
          updateControls();
        }
      });
    }

    newMessage.addEventListener('click', ()=>{scrollBottom();acknowledge();});
    list.addEventListener('scroll', () => { if (atBottom()) {newMessage.hidden = true;acknowledge();} });
    root.querySelectorAll('[data-chat-leave]').forEach(link => link.addEventListener('click', () => stopRecording(true)));
    const onVisible = () => { if (!document.hidden) poll(); };
    document.addEventListener('visibilitychange', onVisible);
    const pollInterval = setInterval(() => {
      if (!root.isConnected) dispose();
      else if (state !== 'archived') poll();
    }, 3000);
    const timerInterval = setInterval(updateTimer, 1000);

    function dispose() {
      if (disposed) return;
      disposed = true;
      clearInterval(pollInterval);
      clearInterval(timerInterval);
      document.removeEventListener('visibilitychange', onVisible);
      if (abort) abort.abort();
      if (sendAbort) sendAbort.abort();
      stopRecording(true);
      closeStream();
      if (previewURL) URL.revokeObjectURL(previewURL);
    }

    window.addEventListener('pagehide', dispose, {once: true});
    formatDates(root);
    updateControls();
    updateTimer();
    scrollBottom();
 acknowledge();
    requestAnimationFrame(() => { list.setAttribute('aria-live', 'polite'); });
    if (state !== 'archived') poll();
  }

  function init() {
    document.querySelectorAll('[data-chat-root]').forEach(initChat);
    document.querySelectorAll('.chat-questionnaire-page,.chat-archive-page').forEach(formatDates);
  }

  window.KeepetChat = {init};
  // pagehide releases the microphone and polling. A cached history page must
  // reload its authenticated server state before accepting another message.
  window.addEventListener('pageshow', event => {
    if (event.persisted && document.querySelector('[data-chat-root]')) window.location.reload();
  });
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
  document.addEventListener('htmx:afterSwap', init);
})();
