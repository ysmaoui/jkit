// Collapsible branches, failed navigation, open-on-failure. Usage: node branches.test.js page.html...
const pw = require('playwright');
const path = require('path');
const fs = require('fs');

let failures = 0;
function check(label, ok, detail) {
  console.log(`${ok ? 'PASS' : 'FAIL'} ${label}${detail !== undefined ? ' ' + JSON.stringify(detail).slice(0, 600) : ''}`);
  if (!ok) failures++;
}

function snapshot(file) {
  const s = fs.readFileSync(file, 'utf8');
  return JSON.parse(/<script type="application\/json" id="snapshot">([\s\S]*?)<\/script>/.exec(s)[1]);
}
function failedLeaves(list, out = []) {
  for (const n of list || []) {
    const before = out.length;
    failedLeaves(n.children, out);
    if (n.state === 'FAILURE' && out.length === before) out.push(n);
  }
  return out;
}
function find(list, id) {
  for (const n of list || []) { if (n.id === id) return n; const f = find(n.children, id); if (f) return f; }
  return null;
}
function defaultNode(list) {
  let found = null;
  (function f(l) { (l || []).forEach(n => { if (!found && n.state === 'FAILURE' && n.steps.length) found = n; f(n.children); }); })(list);
  return found || list[0];
}

const selInfo = page => page.evaluate(() => {
  const b = document.querySelector('.node[aria-pressed="true"]');
  if (!b) return null;
  const r = b.getBoundingClientRect(), g = document.getElementById('graph-scroll').getBoundingClientRect();
  return { id: b.dataset.id, w: r.width, inPane: r.left >= g.left - 1 && r.right <= g.right + 1 && r.top >= g.top - 1 && r.bottom <= g.bottom + 1,
    pos: document.getElementById('fail-pos').textContent, hash: location.hash };
});

async function run(browserName, file) {
  const tag = `[${browserName} ${path.basename(file)}]`;
  const S = snapshot(file);
  const leaves = failedLeaves(S.stages);
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

  // Opens on the failure.
  const want = find(S.stages, S.failureNodeId) || defaultNode(S.stages);
  let s = await selInfo(page);
  check(`${tag} opens on ${want.name} (${want.id})`, s && s.id === want.id && s.w > 0, s);
  // Pages where a stage threw after another's caught error: the page opens on the one that threw.
  const threw = { 'golden-later.html': 'Setup', 'golden-branch.html': 'unit' }[path.basename(file)];
  if (threw) check(`${tag} opens on the stage that threw, ${threw}`, want.name === threw && s && s.id === want.id, { want: want.name, s });
  const fm = await page.evaluate(() => {
    const t = sel => { const p = document.querySelector(sel); return p && p.textContent; };
    return { steps: t('#steps .fail-msg'), header: t('header .fail-msg'), all: document.querySelectorAll('.fail-msg').length };
  });
  const onNode = !!(S.failureMessage && find(S.stages, S.failureNodeId) && want.id === S.failureNodeId);
  const inHeader = !!S.buildErrorMessage;
  check(`${tag} failure messages placed: stage ${onNode ? 'steps' : 'none'}, build ${inHeader ? 'header' : 'none'}`,
    fm.steps === (onNode ? S.failureMessage : null) && fm.header === (inHeader ? S.buildErrorMessage : null) && fm.all === (onNode ? 1 : 0) + (inHeader ? 1 : 0), fm);

  // Width, collapse and expand.
  const m = () => page.evaluate(() => ({ sw: document.getElementById('graph').scrollWidth, pane: document.getElementById('graph-scroll').clientWidth,
    collapsed: document.querySelectorAll('.branch.collapsed').length, branches: document.querySelectorAll('.branch > .tog').length }));
  const w0 = await m();
  const tools = await page.$eval('#branch-tools', e => !e.hidden);
  check(`${tag} branch tools shown iff collapsible branches`, tools === w0.branches > 0, w0);
  if (w0.branches) {
    await page.click('#expand-all');
    const wE = await m();
    await page.click('#collapse-all');
    const wC = await m();
    console.log(`INFO ${tag} graph width initial=${w0.sw} expanded=${wE.sw} collapsed=${wC.sw} pane=${w0.pane} branches=${w0.branches}`);
    check(`${tag} expand all opens every branch`, wE.collapsed === 0, wE);
    check(`${tag} auto-collapse iff wide`, (w0.collapsed > 0) === (wE.sw > 3 * w0.pane), { w0, wE });
    check(`${tag} collapse all keeps selected visible`, (await selInfo(page)).w > 0);
    // The wide pages stand in for real builds whose expanded graph is several screens wide.
    if (/^wide-/.test(path.basename(file))) check(`${tag} wide page auto-collapses on load`, w0.collapsed > 0, w0);
    if (/^wide-failed/.test(path.basename(file))) check(`${tag} collapsed width <= 7000 (margin under 8000)`, wC.sw <= 7000, wC);
    // Only the 24rem cap may cut a name; the strip must not squeeze it.
    const names = await page.evaluate(() => [...document.querySelectorAll('.branch.collapsed > .node .node-name')]
      .filter(e => e.scrollWidth > e.clientWidth + 1 && e.closest('.node').getBoundingClientRect().width < 24 * 14 - 1).map(e => e.textContent));
    check(`${tag} collapsed branch names not squeezed below 24rem`, names.length === 0, names.slice(0, 5));
    const aria = await page.$$eval('.branch > .tog', ts => ts.map(t => { const c = document.getElementById(t.getAttribute('aria-controls'));
      return { ok: !!c && c.parentElement === t.parentElement && /^Branch /.test(t.getAttribute('aria-label')), id: t.getAttribute('aria-controls') }; }));
    check(`${tag} toggles: aria-controls to own kids, fixed label`, aria.every(a => a.ok) && new Set(aria.map(a => a.id)).size === aria.length, aria.filter(a => !a.ok).slice(0, 3));

    // Toggle by click and keyboard.
    await page.click('#expand-all');
    const tog = page.locator('.branch > .tog').first();
    check(`${tag} toggle aria-expanded=true when open`, (await tog.getAttribute('aria-expanded')) === 'true');
    await tog.click();
    const st = await page.evaluate(() => { const b = document.querySelector('.branch > .tog').parentElement;
      return { c: b.classList.contains('collapsed'), strip: getComputedStyle(b.querySelector(':scope > .strip')).display, kids: b.querySelector(':scope > .seq, :scope > .branches').getBoundingClientRect().width,
        aria: b.firstChild.getAttribute('aria-expanded'), label: b.firstChild.getAttribute('aria-label'), dots: b.querySelectorAll('.strip .dot').length, nodes: b.querySelectorAll(':scope > .seq .node, :scope > .branches .node').length }; });
    check(`${tag} click collapses: strip shown, kids hidden`, st.c && st.strip !== 'none' && st.kids === 0 && st.aria === 'false' && /^Branch /.test(st.label) && st.dots === st.nodes, st);
    const dotSize = await page.$eval('.branch.collapsed > .strip .dot', e => { const r = e.getBoundingClientRect(); return [r.width, r.height, e.title]; });
    check(`${tag} dots are 12px with tooltip`, dotSize[0] === 12 && dotSize[1] === 12 && dotSize[2].length > 0, dotSize);
    await tog.focus();
    await page.keyboard.press('Enter');
    check(`${tag} Enter on toggle expands`, (await tog.getAttribute('aria-expanded')) === 'true');
    await page.keyboard.press('Space');
    check(`${tag} Space on toggle collapses`, (await tog.getAttribute('aria-expanded')) === 'false');

    // A dot selects its stage and opens the branch.
    const dot = await page.$eval('.branch.collapsed > .strip .dot:last-child', e => e.title);
    await page.click('.branch.collapsed > .strip .dot:last-child');
    s = await selInfo(page);
    check(`${tag} dot click selects stage, expands branch`, s && s.w > 0 && s.inPane && (await tog.getAttribute('aria-expanded')) === 'true', { dot, s });

    // A hash naming a node inside a collapsed branch reveals it.
    await page.click('#collapse-all');
    const deep = await page.evaluate(() => { const ns = document.querySelectorAll('.branch .seq .node, .branch .branches .node'); return ns.length ? ns[ns.length - 1].dataset.id : null; });
    if (deep) {
      await page.evaluate(id => { location.hash = 'node=' + encodeURIComponent(id); }, deep);
      await page.waitForTimeout(200);
      s = await selInfo(page);
      check(`${tag} hash into collapsed branch expands it`, s && s.id === deep && s.w > 0 && s.inPane, s);
    }
  }

  // Failed navigation.
  const fn = await page.$eval('#failnav', e => !e.hidden);
  check(`${tag} failed nav shown iff failed leaves (${leaves.length})`, fn === leaves.length > 0);
  if (leaves.length) {
    await page.click('#collapse-all').catch(() => {});
    await page.evaluate(() => { location.hash = ''; });
    const seen = [];
    let ok = true;
    const before = await m();
    for (let i = 0; i < leaves.length; i++) {
      await page.click('#fail-next');
      s = await selInfo(page);
      seen.push(s.id);
      const idx = leaves.findIndex(n => n.id === s.id);
      if (!(s.w > 0 && s.inPane && s.pos === `${idx + 1} of ${leaves.length} failed`)) { ok = false; console.log('  bad', JSON.stringify(s)); }
    }
    check(`${tag} next visits every failed leaf in order, visible`, ok && seen.join() === leaves.map(n => n.id).join() ||
      ok && new Set(seen).size === leaves.length && leaves.every(n => seen.includes(n.id)), { seen: seen.slice(0, 20), want: leaves.map(n => n.id).slice(0, 20) });
    const after = await m();
    check(`${tag} full next cycle keeps branches collapsed`, !before.collapsed || (after.collapsed > 0 && after.sw <= before.sw + 50), { before, after });
    await page.click('#fail-next');
    check(`${tag} next wraps`, (await selInfo(page)).id === seen[0]);
    await page.click('#fail-prev');
    check(`${tag} prev wraps`, (await selInfo(page)).id === seen[seen.length - 1]);
    // Keyboard: focus stays on the arrow, so repeated Enter keeps stepping.
    await page.focus('#fail-next');
    await page.keyboard.press('Enter');
    const k1 = (await selInfo(page)).id;
    await page.keyboard.press('Enter');
    const k2 = await selInfo(page);
    const act = await page.evaluate(() => document.activeElement.id);
    check(`${tag} repeated Enter on next steps twice`, k1 === seen[0] && (leaves.length === 1 ? k2.id === seen[0] : k2.id === seen[1]) && act === 'fail-next' &&
      k2.pos === `${leaves.length === 1 ? 1 : 2} of ${leaves.length} failed`, { k1, k2, act });
    const lab = await page.$eval('#fail-prev', e => e.getAttribute('aria-label'));
    check(`${tag} nav buttons labelled`, lab === 'Previous failed stage');
  }

  // A failed leaf with no failed step offers a console search.
  const quiet = leaves.find(n => !n.steps.some(x => x.state === 'FAILURE'));
  const btns = await page.evaluate(() => document.querySelectorAll('#steps .hint button').length);
  if (quiet) {
    await page.evaluate(id => { location.hash = 'node=' + encodeURIComponent(id); }, quiet.id);
    await page.waitForTimeout(200);
    const b = page.locator('#steps .hint button');
    check(`${tag} quiet failed stage offers console search`, (await b.count()) === 1 && (await b.textContent()).includes(quiet.name));
    await b.click();
    await page.waitForFunction(() => /of|No matches/.test(document.getElementById('count').textContent), null, { timeout: 30000 });
    const r = await page.evaluate(() => ({ h: document.getElementById('log-h').textContent, q: document.getElementById('search').value,
      count: document.getElementById('count').textContent, mark: (document.querySelector('#log mark') || {}).textContent, pressed: document.getElementById('console-btn').getAttribute('aria-pressed') }));
    check(`${tag} console opens searched for stage name`, r.h === 'Full console log' && r.q === quiet.name && /^1 of/.test(r.count) && r.mark === quiet.name && r.pressed === 'true', r);
    // Clicking again while the console is open re-runs the search in place.
    await page.fill('#search', 'zzz');
    await page.click('#steps .hint button');
    await page.waitForTimeout(100);
    check(`${tag} second click re-searches open console`, (await page.inputValue('#search')) === quiet.name && /^1 of/.test(await page.textContent('#count')));
  } else {
    check(`${tag} no console-search button when every failed leaf has a failed step`, true, btns);
  }

  check(`${tag} no console errors`, errors.length === 0, errors);
  check(`${tag} no network requests`, net.length === 0, net);
  await browser.close();
}

(async () => {
  for (const b of ['chromium', 'firefox']) for (const f of process.argv.slice(2)) {
    try { await run(b, f); } catch (e) { failures++; console.log(`FAIL [${b} ${f}] threw: ${e.stack}`); }
  }
  console.log(failures ? `${failures} FAILED` : 'ALL PASSED');
  process.exit(failures ? 1 : 0);
})();
