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

// Directory listing behaviour: column sorting and row selection. Both are
// enhancements — without JS the table still renders sorted by the server and
// the bulk bar simply never appears.
(function () {
  var form = document.querySelector('[data-drive]');
  if (!form) return;
  var table = form.querySelector('[data-drive-table]');
  var tbody = table ? table.querySelector('tbody') : null;
  if (!tbody) return;

  // --- sorting -------------------------------------------------------------
  var sortKey = 'name', sortAsc = true;
  function cell(tr, key) {
    if (key === 'name') return tr.getAttribute('data-name') || '';
    return parseInt(tr.getAttribute('data-' + key) || '0', 10);
  }
  function sort(key) {
    if (key === sortKey) sortAsc = !sortAsc; else { sortKey = key; sortAsc = true; }
    var rows = Array.prototype.slice.call(tbody.querySelectorAll('tr'));
    rows.sort(function (a, b) {
      // Folders lead, the way they do in every file browser worth copying.
      var ad = a.getAttribute('data-kind') === 'directory';
      var bd = b.getAttribute('data-kind') === 'directory';
      if (ad !== bd) return ad ? -1 : 1;
      var x = cell(a, sortKey), y = cell(b, sortKey), r;
      if (typeof x === 'string') r = x.localeCompare(y); else r = x - y;
      return sortAsc ? r : -r;
    });
    rows.forEach(function (tr) { tbody.appendChild(tr); });
    table.querySelectorAll('th[data-drive-sort]').forEach(function (th) {
      var on = th.getAttribute('data-drive-sort') === sortKey;
      th.classList.toggle('tmp-drive-sorted', on);
      th.setAttribute('aria-sort', on ? (sortAsc ? 'ascending' : 'descending') : 'none');
    });
  }
  table.querySelectorAll('th[data-drive-sort] button').forEach(function (b) {
    b.addEventListener('click', function () { sort(b.parentNode.getAttribute('data-drive-sort')); });
  });

  // --- selection -----------------------------------------------------------
  var bulk = form.querySelector('[data-drive-bulk]');
  if (!bulk) return;
  var countEl = form.querySelector('[data-drive-count]');
  var moveBtn = form.querySelector('[data-drive-move]');
  var all = form.querySelector('[data-drive-all]');
  var selOne = form.getAttribute('data-sel-one') || '1 item selected';
  var selMany = form.getAttribute('data-sel-many') || '{n} items selected';
  var actionEl = form.querySelector('[data-drive-action]');
  var delBtn = form.querySelector('[data-drive-delete]');

  function boxes() { return Array.prototype.slice.call(tbody.querySelectorAll('input[name=path]')); }
  function sync() {
    var bs = boxes(), n = 0, dirs = 0;
    bs.forEach(function (b) {
      var tr = b.closest('tr');
      if (b.checked) {
        n++;
        if (tr.getAttribute('data-kind') === 'directory') dirs++;
        tr.setAttribute('data-selected', '');
      } else {
        tr.removeAttribute('data-selected');
      }
    });
    bulk.hidden = n === 0;
    if (countEl) countEl.textContent = n === 1 ? selOne : selMany.replace('{n}', n);
    // Move only handles pages and assets, so a folder in the selection
    // disables it rather than failing halfway through the run.
    if (moveBtn) {
      moveBtn.disabled = dirs > 0;
      moveBtn.title = dirs > 0 ? (form.getAttribute('data-immovable') || '') : '';
    }
    if (all) {
      all.checked = n > 0 && n === bs.length;
      all.indeterminate = n > 0 && n < bs.length;
    }
  }
  tbody.addEventListener('change', function (e) { if (e.target.name === 'path') sync(); });
  if (all) all.addEventListener('change', function () {
    boxes().forEach(function (b) { b.checked = all.checked; });
    sync();
  });
  // The action travels in a hidden field: app.js re-submits the form after a
  // confirmation dialog, and a programmatic submit drops the submitter.
  if (moveBtn) moveBtn.addEventListener('click', function () {
    if (actionEl) actionEl.value = 'move';
    form.removeAttribute('data-confirm');
  });
  var refileBtn = form.querySelector('[data-drive-refile]');
  if (refileBtn) refileBtn.addEventListener('click', function () {
    if (actionEl) actionEl.value = 'refile';
    form.removeAttribute('data-confirm');
  });
  if (delBtn) delBtn.addEventListener('click', function () {
    if (actionEl) actionEl.value = 'delete';
    form.setAttribute('data-confirm', form.getAttribute('data-delete-confirm') || 'Delete the selected items?');
  });

  var clear = form.querySelector('[data-drive-clear]');
  if (clear) clear.addEventListener('click', function () {
    boxes().forEach(function (b) { b.checked = false; });
    if (all) all.checked = false;
    sync();
  });
  sync();
})();

// The sidebar tree: disclosure, remembered state and a filter. Without JS the
// server has already opened the branch containing the current page, so the
// tree is still usable — it just cannot be folded.
(function () {
  var nav = document.querySelector('[data-tree]');
  if (!nav) return;
  var KEY = 'tmp-tree-open';

  function open() {
    try { return JSON.parse(localStorage.getItem(KEY) || '[]'); } catch (e) { return []; }
  }
  function remember(list) {
    try { localStorage.setItem(KEY, JSON.stringify(list.slice(0, 200))); } catch (e) {}
  }
  function setShut(li, shut) {
    li.classList.toggle('tmp-tree-shut', shut);
    var b = li.querySelector(':scope > .tmp-tree-row > [data-tree-toggle]');
    if (b) b.setAttribute('aria-expanded', shut ? 'false' : 'true');
  }

  // Restore what was open last time, but never close the branch holding the
  // page being looked at — where you are wins over where you were.
  var saved = open();
  nav.querySelectorAll('li[data-tree-path]').forEach(function (li) {
    if (li.querySelector('[aria-current="page"]')) return;
    setShut(li, saved.indexOf(li.getAttribute('data-tree-path')) === -1);
  });

  nav.addEventListener('click', function (e) {
    var b = e.target.closest('[data-tree-toggle]');
    if (!b) return;
    e.preventDefault();
    var li = b.closest('li');
    var shut = !li.classList.contains('tmp-tree-shut');
    setShut(li, shut);
    var path = li.getAttribute('data-tree-path');
    var list = open().filter(function (p) { return p !== path; });
    if (!shut) list.push(path);
    remember(list);
  });

  function all(shut) {
    var list = [];
    nav.querySelectorAll('li[data-tree-path]').forEach(function (li) {
      setShut(li, shut);
      if (!shut) list.push(li.getAttribute('data-tree-path'));
    });
    remember(list);
  }
  var ex = document.querySelector('[data-tree-expand]');
  var co = document.querySelector('[data-tree-collapse]');
  if (ex) ex.addEventListener('click', function () { all(false); });
  if (co) co.addEventListener('click', function () { all(true); });

  // Filter: a row survives if it matches, or if something under it does.
  var filter = document.querySelector('[data-tree-filter]');
  if (!filter) return;
  var note = null;
  filter.addEventListener('input', function () {
    var q = filter.value.trim().toLowerCase();
    if (note) { note.remove(); note = null; }
    var items = nav.querySelectorAll('li');
    if (!q) {
      items.forEach(function (li) {
        li.classList.remove('tmp-tree-hidden');
        if (li.hasAttribute('data-tree-path')) {
          setShut(li, saved.indexOf(li.getAttribute('data-tree-path')) === -1 &&
                      !li.querySelector('[aria-current="page"]'));
        }
      });
      return;
    }
    var hits = 0;
    items.forEach(function (li) {
      var own = li.querySelector(':scope > .tmp-tree-row .tmp-tree-label');
      var self = own && own.textContent.toLowerCase().indexOf(q) !== -1;
      var kid = Array.prototype.some.call(li.querySelectorAll('.tmp-tree-label'), function (l) {
        return l.textContent.toLowerCase().indexOf(q) !== -1;
      });
      li.classList.toggle('tmp-tree-hidden', !(self || kid));
      if (self && !li.hasAttribute('data-tree-path')) hits++;
      if ((self || kid) && li.hasAttribute('data-tree-path')) setShut(li, false);
    });
    if (!hits) {
      note = document.createElement('p');
      note.className = 'tmp-tree-nohits';
      note.textContent = 'No pages match “' + filter.value.trim() + '”.';
      nav.appendChild(note);
    }
  });
})();
