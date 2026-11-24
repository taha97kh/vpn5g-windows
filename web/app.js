// VPN 5G — web UI logic. Vanilla JS against the local JSON API.
(function () {
  'use strict';

  var $ = function (id) { return document.getElementById(id); };
  var state = { connected: false, serverId: null, servers: [], updatePoll: null, pendingConnectId: null };

  function api(path, opts) {
    return fetch(path, opts).then(function (r) { return r.json(); });
  }

  function show(el, on) { el.classList.toggle('hidden', !on); }
  function showError(el, msg) { el.textContent = msg; show(el, !!msg); }
  function showOk(el, msg) { el.textContent = msg; show(el, !!msg); }

  function fmtSpeed(bps) {
    if (bps == null || bps < 0) return '—';
    if (bps < 1024) return bps + ' B/s';
    if (bps < 1024 * 1024) return (bps / 1024).toFixed(1) + ' KB/s';
    return (bps / (1024 * 1024)).toFixed(2) + ' MB/s';
  }

  // ---- Tabs ----
  document.querySelectorAll('.tab').forEach(function (btn) {
    btn.addEventListener('click', function () {
      document.querySelectorAll('.tab').forEach(function (b) { b.classList.remove('active'); });
      document.querySelectorAll('.tab-page').forEach(function (p) { p.classList.remove('active'); });
      btn.classList.add('active');
      $('tab-' + btn.dataset.tab).classList.add('active');
      if (btn.dataset.tab === 'premium') { loadPlans(); loadSubscription(); }
      if (btn.dataset.tab === 'account') { refreshAuth(); }
    });
  });

  // ---- Status / countdown / speeds ----
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
      show($('speeds'), true);
      $('speed-down').textContent = st.speeds_available ? fmtSpeed(st.down_bps) : '—';
      $('speed-up').textContent = st.speeds_available ? fmtSpeed(st.up_bps) : '—';
      if (st.unlimited) {
        show($('countdown'), false);
        show($('unlimited-note'), true);
      } else {
        show($('countdown'), true);
        show($('unlimited-note'), false);
        $('countdown-time').textContent = fmtTime(st.remaining_seconds || 0);
      }
    } else {
      state.serverId = null;
      $('server-line').textContent = 'سروری انتخاب نشده — از تب سرورها یکی را انتخاب کنید';
      show($('countdown'), false);
      show($('unlimited-note'), false);
      show($('speeds'), false);
    }
    if (st.version) $('version').textContent = st.version;
    renderServers();
  }

  function refreshStatus() {
    api('/api/status').then(renderStatus).catch(function () {});
  }
  refreshStatus();
  setInterval(refreshStatus, 1000);

  // ---- Power button ----
  $('power').addEventListener('click', function () {
    showError($('home-error'), '');
    if (state.connected) {
      api('/api/disconnect', { method: 'POST' }).then(refreshStatus);
      return;
    }
    var first = state.servers.find(function (s) { return s.allowed; });
    if (!first) { showError($('home-error'), 'هنوز سروری آماده نیست — کمی صبر کنید و لیست را بروزرسانی کنید.'); return; }
    connectTo(first.id);
  });

  function connectTo(id) {
    showError($('home-error'), '');
    $('vip-note').classList.add('hidden');
    state.pendingConnectId = id;
    api('/api/connect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ server_id: id })
    }).then(handleConnectRes).catch(function (e) { showError($('home-error'), String(e)); });
  }

  function handleConnectRes(res) {
    if (res.ok) { state.pendingConnectId = null; refreshStatus(); return; }
    if (res.error === 'vip') {
      $('vip-note').classList.remove('hidden');
      document.querySelector('[data-tab="servers"]').click();
      return;
    }
    if (res.error === 'ad_required' && res.ad) {
      showInterstitial(res.ad, res.duration_seconds || 10);
      return;
    }
    showError($('home-error'), res.error || 'اتصال ناموفق بود');
  }

  // ---- Interstitial ad (countdown on performance.now — never the wall clock) ----
  var adTimer = null;
  function showInterstitial(ad, duration) {
    var total = Math.max(1, Math.min(20, duration || 10));
    $('ad-text').textContent = ad.text || '';
    if (ad.image_url) {
      $('ad-image').src = ad.image_url;
      show($('ad-image-wrap'), true);
    } else {
      show($('ad-image-wrap'), false);
    }
    if (ad.link_url) {
      show($('ad-open'), true);
      $('ad-open').onclick = function () {
        api('/api/ads/click', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ ad_id: ad.id })
        }).catch(function () {});
        window.open(ad.link_url, '_blank', 'noopener');
      };
    } else {
      show($('ad-open'), false);
    }
    show($('interstitial'), true);
    var start = performance.now();
    function tick() {
      var elapsed = (performance.now() - start) / 1000;
      var left = Math.max(0, Math.ceil(total - elapsed));
      $('ad-count').textContent = left;
      if (elapsed >= total) {
        api('/api/ads/viewed', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ ad_id: ad.id })
        }).then(function () {
          show($('interstitial'), false);
          var id = state.pendingConnectId;
          state.pendingConnectId = null;
          if (id) connectTo(id);
        }).catch(function () {
          show($('interstitial'), false);
        });
        return;
      }
      adTimer = setTimeout(tick, 200);
    }
    tick();
  }

  // ---- Servers ----
  function loadServers() {
    return api('/api/servers').then(function (res) {
      state.servers = res.servers || [];
      var n = state.servers.length;
      $('servers-count').textContent = n
        ? n + ' سرور آماده (' + state.servers.filter(function (s) { return s.allowed; }).length + ' در دسترس شما)'
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
        var badge = s.allowed
          ? (s.free ? '<span class="badge free">رایگان</span>' : '<span class="badge vip">ویژه</span>')
          : '<span class="badge locked">🔒 قفل</span>';
        html += '<div class="server' + (s.id === state.serverId ? ' active' : '') + '" data-id="' + s.id + '">' +
          '<div class="name">' + esc(s.name) + '</div>' +
          '<div class="meta"><span class="proto">' + esc(s.protocol) + '</span>' +
          '<span class="ping">' + s.ping_ms + 'ms</span>' + badge +
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

  // ---- Gift code (servers tab) ----
  $('btn-gift-open').addEventListener('click', function () {
    show($('gift-box'), true);
    $('gift-code-input').focus();
  });

  $('btn-gift-redeem').addEventListener('click', function () {
    var code = $('gift-code-input').value.trim();
    showError($('gift-error'), '');
    showOk($('gift-msg'), '');
    api('/api/redeem', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: code })
    }).then(function (res) {
      if (res.ok) {
        showOk($('gift-msg'), 'کد هدیه فعال شد ✨');
        loadServers();
      } else {
        showError($('gift-error'), res.error || 'فعال‌سازی ناموفق بود');
      }
    }).catch(function (e) { showError($('gift-error'), String(e)); });
  });

  // ---- Premium tab ----
  function loadSubscription() {
    api('/api/subscription').then(function (res) {
      var el = $('sub-status');
      if (!res.signed_in) {
        el.textContent = 'برای دیدن اشتراک، ابتدا وارد حساب شوید.';
        return;
      }
      if (res.subscriber) {
        el.textContent = 'اشتراک ویژه فعال است ✨ — ' + (res.remaining_days || 0) + ' روز مانده';
      } else if (res.gift) {
        el.textContent = 'کد هدیه فعال است 🎁 — ' + (res.remaining_days || 0) + ' روز مانده';
      } else {
        el.textContent = 'اشتراک فعالی ندارید — از بخش طرح‌ها اشتراک تهیه کنید.';
      }
    }).catch(function () {});
  }

  function loadPlans() {
    api('/api/plans').then(function (res) {
      var plans = res.plans || [];
      if (!plans.length) {
        $('plans-list').innerHTML = '<span class="hint">هنوز طرحی منتشر نشده است.</span>';
        return;
      }
      var html = '';
      plans.forEach(function (p) {
        var price = p.price_toman ? Number(p.price_toman).toLocaleString('fa-IR') + ' تومان' : '—';
        html += '<div class="plan"><div><b>' + esc(p.name_fa || p.id) + '</b>' +
          '<span class="plan-days">' + (p.days || '') + ' روز</span></div>' +
          '<div class="plan-buy"><span>' + price + '</span>' +
          '<a class="btn primary small" href="' + esc(p.buy_url) + '" target="_blank" rel="noopener">خرید</a></div></div>';
      });
      $('plans-list').innerHTML = html;
    }).catch(function () {});
  }

  $('btn-redeem').addEventListener('click', function () {
    var code = $('redeem-code-input').value.trim();
    showError($('redeem-error'), '');
    showOk($('redeem-msg'), '');
    api('/api/redeem', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code: code })
    }).then(function (res) {
      if (res.ok) {
        showOk($('redeem-msg'), 'اشتراک فعال شد ✨ — ' + (res.remaining_days || 0) + ' روز');
        loadSubscription();
        loadServers();
      } else {
        showError($('redeem-error'), res.error || 'فعال‌سازی ناموفق بود');
      }
    }).catch(function (e) { showError($('redeem-error'), String(e)); });
  });

  $('btn-buy-bot').addEventListener('click', function () {
    window.open('https://t.me/VPN5G_IRBot', '_blank', 'noopener');
  });

  // ---- Auth (password only) ----
  function refreshAuth() {
    api('/api/auth/status').then(function (res) {
      show($('auth-signed-in'), !!res.signed_in);
      show($('auth-form'), !res.signed_in);
      if (res.signed_in) {
        $('auth-email').textContent = res.email || '';
        api('/api/subscription').then(function (s) {
          $('auth-sub').textContent = s.subscriber
            ? 'اشتراک ویژه فعال — ' + (s.remaining_days || 0) + ' روز مانده'
            : (s.gift ? 'کد هدیه فعال — ' + (s.remaining_days || 0) + ' روز مانده' : 'اشتراک رایگان');
        }).catch(function () {});
      }
    }).catch(function () {});
  }
  refreshAuth();

  function authCall(path) {
    var email = $('auth-email-input').value.trim();
    var password = $('auth-password-input').value;
    showError($('auth-error'), '');
    showOk($('auth-ok'), '');
    api(path, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: email, password: password })
    }).then(function (res) {
      if (res.ok) { refreshAuth(); loadSubscription(); }
      else { showError($('auth-error'), res.error || 'ناموفق بود'); }
    }).catch(function (e) { showError($('auth-error'), String(e)); });
  }

  $('btn-login').addEventListener('click', function () { authCall('/api/auth/login'); });
  $('btn-signup').addEventListener('click', function () { authCall('/api/auth/signup'); });

  $('btn-forgot').addEventListener('click', function () {
    var email = $('auth-email-input').value.trim();
    showError($('auth-error'), '');
    showOk($('auth-ok'), '');
    api('/api/auth/recover', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: email })
    }).then(function (res) {
      if (res.ok) { showOk($('auth-ok'), res.note || 'لینک بازنشانی ارسال شد.'); }
      else { showError($('auth-error'), res.error || 'ارسال ناموفق بود'); }
    }).catch(function (e) { showError($('auth-error'), String(e)); });
  });

  $('btn-logout').addEventListener('click', function () {
    api('/api/auth/logout', { method: 'POST' }).then(function () { refreshAuth(); loadSubscription(); });
  });

  // ---- Announcement ----
  var annDismissed = null;
  function loadAnnouncement() {
    api('/api/announcement').then(function (res) {
      var ann = res.announcement;
      if (!ann || !ann.text || annDismissed === ann.id) { show($('announcement'), false); return; }
      $('announcement-text').textContent = ann.text;
      var el = $('announcement');
      el.onclick = function (ev) {
        if (ev.target.id === 'announcement-close') return;
        if (ann.link_url) window.open(ann.link_url, '_blank', 'noopener');
      };
      show(el, true);
    }).catch(function () {});
  }
  $('announcement-close').addEventListener('click', function () {
    api('/api/announcement').then(function (res) {
      if (res.announcement) annDismissed = res.announcement.id;
    }).catch(function () {});
    show($('announcement'), false);
  });
  loadAnnouncement();
  setInterval(loadAnnouncement, 60000);

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
