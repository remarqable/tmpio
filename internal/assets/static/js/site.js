// Site behaviour: appearance toggle persisted locally, sidebar toggle, and
// unsaved-text preservation for the shared editor. Reading works without JS.
(function () {
  var root = document.documentElement;
  var key = 'tmp-appearance';
  try { var saved = localStorage.getItem(key); if (saved === 'light' || saved === 'dark' || saved === 'system') root.setAttribute('data-appearance', saved); } catch (e) {}
  function bs() { var v = root.getAttribute('data-appearance'); var dark = v === 'dark' || (v === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches); root.setAttribute('data-bs-theme', dark ? 'dark' : 'light'); }
  bs();
  function mark() { document.querySelectorAll('.tmp-appearance button').forEach(function (b) { b.setAttribute('aria-pressed', b.getAttribute('data-appearance') === root.getAttribute('data-appearance') ? 'true' : 'false'); }); }
  mark();
  document.addEventListener('click', function (e) {
    var b = e.target.closest('.tmp-appearance button[data-appearance]');
    if (b) { var v = b.getAttribute('data-appearance'); root.setAttribute('data-appearance', v); try { localStorage.setItem(key, v); } catch (err) {} mark(); bs(); return; }
    var m = e.target.closest('[data-toggle-sidebar]');
    if (m) { var open = document.body.classList.toggle('tmp-sidebar-open'); m.setAttribute('aria-expanded', open ? 'true' : 'false'); }
  });
  document.addEventListener('keydown', function (e) { if (e.key === 'Escape' && document.body.classList.contains('tmp-sidebar-open')) { document.body.classList.remove('tmp-sidebar-open'); var m = document.querySelector('[data-toggle-sidebar]'); if (m) m.setAttribute('aria-expanded', 'false'); } });
  var editor = document.querySelector('[data-share-editor]');
  if (editor) {
    var ta = editor.querySelector('textarea[name=content]');
    var draftKey = 'tmp-draft:' + location.pathname;
    try { var d = localStorage.getItem(draftKey); if (d && ta && d !== ta.value && !editor.querySelector('.tmp-callout')) { /* keep server value; expose draft */ var n = document.createElement('div'); n.className = 'tmp-callout tmp-callout-note'; var body = document.createElement('div'); body.className = 'tmp-callout-body'; body.appendChild(document.createTextNode((editor.getAttribute('data-draft-text') || 'An unsaved draft from this browser exists.') + ' ')); var rb = document.createElement('button'); rb.type = 'button'; rb.className = 'tmp-btn tmp-btn-secondary'; rb.setAttribute('data-restore-draft', ''); rb.textContent = editor.getAttribute('data-draft-restore') || 'Restore draft'; body.appendChild(rb); n.appendChild(body); editor.parentNode.insertBefore(n, editor); n.querySelector('[data-restore-draft]').addEventListener('click', function () { ta.value = d; n.remove(); }); } } catch (e) {}
    if (ta) { ta.addEventListener('input', function () { try { localStorage.setItem(draftKey, ta.value); } catch (e) {} }); }
    editor.addEventListener('submit', function () { try { sessionStorage.setItem(draftKey + ':pending', ta.value); } catch (e) {} });
    if (document.querySelector('.tmp-callout-tip')) { try { localStorage.removeItem(draftKey); } catch (e) {} }
  }
})();
