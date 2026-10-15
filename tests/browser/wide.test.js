// Graph at 1400px: cut-off stages get "N more" scroll buttons. Usage: node wide.test.js page.html...
const pw = require('playwright');
const path = require('path');
const fs = require('fs');
// Screenshots for a human look; no check reads them.
const SHOTS = process.env.SHOTS || 'shots';
let failures = 0;
function check(label, ok, detail) {
  console.log(`${ok ? 'PASS' : 'FAIL'} ${label}${detail !== undefined ? ' ' + JSON.stringify(detail).slice(0, 900) : ''}`);
  if (!ok) failures++;
}
const measure = page => page.evaluate(() => {
  const g = document.getElementById('graph-scroll'), gr = g.getBoundingClientRect();
  const lo = gr.left + g.clientLeft, hi = lo + g.clientWidth;
  const tops = [...document.querySelectorAll('#graph > .stage > .node')].map(n => { const r = n.getBoundingClientRect(); return { name: n.querySelector('.node-name').textContent, l: r.left, r: r.right }; });
  const btn = id => { const b = document.getElementById(id), r = b.getBoundingClientRect();
    return { shown: !b.hidden && r.width > 0, text: b.textContent, label: b.getAttribute('aria-label'), inPane: r.left >= gr.left && r.right <= gr.right && r.top >= gr.top && r.bottom <= gr.bottom,
      hit: !b.hidden && document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2) === b }; };
  return { before: tops.filter(t => t.l < lo - 1).length, after: tops.filter(t => t.l >= lo - 1 && t.r > hi + 1).map(t => t.name),
    lastIn: tops.length > 0 && tops[tops.length - 1].r <= hi + 1, atEnd: g.scrollLeft + g.clientWidth >= g.scrollWidth - 2, atStart: g.scrollLeft <= 2,
    left: btn('graph-left'), right: btn('graph-right') };
});
async function run(b, f, opts, suffix) {
  const browser = await pw[b].launch(opts);
  const page = await (await browser.newContext({ viewport: { width: 1400, height: 900 } })).newPage();
  const errors = [], net = [];
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', e => errors.push(String(e)));
  page.on('request', r => { if (!/^(file|data|blob):/.test(r.url())) net.push(r.url()); });
  await page.goto('file://' + path.resolve(f));
  await page.waitForSelector('.node', { state: 'attached' });
  const base = path.basename(f, '.html');
  const tag = `[${b}${suffix} ${base} 1400]`;
  let m = await measure(page);
  check(`${tag} right button iff more to the right, N = cut stages`, m.right.shown === !m.atEnd &&
    (!m.right.shown || (m.right.text === (m.after.length ? `${m.after.length} more →` : 'More →') && m.right.hit && m.right.inPane &&
      m.right.label === (m.after.length ? `${m.after.length} more stage${m.after.length > 1 ? 's' : ''}` : 'More stages') + ', scroll right' &&
      m.right.label.startsWith(m.right.text.replace(' →', '')))), m);
  check(`${tag} left button iff scrolled`, m.left.shown === !m.atStart && (!m.left.shown || (m.left.hit && m.left.text === (m.before ? `← ${m.before} more` : '← More'))), m.left);
  if (/^wide-/.test(base)) check(`${tag} wide page overflows the pane`, m.left.shown || m.right.shown, m);
  await page.screenshot({ path: `${SHOTS}/${b}${suffix}-${base}-1400-after.png` });
  if (m.right.shown) {
    // Keyboard: focus it, Enter scrolls.
    await page.focus('#graph-right');
    let guard = 0;
    while (!(await measure(page)).atEnd && guard++ < 40) { await page.keyboard.press('Enter'); await page.waitForTimeout(700); }
    m = await measure(page);
    const act = await page.evaluate(() => document.activeElement.id);
    check(`${tag} right button reaches the end, last stage shown, right button hides, focus moves to left`, m.atEnd && m.lastIn && !m.right.shown && m.left.shown && act === 'graph-left' &&
      m.left.label.startsWith(m.left.text.replace('← ', '')) && /, scroll left$/.test(m.left.label), { guard, act, m });
    guard = 0;
    while (!(await measure(page)).atStart && guard++ < 40) { await page.keyboard.press('Enter'); await page.waitForTimeout(700); }
    m = await measure(page);
    const act2 = await page.evaluate(() => document.activeElement.id);
    check(`${tag} Enter on left back to start, left hides, focus moves to right`, m.atStart && !m.left.shown && m.right.shown && act2 === 'graph-right', { guard, act2 });
    await page.keyboard.press('Enter');
    await page.waitForTimeout(700);
    await page.screenshot({ path: `${SHOTS}/${b}${suffix}-${base}-1400-end.png` });
    await page.click('#graph-left');
    await page.waitForTimeout(700);
    m = await measure(page);
    check(`${tag} left button scrolls back, right button returns`, m.right.shown, m);
    await page.click('#mode-list');
    await page.waitForTimeout(100);
    m = await measure(page);
    check(`${tag} no buttons in list mode`, !m.left.shown && !m.right.shown, m);
    await page.click('#mode-graph');
    // Singular wording: put the pane's right edge midway through the last top-level node.
    await page.evaluate(() => {
      const g = document.getElementById('graph-scroll'), lo = g.getBoundingClientRect().left + g.clientLeft;
      const ns = document.querySelectorAll('#graph > .stage > .node'), r = ns[ns.length - 1].getBoundingClientRect();
      g.scrollLeft = r.left - lo + g.scrollLeft - g.clientWidth + r.width / 2;
    });
    await page.waitForTimeout(100);
    m = await measure(page);
    const last = await page.$$eval('#graph > .stage > .node .node-name', ns => ns[ns.length - 1].textContent);
    check(`${tag} last stage half cut: "1 more →", singular label`, m.after.length === 1 && m.after[0] === last &&
      m.right.text === '1 more →' && m.right.label === '1 more stage, scroll right', { last, m });
    await page.setViewportSize({ width: 1900, height: 900 });
    await page.waitForTimeout(150);
    m = await measure(page);
    check(`${tag} resize keeps button in step`, m.right.shown === !m.atEnd, m);
    await page.evaluate(() => localStorage.clear());
  }
  check(`${tag} no console errors / network`, !errors.length && !net.length, { errors, net });
  await browser.close();
}
(async () => {
  fs.mkdirSync(SHOTS, { recursive: true });
  for (const f of process.argv.slice(2)) {
    await run('chromium', f, {}, '');
    await run('chromium', f, { ignoreDefaultArgs: ['--hide-scrollbars'] }, '-sb');
    await run('firefox', f, {}, '');
  }
  console.log(failures ? `${failures} FAILED` : 'ALL PASSED');
  process.exit(failures ? 1 : 0);
})();
