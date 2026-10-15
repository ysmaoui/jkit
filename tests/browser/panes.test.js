// Resizable panes check for jkit export html. Usage: node panes.test.js page.html...
const pw = require('playwright');
const path = require('path');

let failures = 0;
function check(label, ok, detail) {
  console.log(`${ok ? 'PASS' : 'FAIL'} ${label}${detail !== undefined ? ' ' + JSON.stringify(detail) : ''}`);
  if (!ok) failures++;
}
const near = (a, b, tol = 2) => Math.abs(a - b) <= tol;

async function h(page, sel) { return page.$eval(sel, e => e.getBoundingClientRect().height); }
async function w(page, sel) { return page.$eval(sel, e => e.getBoundingClientRect().width); }

async function drag(page, sel, dx, dy) {
  await page.$eval(sel, e => e.scrollIntoView({ block: 'nearest' }));
  const b = await page.$eval(sel, e => { const r = e.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 }; });
  await page.mouse.move(b.x, b.y);
  await page.mouse.down();
  for (let i = 1; i <= 5; i++) await page.mouse.move(b.x + dx * i / 5, b.y + dy * i / 5);
  await page.mouse.up();
}

async function run(browserName, file) {
  const tag = `[${browserName} ${path.basename(file)}]`;
  const browser = await pw[browserName].launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  const errors = [], net = [];
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', e => errors.push(String(e)));
  page.on('request', r => { if (!/^(file|data|blob):/.test(r.url())) net.push(r.url()); });
  const url = 'file://' + path.resolve(file);
  await page.goto(url);
  await page.waitForSelector('.node');

  const g0 = await h(page, '#graph-scroll');
  check(`${tag} default graph height <= 18rem`, g0 <= 288.5, g0);
  const aria = await page.$eval('#graph-split', e => ['role', 'tabindex', 'aria-orientation', 'aria-valuenow', 'aria-valuemin', 'aria-valuemax'].map(a => e.getAttribute(a)));
  check(`${tag} graph separator aria`, aria[0] === 'separator' && aria[1] === '0' && aria[2] === 'horizontal' && +aria[3] > 0 && aria[4] === '80' && aria[5] === '780', aria);

  await drag(page, '#graph-split', 0, 150);
  const g1 = await h(page, '#graph-scroll');
  check(`${tag} drag down grows graph by 150`, near(g1, g0 + 150), { g0, g1 });
  check(`${tag} aria-valuenow follows drag`, near(+(await page.getAttribute('#graph-split', 'aria-valuenow')), g1, 1));

  await page.focus('#graph-split');
  await page.keyboard.press('ArrowDown');
  check(`${tag} ArrowDown +32`, near(await h(page, '#graph-scroll'), g1 + 32));
  await page.keyboard.press('ArrowUp');
  await page.keyboard.press('ArrowUp');
  check(`${tag} ArrowUp -32`, near(await h(page, '#graph-scroll'), g1 - 32));
  await page.keyboard.press('Home');
  check(`${tag} Home clamps to 80`, near(await h(page, '#graph-scroll'), 80, 0.5));
  await page.keyboard.press('End');
  check(`${tag} End clamps to viewport-120`, near(await h(page, '#graph-scroll'), 780, 0.5));
  await page.$eval('#graph-split', e => e.scrollIntoView({ block: 'start' }));
  { const y = await page.$eval('#graph-split', e => e.getBoundingClientRect().top + 7); await drag(page, '#graph-split', 0, 898 - y); }
  check(`${tag} drag clamps at max`, near(await h(page, '#graph-scroll'), 780, 0.5));
  // Stays inside the viewport: Playwright's Firefox drops moves outside it.
  await page.focus('#graph-split');
  await page.keyboard.press('Home');
  for (let i = 0; i < 4; i++) await page.keyboard.press('ArrowDown');
  await page.$eval('#graph-split', e => e.scrollIntoView({ block: 'end' }));
  { const y = await page.$eval('#graph-split', e => e.getBoundingClientRect().top + 7); await drag(page, '#graph-split', 0, 2 - y); }
  { const v = await h(page, '#graph-scroll'); check(`${tag} drag clamps at min`, near(v, 80, 0.5), v); }
  await drag(page, '#graph-split', 0, 220);
  const gSaved = await h(page, '#graph-scroll');

  const s0 = await w(page, '#steps-panel');
  const paneW = await w(page, '.panels');
  check(`${tag} default steps column ~40%`, near(s0, (paneW - 14) * 0.4, 3) || near(s0, paneW * 0.4, 3), { s0, paneW });
  await drag(page, '#panels-split', -100, 0);
  const s1 = await w(page, '#steps-panel');
  check(`${tag} drag left shrinks steps by 100`, near(s1, s0 - 100), { s0, s1 });
  await page.focus('#panels-split');
  await page.keyboard.press('ArrowRight');
  const s2 = await w(page, '#steps-panel');
  check(`${tag} ArrowRight +32`, near(s2, s1 + 32), { s1, s2 });
  const logW = await w(page, '#log-panel');
  check(`${tag} columns fill row`, near(s2 + 14 + logW, paneW), { s2, logW, paneW });

  const stored = await page.evaluate(() => localStorage.getItem('jkit.export.layout'));
  await page.reload();
  await page.waitForSelector('.node');
  const gR = await h(page, '#graph-scroll'), sR = await w(page, '#steps-panel');
  check(`${tag} graph height persists across reload`, near(gR, gSaved, 1), { gSaved, gR, stored });
  check(`${tag} steps width persists across reload`, near(sR, s2, 1), { s2, sR });

  // A node below the visible part of a short pane must be scrolled into view.
  await page.focus('#graph-split');
  await page.keyboard.press('Home');
  const lastId = await page.$$eval('.node', ns => ns.reduce((a, n) => n.getBoundingClientRect().bottom > a.getBoundingClientRect().bottom ? n : a).dataset.id);
  await page.evaluate(id => { location.hash = 'node=' + encodeURIComponent(id); }, lastId);
  await page.waitForTimeout(200);
  const vis = await page.evaluate(id => {
    const b = document.querySelector('.node[data-id="' + CSS.escape(id) + '"]'), g = document.getElementById('graph-scroll');
    const r = b.getBoundingClientRect(), q = g.getBoundingClientRect();
    return { pressed: b.getAttribute('aria-pressed'), top: r.top - q.top, bottom: r.bottom - q.top, h: g.clientHeight, sh: g.scrollHeight, st: g.scrollTop, left: r.left - q.left, right: r.right - q.left, cw: g.clientWidth };
  }, lastId);
  check(`${tag} revealNode scrolls vertically`, vis.pressed === 'true' && vis.top >= 0 && vis.bottom <= vis.h + 2 && vis.left >= 0 && vis.right <= vis.cw + 2 && (vis.sh <= vis.h + 30 || vis.st > 0), vis);

  await page.dblclick('#graph-split');
  check(`${tag} double-click resets graph`, near(await h(page, '#graph-scroll'), g0, 1));
  await page.dblclick('#panels-split');
  check(`${tag} double-click resets columns`, near(await w(page, '#steps-panel'), s0, 1));
  await page.reload();
  await page.waitForSelector('.node');
  check(`${tag} reset persists across reload`, near(await h(page, '#graph-scroll'), g0, 1) && near(await w(page, '#steps-panel'), s0, 1));

  const stepsStyle = await page.$eval('#steps', e => { const c = getComputedStyle(e); return [c.overflowY, c.maxHeight]; });
  check(`${tag} #steps scrolls, capped at 75vh`, stepsStyle[0] === 'auto' && near(parseFloat(stepsStyle[1]), 675, 1), stepsStyle);

  await page.setViewportSize({ width: 600, height: 800 });
  const narrow = await page.evaluate(() => ({
    sepShown: getComputedStyle(document.getElementById('panels-split')).display !== 'none',
    pageScrollX: document.documentElement.scrollWidth > window.innerWidth,
  }));
  check(`${tag} narrow: column separator hidden, no page h-scroll`, !narrow.sepShown && !narrow.pageScrollX, narrow);

  // Storage that throws must not break the page.
  const p2 = await ctx.newPage();
  const errs2 = [];
  p2.on('pageerror', e => errs2.push(String(e)));
  p2.on('console', m => { if (m.type() === 'error') errs2.push(m.text()); });
  await p2.addInitScript(() => {
    const boom = () => { throw new Error('denied'); };
    Object.defineProperty(window, 'localStorage', { get: boom });
  });
  await p2.goto(url);
  await p2.waitForSelector('.node');
  await p2.focus('#graph-split');
  await p2.keyboard.press('ArrowDown');
  check(`${tag} throwing localStorage: page works, no errors`, errs2.length === 0 && (await p2.$$('.node')).length > 0, errs2);

  check(`${tag} no console errors`, errors.length === 0, errors);
  check(`${tag} no network requests`, net.length === 0, net);
  await browser.close();
}

async function review(browserName, file) {
  const tag = `[${browserName} ${path.basename(file)}]`;
  const browser = await pw[browserName].launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 800 } });
  const page = await ctx.newPage();
  const errors = [];
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', e => errors.push(String(e)));
  const url = 'file://' + path.resolve(file);
  await page.goto(url);
  await page.waitForSelector('.node');
  const st = () => page.evaluate(() => {
    const a = (id, n) => +document.getElementById(id).getAttribute(n);
    const r = id => document.getElementById(id).getBoundingClientRect();
    return { gh: r('graph-scroll').height, sw: r('steps-panel').width, lw: r('log-panel').width,
      gnow: a('graph-split', 'aria-valuenow'), gmax: a('graph-split', 'aria-valuemax'),
      pnow: a('panels-split', 'aria-valuenow'), pmax: a('panels-split', 'aria-valuemax'),
      hscroll: document.documentElement.scrollWidth - document.documentElement.clientWidth };
  });
  const load = async val => {
    await page.evaluate(v => localStorage.setItem('jkit.export.layout', v), val);
    await page.reload();
    await page.waitForSelector('.node');
    return st();
  };

  // Stored values pass through the same clamp as drags.
  const defaults = await st();
  for (const val of ['{"stepsPct":1e12,"graphH":1e12}', '{"stepsPct":0.5,"graphH":5}', 'not json', '"str"', '{"graphH":"abc","stepsPct":-5}']) {
    const s = await load(val);
    check(`${tag} stored ${val} clamped`, s.sw >= 79.5 && s.lw >= 100 && s.hscroll === 0 && s.gh >= 79.5 && s.gh <= 680.5 &&
      s.pnow <= s.pmax && s.gnow <= s.gmax, s);
  }
  for (const val of ['not json', '"str"', '{"graphH":"abc","stepsPct":-5}']) {
    const s = await load(val);
    check(`${tag} stored ${val} gives defaults`, near(s.gh, defaults.gh, 1) && near(s.sw, defaults.sw, 1), s);
  }
  await load('{}');

  // Window resize re-clamps and keeps aria values current.
  await page.focus('#graph-split');
  await page.keyboard.press('End');
  check(`${tag} graph End at 800h`, near((await st()).gh, 680, 0.5));
  await page.setViewportSize({ width: 800, height: 500 });
  await page.waitForTimeout(150);
  let s = await st();
  check(`${tag} resize to 500h re-clamps graph`, near(s.gh, 380, 0.5) && s.gmax === 380 && near(s.gnow, 380, 1), s);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.waitForTimeout(150);
  s = await st();
  check(`${tag} resize back restores chosen graph height`, near(s.gh, 680, 0.5) && s.gmax === 680 && near(s.gnow, 680, 1), s);
  await page.focus('#panels-split');
  await page.keyboard.press('End');
  await page.setViewportSize({ width: 920, height: 800 });
  await page.waitForTimeout(150);
  s = await st();
  check(`${tag} resize to 920w keeps log column`, s.lw >= 105 && s.hscroll === 0 && near(s.pnow, s.sw, 1) && s.pnow <= s.pmax, s);
  await page.setViewportSize({ width: 600, height: 800 });
  await page.waitForTimeout(150);
  await page.setViewportSize({ width: 1280, height: 800 });
  await page.waitForTimeout(150);
  s = await st();
  check(`${tag} across breakpoint and back: aria in range`, s.pnow <= s.pmax && near(s.pnow, s.sw, 1) && s.lw >= 105, s);

  // A mouse press focuses the separator so arrow keys work after a drag.
  await page.focus('#graph-split');
  await page.keyboard.press('Home');
  await page.focus('#console-btn');
  const c = await page.$eval('#graph-split', e => { e.scrollIntoView({ block: 'nearest' }); const r = e.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 }; });
  await page.mouse.move(c.x, c.y);
  await page.mouse.down();
  await page.mouse.move(c.x, c.y + 100, { steps: 4 });
  await page.mouse.up();
  const active = await page.evaluate(() => document.activeElement.id);
  const before = (await st()).gh;
  await page.keyboard.press('ArrowUp');
  const after = (await st()).gh;
  check(`${tag} drag focuses separator, then arrow works`, active === 'graph-split' && near(after, before - 32), { active, before, after });

  check(`${tag} review: no console errors`, errors.length === 0, errors);
  await browser.close();
}

(async () => {
  for (const b of ['chromium', 'firefox']) for (const f of process.argv.slice(2)) {
    try { await run(b, f); await review(b, f); } catch (e) { failures++; console.log(`FAIL [${b} ${f}] threw: ${e.stack}`); }
  }
  console.log(failures ? `${failures} FAILED` : 'ALL PASSED');
  process.exit(failures ? 1 : 0);
})();
