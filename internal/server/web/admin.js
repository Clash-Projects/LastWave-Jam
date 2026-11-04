(function () {
  'use strict';

  var state = { rooms: [], selected: null, chatKey: '' };

  function $(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  }
  function badge(text, cls) { return el('span', 'badge' + (cls ? ' ' + cls : ''), text); }
  function fmt(ms) {
    var s = Math.max(0, Math.floor((ms || 0) / 1000));
    return Math.floor(s / 60) + ':' + String(s % 60).padStart(2, '0');
  }
  function clock(ms) {
    return ms ? new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '';
  }

  var toastTimer;
  function toast(msg) {
    var t = $('toast');
    t.textContent = msg;
    t.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { t.hidden = true; }, 2400);
  }

  function api(path, method, body) {
    var init = { method: method || 'GET', credentials: 'same-origin', headers: { 'X-Requested-With': 'lwjam' } };
    if (body) { init.headers['Content-Type'] = 'application/json'; init.body = JSON.stringify(body); }
    return fetch(path, init).then(function (r) {
      if (r.status === 401) { window.location.replace('/admin/login'); throw new Error('auth'); }
      return r.json().then(function (d) {
        if (!r.ok) throw new Error(d.error || 'Request failed');
        return d;
      });
    });
  }

  function roomLabel(r) { return r.station ? r.name + ' station' : 'Jam ' + r.code; }

  /* ---------------- room list ---------------- */
  function renderList() {
    var box = $('room-list');
    box.textContent = '';
    var total = 0;
    state.rooms.forEach(function (r) {
      total += r.participants.length;
      var b = el('button', 'room' + (r.code === state.selected ? ' on' : ''));
      b.type = 'button';
      var row = el('div', 'room-row');
      row.appendChild(el('span', 'room-name', roomLabel(r)));
      row.appendChild(el('span', 'count', r.participants.length + ' in'));
      b.appendChild(row);
      var row2 = el('div', 'room-row');
      row2.appendChild(el('span', 'room-sub', r.nowPlaying ? '♪ ' + r.nowPlaying.title : 'Nothing playing'));
      if (r.station) row2.appendChild(badge(r.stationAuto ? 'Auto' : 'Admin', r.stationAuto ? 'live' : 'solid'));
      else row2.appendChild(badge('Private'));
      b.appendChild(row2);
      b.addEventListener('click', function () { select(r.code); });
      box.appendChild(b);
    });
    $('room-count').textContent = String(state.rooms.length);
    var tot = $('totals');
    tot.textContent = '';
    tot.appendChild(el('span', 'pulse'));
    tot.appendChild(document.createTextNode(state.rooms.length + ' Jams · ' + total + ' listening'));
  }

  /* ---------------- room detail ---------------- */
  function current() {
    for (var i = 0; i < state.rooms.length; i++) if (state.rooms[i].code === state.selected) return state.rooms[i];
    return null;
  }

  function act(path, body, okMsg) {
    api(path, 'POST', body)
      .then(function (view) {
        for (var i = 0; i < state.rooms.length; i++) if (state.rooms[i].code === view.code) state.rooms[i] = view;
        toast(okMsg);
        renderList(); renderDetail(); loadChat(true);
      })
      .catch(function (e) { if (e.message !== 'auth') toast(e.message); });
  }

  function renderDetail() {
    var r = current();
    $('empty').hidden = !!r;
    $('room').hidden = !r;
    if (!r) return;

    $('d-title').textContent = roomLabel(r);
    var meta = $('d-meta');
    meta.textContent = '';
    meta.appendChild(el('span', 'count', r.code));
    if (r.station) meta.appendChild(badge(r.stationAuto ? 'Auto radio' : 'Admin in control', r.stationAuto ? 'live' : 'solid'));
    else meta.appendChild(badge(r.mode === 'collaborative' ? 'Everyone controls' : 'Host controls'));
    meta.appendChild(document.createTextNode(r.participants.length + ' listening · started ' + clock(r.createdAt)));

    var hasAdmin = !!r.hostId;
    $('btn-auto').hidden = !(r.station && !r.stationAuto);
    $('btn-unhost').hidden = r.station || !hasAdmin;
    $('btn-unhost').textContent = 'Take host';

    var np = $('np');
    np.textContent = '';
    if (r.nowPlaying) {
      var t = r.nowPlaying;
      if (t.artwork_url) {
        var img = document.createElement('img');
        img.src = t.artwork_url.replace('/hqdefault.jpg', '/mqdefault.jpg');
        img.alt = '';
        np.appendChild(img);
      }
      var info = el('div');
      info.appendChild(el('div', 'np-title', t.title));
      info.appendChild(el('div', 'np-sub', (t.artist || '') + (t.duration_ms ? ' · ' + fmt(r.positionMs) + ' / ' + fmt(t.duration_ms) : '') + (r.isPlaying ? '' : ' · paused')));
      np.appendChild(info);
    } else {
      np.appendChild(el('div', 'np-sub', 'Nothing playing right now.'));
    }

    $('people-title').textContent = 'Listeners · ' + r.participants.length;
    var people = $('people');
    people.textContent = '';
    if (r.participants.length === 0) people.appendChild(el('div', 'np-sub', 'Nobody is listening.'));
    r.participants.forEach(function (p) {
      var row = el('div', 'person');
      row.appendChild(el('span', 'person-name', p.name));
      var right = el('div', 'person-right');
      if (p.isHost) right.appendChild(badge(r.station ? 'Admin' : 'Host', 'solid'));
      else if (p.isCoHost) right.appendChild(badge('Co-host'));
      if (p.isHost) {
        var take = el('button', 'btn ghost small', r.station ? 'Take admin' : 'Take host');
        take.type = 'button';
        take.addEventListener('click', function () {
          act('/admin/api/rooms/' + encodeURIComponent(r.code) + '/unhost', {},
            r.station ? 'Admin removed · station back on auto' : 'Host removed');
        });
        right.appendChild(take);
      } else {
        var give = el('button', 'btn small', r.station ? 'Make admin' : 'Make host');
        give.type = 'button';
        give.addEventListener('click', function () {
          act('/admin/api/rooms/' + encodeURIComponent(r.code) + '/host', { userId: p.id },
            p.name + (r.station ? ' is now the station admin' : ' is now the host'));
        });
        right.appendChild(give);
      }
      row.appendChild(right);
      people.appendChild(row);
    });
  }

  /* ---------------- chat ---------------- */
  function loadChat(force) {
    var r = current();
    if (!r) return;
    api('/admin/api/rooms/' + encodeURIComponent(r.code) + '/chat')
      .then(function (d) {
        var msgs = d.messages || [];
        var key = r.code + ':' + msgs.length + ':' + (msgs.length ? msgs[msgs.length - 1].id : '');
        if (!force && key === state.chatKey) return;
        state.chatKey = key;
        var box = $('chat');
        var atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
        box.textContent = '';
        if (msgs.length === 0) box.appendChild(el('div', 'np-sub', 'No messages yet.'));
        msgs.forEach(function (m) {
          var line = el('div', 'line');
          var head = el('div', 'line-head');
          head.appendChild(el('span', 'line-name' + (m.user_id === 'station' ? ' station' : ''), m.user_name + (m.is_host ? ' · host' : '')));
          head.appendChild(el('span', 'line-time', clock(m.timestamp)));
          line.appendChild(head);
          if (m.reply_to) line.appendChild(el('div', 'line-reply', '↪ ' + m.reply_to.user_name + ': ' + m.reply_to.text));
          line.appendChild(el('div', 'line-text', m.text));
          box.appendChild(line);
        });
        $('chat-count').textContent = '· ' + msgs.length;
        if (atBottom || force) box.scrollTop = box.scrollHeight;
      })
      .catch(function () {});
  }

  /* ---------------- loop ---------------- */
  function select(code) {
    state.selected = code;
    state.chatKey = '';
    renderList(); renderDetail(); loadChat(true);
  }

  function loadRooms() {
    return api('/admin/api/rooms').then(function (d) {
      state.rooms = d.rooms || [];
      if (state.selected && !current()) state.selected = null;
      if (!state.selected && state.rooms.length) state.selected = state.rooms[0].code;
      renderList(); renderDetail();
    }).catch(function (e) { if (e.message !== 'auth') toast(e.message); });
  }

  $('btn-auto').addEventListener('click', function () {
    var r = current();
    if (r) act('/admin/api/rooms/' + encodeURIComponent(r.code) + '/auto', {}, 'Station back on auto');
  });
  $('btn-unhost').addEventListener('click', function () {
    var r = current();
    if (r) act('/admin/api/rooms/' + encodeURIComponent(r.code) + '/unhost', {}, 'Host removed');
  });
  $('logout').addEventListener('click', function () {
    api('/admin/logout', 'POST', {}).catch(function () {}).then(function () { window.location.replace('/admin/login'); });
  });

  loadRooms().then(function () { loadChat(true); });
  setInterval(loadRooms, 3000);
  setInterval(function () { loadChat(false); }, 2000);
})();
