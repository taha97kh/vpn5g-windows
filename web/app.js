// VPN 5G — web UI logic. Vanilla JS against the local JSON API.
(function () {
  'use strict';

  var $ = function (id) { return document.getElementById(id); };
  var state = { connected: false, serverId: null, servers: [], updatePoll: null };

  function api(path, opts) {
    return fetch(path, opts).then(function (r) { return r.json(); });
  }

  function show(el, on) { el.classList.toggle('hidden', !on); }
  function showError(el, msg) { el.textContent = msg; show(el, !!msg); }

  // ---- Tabs ----
  document.querySelectorAll('.tab').forEach(function (btn) {
    btn.addEventListener('click', function () {
      document.querySelectorAll('.tab').forEach(function (b) { b.classList.remove('active'); });
      document.querySelectorAll('.tab-page').forEach(function (p) { p.classList.remove('active'); });
      btn.classList.add('active');
      $('tab-' + btn.dataset.tab).classList.add('active');
    });
  });

  // ---- Status / countdown ----
  function fmtTime(sec) {
    var m = Math.floor(sec / 60), s = sec % 60;
    return (m < 10 ? '0' : '') + m + ':' + (s < 10 ? '0' : '') + s;
  }

  function renderStatus(st) {
    state.connected = !!st.connected;
    $('power').classList.toggle('on', state.connected);
    $('status-text').textContent = state.connected ? 'متصل است ✓' : 'قطع است';
    if (state.connected) {
      state.serverId = st.server_id;
      $('server-line').textContent = (st.country || '') + ' — ' + (st.server_name || '') +
        ' (' + (st.protocol || '') + ', ' + (st.ping_ms || 0) + 'ms)';
      show($('countdown'), true);
      $('countdown-time').textContent = fmtTime(st.remaining_seconds || 0);
    } else {
      state.serverId = null;
      $('server-line').textContent = 'سروری انتخاب نشده — از تب سرورها یکی را انتخاب کنید';
      show($('countdown'), false);
    }
    if (st.version) $('version').textContent = st.version;
    renderServers();
  }

  function refreshStatus() {
    api('/api/status').then(renderStatus).catch(function () {});
  }
  refreshStatus();
  setInterval(refreshStatus, 1000);

  // ---- Power button: disconnect, or connect to first free server ----
  $('power').addEventListener('click', function () {
    showError($('home-error'), '');
    if (state.connected) {
      api('/api/disconnect', { method: 'POST' }).then(refreshStatus);
      return;
    }
    var first = state.servers.find(function (s) { return s.free; });
    if (!first) { showError($('home-error'), 'هنوز سروری آماده نیست — کمی صبر کنید و لیست را بروزرسانی کنید.'); return; }
    connectTo(first.id);
  });

  function connectTo(id) {
    showError($('home-error'), '');
    $('vip-note').classList.add('hidden');
    api('/api/connect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ server_id: id })
    }).then(function (res) {
      if (res.ok) { refreshStatus(); return; }
      if (res.error === 'vip') {
        $('vip-note').classList.remove('hidden');
        document.querySelector('[data-tab="servers"]').click();
        return;
      }
      showError($('home-error'), res.error || 'اتصال ناموفق بود');
    }).catch(function (e) { showError($('home-error'), String(e)); });
  }

  // ---- Servers ----
  function loadServers() {
    return api('/api/servers').then(function (res) {
      state.servers = res.servers || [];
      var n = state.servers.length;
      $('servers-count').textContent = n
        ? n + ' سرور آماده (' + state.servers.filter(function (s) { return s.free; }).length + ' رایگان)'
        : (res.refreshing ? 'در حال بررسی سرورها…' : 'هنوز سروری پیدا نشده است');
      renderServers();
    }).catch(function () {});
  }

  function renderServers() {
    var list = $('servers-list');
    var q = ($('search').value || '').trim();
    var servers = state.servers.filter(function (s) {
      if (!q) return true;
      return (s.country + ' ' + s.name + ' ' + s.protocol).indexOf(q) !== -1;
    });
    if (!servers.length) {
      list.innerHTML = '<div class="empty">سروری مطابق جستجو نیست.<br>لیست هر ۵ دقیقه خودکار بروز می‌شود و فقط سرورهایی که پاسخ می‌دهند نمایش داده می‌شوند.</div>';
      return;
    }
    var groups = {};
    var order = [];
    servers.forEach(function (s) {
      var c = s.country || 'Other';
      if (!groups[c]) { groups[c] = []; order.push(c); }
      groups[c].push(s);
    });
    var html = '';
    order.forEach(function (country) {
      html += '<div class="country-group"><h3>' + esc(country) + '</h3>';
      groups[country].forEach(function (s) {
        html += '<div class="server' + (s.id === state.serverId ? ' active' : '') + '" data-id="' + s.id + '">' +
          '<div class="name">' + esc(s.name) + '</div>' +
          '<div class="meta"><span class="proto">' + esc(s.protocol) + '</span>' +
          '<span class="ping">' + s.ping_ms + 'ms</span>' +
          (s.free ? '<span class="badge free">رایگان</span>' : '<span class="badge vip">VIP</span>') +
          '</div></div>';
      });
      html += '</div>';
    });
    list.innerHTML = html;
    list.querySelectorAll('.server').forEach(function (el) {
      el.addEventListener('click', function () { connectTo(el.dataset.id); });
    });
  }

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  $('search').addEventListener('input', renderServers);
  $('btn-refresh').addEventListener('click', function () { loadServers(); });
  loadServers();
  setInterval(loadServers, 30000);

  // ---- Auth ----
  function refreshAuth() {
    api('/api/auth/status').then(function (res) {
      show($('auth-signed-in'), !!res.signed_in);
      show($('auth-form'), !res.signed_in);
      if (res.signed_in) $('auth-email').textContent = res.email || '';
    }).catch(function () {});
  }
  refreshAuth();

  $('btn-send-code').addEventListener('click', function () {
    var email = $('auth-email-input').value.trim();
    showError($('auth-error'), '');
    show($('auth-ok'), false);
    $('btn-send-code').disabled = true;
    api('/api/auth/send-code', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: email })
    }).then(function (res) {
      $('btn-send-code').disabled = false;
      if (res.ok) {
        show($('code-row'), true);
        $('auth-ok').textContent = 'کد ۶ رقمی به ایمیلت ارسال شد.';
        show($('auth-ok'), true);
      } else {
        showError($('auth-error'), res.error || 'ارسال کد ناموفق بود');
      }
    }).catch(function (e) { $('btn-send-code').disabled = false; showError($('auth-error'), String(e)); });
  });

  $('btn-verify').addEventListener('click', function () {
    var email = $('auth-email-input').value.trim();
    var code = $('auth-code-input').value.trim();
    showError($('auth-error'), '');
    api('/api/auth/verify', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: email, code: code })
    }).then(function (res) {
      if (res.ok) { refreshAuth(); }
      else { showError($('auth-error'), res.error || 'کد اشتباه است یا منقضی شده'); }
    }).catch(function (e) { showError($('auth-error'), String(e)); });
  });

  $('btn-logout').addEventListener('click', function () {
    api('/api/auth/logout', { method: 'POST' }).then(refreshAuth);
  });

  // ---- In-app update ----
  var updateDismissed = false;

  function checkUpdate() {
    api('/api/update/check').then(function (res) {
      if (res.update_available && !updateDismissed) {
        $('update-version').textContent = 'نسخه ' + res.latest_version + ' (فعلی: ' + res.current_version + ')';
        show($('update-banner'), true);
      } else if (!res.update_available) {
        show($('update-banner'), false);
      }
      if (res.state === 'downloading' || res.state === 'downloaded' || res.state === 'applying') {
        renderUpdateProgress(res);
      }
    }).catch(function () {});
  }

  function renderUpdateProgress(res) {
    show($('update-progress'), true);
    var pct = res.total > 0 ? Math.round(100 * res.downloaded / res.total) : 0;
    $('update-progress-fill').style.width = pct + '%';
    if (res.state === 'downloading') {
      $('update-progress-text').textContent = 'در حال دانلود… ' + pct + '%';
      $('btn-update').disabled = true;
    } else if (res.state === 'downloaded') {
      $('update-progress-text').textContent = 'دانلود کامل شد — برای نصب و راه‌اندازی مجدد، «نصب بروزرسانی» را بزنید.';
      $('btn-update').textContent = 'نصب بروزرسانی';
      $('btn-update').disabled = false;
    } else if (res.state === 'applying') {
      $('update-progress-text').textContent = 'در حال نصب و راه‌اندازی مجدد…';
      $('btn-update').disabled = true;
    }
    if (res.state === 'failed' && res.error) {
      showError($('update-error'), 'خطا: ' + res.error + ' — می‌توانید دوباره تلاش کنید.');
      $('btn-update').textContent = 'تلاش دوباره';
      $('btn-update').disabled = false;
    }
  }

  $('btn-update').addEventListener('click', function () {
    showError($('update-error'), '');
    api('/api/update/status').then(function (res) {
      if (res.state === 'downloaded') {
        // Apply: the app will exit and the updater restarts it.
        $('update-progress-text').textContent = 'در حال نصب و راه‌اندازی مجدد…';
        $('btn-update').disabled = true;
        return api('/api/update/apply', { method: 'POST' });
      }
      return api('/api/update/download', { method: 'POST' }).then(function (dl) {
        if (!dl.ok) { showError($('update-error'), dl.error || 'دانلود شروع نشد'); return; }
        show($('update-progress'), true);
        if (state.updatePoll) clearInterval(state.updatePoll);
        state.updatePoll = setInterval(function () {
          api('/api/update/status').then(function (s) {
            renderUpdateProgress(s);
            if (s.state === 'downloaded' || s.state === 'failed') clearInterval(state.updatePoll);
          }).catch(function () {});
        }, 500);
      });
    }).catch(function (e) { showError($('update-error'), String(e)); });
  });

  $('btn-update-dismiss').addEventListener('click', function () {
    updateDismissed = true;
    show($('update-banner'), false);
  });

  checkUpdate();
  setInterval(checkUpdate, 60000);
})();
