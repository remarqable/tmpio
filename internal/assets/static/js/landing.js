// Typewriter headline for the landing page. Types each headline, holds, fades
// the line out, then types the next one. Disabled when the visitor prefers
// reduced motion (the first line stays as rendered by the server).
(function () {
  var h1 = document.querySelector('.ld-type');
  if (!h1 || window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
  var lines;
  try { lines = JSON.parse(h1.getAttribute('data-headlines') || '[]'); } catch (e) { return; }
  if (!lines || lines.length < 2) return;
  var text = h1.querySelector('.ld-type-text');
  var i = 0;
  var TYPE = 55, HOLD = 2800, FADE = 450, GAP = 250;
  function type(line, pos, done) {
    text.textContent = line.slice(0, pos);
    if (pos >= line.length) return done();
    setTimeout(function () { type(line, pos + 1, done); }, TYPE + Math.random() * 40);
  }
  function next() {
    h1.classList.add('ld-fading');
    setTimeout(function () {
      i = (i + 1) % lines.length;
      text.textContent = '';
      h1.classList.remove('ld-fading');
      setTimeout(function () { type(lines[i], 0, function () { setTimeout(next, HOLD); }); }, GAP);
    }, FADE);
  }
  setTimeout(next, HOLD);
})();
