(function () {
  'use strict';
  var form = document.getElementById('login');
  var err = document.getElementById('error');
  var go = document.getElementById('go');
  form.addEventListener('submit', function (e) {
    e.preventDefault();
    err.hidden = true;
    go.disabled = true;
    fetch('/admin/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'lwjam' },
      body: JSON.stringify({ password: document.getElementById('password').value }),
    })
      .then(function (r) { return r.json().then(function (d) { return { ok: r.ok, d: d }; }); })
      .then(function (res) {
        if (res.ok) { window.location.replace('/admin'); return; }
        err.textContent = res.d.error || 'Sign in failed';
        err.hidden = false;
        go.disabled = false;
      })
      .catch(function () { err.textContent = 'Could not reach the server'; err.hidden = false; go.disabled = false; });
  });
})();
