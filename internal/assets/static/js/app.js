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
      if (confirmDlg.returnValue === 'ok') { form.dataset.confirmed = '1'; form.submit(); }
    });
  });
  // Close dialogs on backdrop click.
  document.addEventListener('click', function (e) {
    if (e.target instanceof HTMLDialogElement && e.target.open) { var r = e.target.getBoundingClientRect(); if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) e.target.close(); }
  });
})();
