// Owner UI behaviour: copy with toast, dialog-based confirmations, dialog openers. No inline handlers (CSP).
(function () {
  var toastEl = document.getElementById('tmp-toast');
  var toastTimer;
  function toast(text) {
    if (!toastEl) return;
    toastEl.textContent = text; toastEl.hidden = false; toastEl.classList.add('on');
    clearTimeout(toastTimer); toastTimer = setTimeout(function () { toastEl.classList.remove('on'); toastEl.hidden = true; }, 1800);
  }
  document.addEventListener('click', function (e) {
    var btn = e.target.closest('[data-copy-btn]');
    if (btn) {
      var text = btn.getAttribute('data-copy-btn');
      var label = btn.getAttribute('data-copied') || (document.body.getAttribute('data-copied') || 'Copied');
      var done = function () { toast(label); };
      if (navigator.clipboard && navigator.clipboard.writeText) { navigator.clipboard.writeText(text).then(done, done); }
      else { var ta = document.createElement('textarea'); ta.value = text; document.body.appendChild(ta); ta.select(); try { document.execCommand('copy'); } catch (err) {} document.body.removeChild(ta); done(); }
      return;
    }
    var opener = e.target.closest('[data-open-dialog]');
    if (opener) { var dlg = document.getElementById(opener.getAttribute('data-open-dialog')); if (dlg && dlg.showModal) { dlg.showModal(); var f = dlg.querySelector('input:not([type=hidden]),textarea'); if (f) { f.focus(); f.select && f.select(); } } return; }
    var closer = e.target.closest('[data-close-dialog]');
    if (closer) { var d = closer.closest('dialog'); if (d) d.close(); }
  });
  // Busy dialog for slow forms (data-busy="message"): shown once the form is
  // really going, and not dismissable, because the request carries on whether
  // or not the dialog is open and a second click would start it again.
  var busyDlg = document.getElementById('tmp-busy');
  var busy = false;
  function showBusy(form) {
    var msg = form.getAttribute('data-busy');
    if (!msg) return;
    busy = true;
    form.querySelectorAll('button').forEach(function (b) { b.disabled = true; });
    if (!busyDlg || !busyDlg.showModal) return;
    busyDlg.querySelector('[data-busy-text]').textContent = msg;
    if (!busyDlg.open) busyDlg.showModal();
  }
  if (busyDlg) {
    // Chrome lets Escape close a modal even when its cancel event is
    // cancelled, unless the page has been interacted with since, so stop the
    // key itself and reopen if anything closes the dialog anyway.
    busyDlg.addEventListener('cancel', function (e) { e.preventDefault(); });
    busyDlg.addEventListener('close', function () { if (busy) busyDlg.showModal(); });
    document.addEventListener('keydown', function (e) { if (busy && e.key === 'Escape') e.preventDefault(); }, true);
  }
  // Coming back with the back button restores the page as it was left, open
  // dialog and disabled buttons included.
  window.addEventListener('pageshow', function (e) {
    if (!e.persisted) return;
    busy = false;
    if (busyDlg && busyDlg.open) busyDlg.close();
    document.querySelectorAll('form[data-busy] button').forEach(function (b) { b.disabled = false; });
  });
  // Confirmation dialog for destructive forms.
  var confirmDlg = document.getElementById('tmp-confirm');
  document.addEventListener('submit', function (e) {
    var form = e.target;
    if (!form.hasAttribute('data-confirm') || form.dataset.confirmed === '1') return;
    e.preventDefault();
    if (!confirmDlg || !confirmDlg.showModal) { if (window.confirm(form.getAttribute('data-confirm'))) { form.dataset.confirmed = '1'; form.submit(); } return; }
    confirmDlg.querySelector('[data-confirm-text]').textContent = form.getAttribute('data-confirm');
    var submitBtn = form.querySelector('button:not([type=button])');
    var ok = confirmDlg.querySelector('[data-confirm-ok]');
    if (submitBtn && ok) { ok.innerHTML = submitBtn.innerHTML; }
    confirmDlg.returnValue = '';
    confirmDlg.showModal();
    confirmDlg.addEventListener('close', function onClose() {
      confirmDlg.removeEventListener('close', onClose);
      if (confirmDlg.returnValue === 'ok') { form.dataset.confirmed = '1'; showBusy(form); form.submit(); }
    });
  });
  // A form without a confirmation step goes straight to busy. This runs after
  // the confirmation listener, which cancels the first submit of a form that
  // still has to ask; form.submit() after the answer fires no event, so that
  // path calls showBusy itself.
  document.addEventListener('submit', function (e) {
    if (!e.defaultPrevented && e.target.hasAttribute('data-busy')) showBusy(e.target);
  });
  // Close dialogs on backdrop click, except the busy one.
  document.addEventListener('click', function (e) {
    if (e.target === busyDlg) return;
    if (e.target instanceof HTMLDialogElement && e.target.open) { var r = e.target.getBoundingClientRect(); if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) e.target.close(); }
  });
})();
