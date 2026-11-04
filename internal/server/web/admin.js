(function () {
  'use strict';

  // selected: { kind: 'live', code } | { kind: 'ended', id } | null
  var state = { rooms: [], ended: [], selected: null, chatKey: '' };

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
  function isSel(kind, key) {
    return state.selected && state.selected.kind === kind && (state.selected.code === key || state.selected.id === key);
  }

  /* ---------------- lists ---------------- */
  function renderList() {
    var box = $('room-list');
    box.textContent = '';
    var total = 0;
    state.rooms.forEach(function (r) {
      total += r.participants.length;
      var b = el('button', 'room' + (isSel('live', r.code) ? ' on' : ''));
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
      b.addEventListener('click', function () { select({ kind: 'live', code: r.code }); });
      box.appendChild(b);
    });

    if (state.ended.length) {
      var head = el('div', 'rooms-head mt20');
      head.appendChild(el('span', 'tag', 'Ended · last 24 h'));
      head.appendChild(el('span', 'count', String(state.ended.length)));
      box.appendChild(head);
      state.ended.forEach(function (a) {
        var b = el('button', 'room' + (isSel('ended', a.id) ? ' on' : ''));
        b.type = 'button';
        var row = el('div', 'room-row');
        row.appendChild(el('span', 'room-name', roomLabel(a.room)));
        row.appendChild(el('span', 'count', a.chatCount + ' msgs'));
        b.appendChild(row);
        var row2 = el('div', 'room-row');
        row2.appendChild(el('span', 'room-sub', 'Ended ' + clock(a.endedAt) + ' · ' + (a.room.seen || []).length + ' people'));
        row2.appendChild(badge('Ended'));
        b.appendChild(row2);
        b.addEventListener('click', function () { select({ kind: 'ended', id: a.id }); });
        box.appendChild(b);
      });
    }

    $('room-count').textContent = String(state.rooms.length);
    var tot = $('totals');
    tot.textContent = '';
    tot.appendChild(el('span', 'pulse'));
    tot.appendChild(document.createTextNode(state.rooms.length + ' live · ' + total + ' listening · ' + state.ended.length + ' ended today'));
  }

  /* ---------------- detail ---------------- */
  function liveRoom() {
    if (!state.selected || state.selected.kind !== 'live') return null;
    for (var i = 0; i < state.rooms.length; i++) if (state.rooms[i].code === state.selected.code) return state.rooms[i];
    return null;
  }
  function endedRoom() {
    if (!state.selected || state.selected.kind !== 'ended') return null;
    for (var i = 0; i < state.ended.length; i++) if (state.ended[i].id === state.selected.id) return state.ended[i];
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

  function renderNowPlaying(r, label) {
    var np = $('np');
    np.textContent = '';
    $('np-title').textContent = label;
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
      info.appendChild(el('div', 'np-sub', t.artist || ''));
      np.appendChild(info);
      return info;
    }
    np.appendChild(el('div', 'np-sub', 'Nothing was playing.'));
    return null;
  }

  function renderDetail() {
    var live = liveRoom();
    var ended = endedRoom();
    $('empty').hidden = !!(live || ended);
    $('room').hidden = !(live || ended);
    $('btn-auto').hidden = true;
    $('btn-unhost').hidden = true;
    if (live) renderLive(live);
    else if (ended) renderEnded(ended);
  }

  function renderLive(r) {
    $('d-title').textContent = roomLabel(r);
    var meta = $('d-meta');
    meta.textContent = '';
    meta.appendChild(el('span', 'count', r.code));
    if (r.station) meta.appendChild(badge(r.stationAuto ? 'Auto radio' : 'Admin in control', r.stationAuto ? 'live' : 'solid'));
    else meta.appendChild(badge(r.mode === 'collaborative' ? 'Everyone controls' : 'Host controls'));
    meta.appendChild(document.createTextNode(r.participants.length + ' listening · started ' + clock(r.createdAt)));
    $('btn-auto').hidden = !(r.station && !r.stationAuto);

    var info = renderNowPlaying(r, 'Now playing');
    if (info && r.nowPlaying.duration_ms) {
      info.lastChild.textContent += ' · ' + fmt(r.positionMs) + ' / ' + fmt(r.nowPlaying.duration_ms) + (r.isPlaying ? '' : ' · paused');
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
      var base = '/admin/api/rooms/' + encodeURIComponent(r.code);
      if (p.isHost) {
        var take = el('button', 'btn ghost small', r.station ? 'Take admin' : 'Take host');
        take.type = 'button';
        take.addEventListener('click', function () {
          act(base + '/unhost', {}, r.station ? 'Admin removed · station back on auto' : 'Host removed');
        });
        right.appendChild(take);
      } else {
        var give = el('button', 'btn small', r.station ? 'Make admin' : 'Make host');
        give.type = 'button';
        give.addEventListener('click', function () {
          act(base + '/host', { userId: p.id }, p.name + (r.station ? ' is now the station admin' : ' is now the host'));
        });
        right.appendChild(give);
      }
      row.appendChild(right);
      people.appendChild(row);
    });
  }

  function renderEnded(a) {
    var r = a.room;
    $('d-title').textContent = roomLabel(r);
    var meta = $('d-meta');
    meta.textContent = '';
    meta.appendChild(el('span', 'count', r.code));
    meta.appendChild(badge('Ended'));
    meta.appendChild(document.createTextNode('started ' + clock(r.createdAt) + ' · ended ' + clock(a.endedAt) + ' · deleted 24 h after it ended'));
    renderNowPlaying(r, 'Last played');

    var seen = r.seen || [];
    $('people-title').textContent = 'People who joined · ' + seen.length;
    var people = $('people');
    people.textContent = '';
    if (seen.length === 0) people.appendChild(el('div', 'np-sub', 'Nobody joined.'));
    seen.forEach(function (name) {
      var row = el('div', 'person');
      row.appendChild(el('span', 'person-name', name));
      people.appendChild(row);
    });
  }

  /* ---------------- chat ---------------- */
  function chatPath() {
    if (!state.selected) return null;
    return state.selected.kind === 'live'
      ? '/admin/api/rooms/' + encodeURIComponent(state.selected.code) + '/chat'
      : '/admin/api/ended/' + encodeURIComponent(state.selected.id) + '/chat';
  }

  function loadChat(force) {
    var path = chatPath();
    if (!path) return;
    var selKey = path;
    api(path)
      .then(function (d) {
        if (chatPath() !== selKey) return; // selection changed meanwhile
        var msgs = d.messages || [];
        var key = selKey + ':' + msgs.length + ':' + (msgs.length ? msgs[msgs.length - 1].id : '');
        if (!force && key === state.chatKey) return;
        state.chatKey = key;
        var box = $('chat');
        var atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 40;
        box.textContent = '';
        if (msgs.length === 0) box.appendChild(el('div', 'np-sub', 'No messages in the last 24 hours.'));
        var lastDay = '';
        msgs.forEach(function (m) {
          var day = new Date(m.timestamp).toLocaleDateString([], { weekday: 'short', day: 'numeric', month: 'short' });
          if (day !== lastDay) { box.appendChild(el('div', 'tag', day)); lastDay = day; }
          var line = el('div', 'line');
          var head = el('div', 'line-head');
          head.appendChild(el('span', 'line-name' + (m.user_id === 'station' ? ' station' : ''), m.user_name + (m.is_host ? ' · host' : '')));
          head.appendChild(el('span', 'line-time', clock(m.timestamp)));
          line.appendChild(head);
          if (m.reply_to) line.appendChild(el('div', 'line-reply', '↪ ' + m.reply_to.user_name + ': ' + m.reply_to.text));
          line.appendChild(el('div', 'line-text', m.text));
          box.appendChild(line);
        });
        $('chat-count').textContent = '· ' + msgs.length + ' in 24 h';
        if (atBottom || force) box.scrollTop = box.scrollHeight;
      })
      .catch(function (e) { if (e.message !== 'auth') toast(e.message); });
  }

  /* ---------------- loop ---------------- */
  function select(sel) {
    state.selected = sel;
    state.chatKey = '';
    $('chat').textContent = '';
    renderList(); renderDetail(); loadChat(true);
  }

  function loadRooms() {
    return api('/admin/api/rooms').then(function (d) {
      state.rooms = d.rooms || [];
      state.ended = d.ended || [];
      if (state.selected && !liveRoom() && !endedRoom()) state.selected = null;
      if (!state.selected && state.rooms.length) state.selected = { kind: 'live', code: state.rooms[0].code };
      renderList(); renderDetail();
    }).catch(function (e) { if (e.message !== 'auth') toast(e.message); });
  }

  $('btn-auto').addEventListener('click', function () {
    var r = liveRoom();
    if (r) act('/admin/api/rooms/' + encodeURIComponent(r.code) + '/auto', {}, 'Station back on auto');
  });
  $('logout').addEventListener('click', function () {
    api('/admin/logout', 'POST', {}).catch(function () {}).then(function () { window.location.replace('/admin/login'); });
  });

  loadRooms().then(function () { loadChat(true); });
  setInterval(loadRooms, 3000);
  setInterval(function () { loadChat(false); }, 2500);
})();
