// Back/Forward history, log scroll, 390px meta label, failed counter, build error caption. Usage: node small.test.js page.html...
const pw = require('playwright');
const path = require('path');
const fs = require('fs');
// Screenshots for a human look; no check reads them.
const SHOTS = process.env.SHOTS || 'shots';

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
function all(list, out = []) { for (const n of list || []) { out.push(n); all(n.children, out); } return out; }
function find(list, id) { return all(list).find(n => n.id === id) || null; }
function defaultNode(list) { return all(list).find(n => n.state === 'FAILURE' && n.steps.length) || (list || [])[0]; }
function watch(page, errors, net) {
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', e => errors.push(String(e)));
  page.on('request', r => { if (!/^(file|data|blob):/.test(r.url())) net.push(r.url()); });
}
const state = page => page.evaluate(() => ({
  graph: (document.querySelector('.node[aria-pressed="true"]') || { dataset: {} }).dataset.id,
  list: (document.querySelector('.sl-sel[aria-pressed="true"]') || { dataset: {} }).dataset.id,
  step: (document.querySelector('.step-btn[aria-pressed="true"]') || { dataset: {} }).dataset.step,
  hash: decodeURIComponent(location.hash), len: history.length, logH: document.getElementById('log-h').textContent,
  pos: document.getElementById('fail-pos').textContent,
}));
const settle = page => page.waitForTimeout(150);
async function back(page) { await page.goBack({ waitUntil: 'commit' }).catch(() => {}); await settle(page); }
async function fwd(page) { await page.goForward({ waitUntil: 'commit' }).catch(() => {}); await settle(page); }
const logVisible = page => page.evaluate(() => {
  const r = document.getElementById('log').getBoundingClientRect();
  return Math.round(Math.min(r.bottom, innerHeight) - Math.max(r.top, 0));
});

async function wide(browserName, file) {
  const tag = `[${browserName} ${path.basename(file)}]`;
  const S = snapshot(file), leaves = failedLeaves(S.stages), nodes = all(S.stages);
  const want = find(S.stages, S.failureNodeId) || defaultNode(S.stages);
  const browser = await pw[browserName].launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  const errors = [], net = [];
  watch(page, errors, net);
  const url = 'file://' + path.resolve(file);
  await page.goto(url);
  await page.waitForSelector('.node', { state: 'attached' });

  let s = await state(page);
  const len0 = s.len;
  check(`${tag} initial open replaces, hash names ${want.name}`, s.graph === want.id && s.hash === '#node=' + want.id, s);

  // Graph mode: two node clicks, then Back twice and Forward once.
  const tops = S.stages.filter(n => n.id !== want.id);
  if (tops.length >= 2) {
    const [a, b] = tops.slice(-2);
    await page.click(`.node[data-id="${a.id}"]`);
    await page.click(`.node[data-id="${a.id}"]`);
    await page.click(`.node[data-id="${b.id}"]`);
    s = await state(page);
    check(`${tag} graph: clicks push one entry each, repeat click none`, s.graph === b.id && s.len === len0 + 2, s);
    await back(page);
    s = await state(page);
    check(`${tag} graph: Back reselects ${a.name}`, s.graph === a.id && s.list === a.id && s.hash === '#node=' + a.id, s);
    await back(page);
    s = await state(page);
    check(`${tag} graph: Back again returns to initial ${want.name}`, s.graph === want.id && s.hash === '#node=' + want.id, s);
    await fwd(page);
    s = await state(page);
    check(`${tag} graph: Forward reselects ${a.name}`, s.graph === a.id, s);
    await page.click(`.node[data-id="${want.id}"]`);
  }

  // Step selection: Back closes the step log.
  const withSteps = nodes.filter(n => n.steps.length).sort((x, y) => y.steps.length - x.steps.length)[0] || null;
  if (withSteps) {
    await page.evaluate(id => { location.hash = 'node=' + id; }, withSteps.id);
    await settle(page);
    const st = withSteps.steps[withSteps.steps.length - 1];
    await page.click(`.step-btn[data-step="${st.id}"]`);
    s = await state(page);
    const opened = s.step === st.id && s.logH.startsWith('Log: ');
    await back(page);
    s = await state(page);
    check(`${tag} step: Back undoes step selection, log cleared`, opened && s.graph === withSteps.id && !s.step && s.logH === 'Log', s);
    // Log already in view: no page scroll.
    await page.$eval('#log-panel', e => e.scrollIntoView({ block: 'start' }));
    const y0 = await page.evaluate(() => scrollY);
    await page.click(`.step-btn[data-step="${withSteps.steps[0].id}"]`);
    await settle(page);
    const y1 = await page.evaluate(() => scrollY);
    check(`${tag} step click with log in view does not scroll page`, y0 === y1, { y0, y1 });
    // Log off-screen at desktop width: a long uncapped step list pushes the clicked step far below it.
    await page.addStyleTag({ content: '#steps{max-height:none!important} #steps .steps li{padding-top:1200px!important}' });
    await page.evaluate(() => [...document.querySelectorAll('#steps .step-btn')].pop().scrollIntoView({ block: 'end' }));
    const pre = await logVisible(page);
    await page.click(`.step-btn[data-step="${st.id}"]`);
    await settle(page);
    const post = await logVisible(page);
    check(`${tag} 1280: off-screen log scrolled into view on step click`, pre < 48 && post >= 48, { pre, post });
    await page.reload();
    await page.waitForSelector('.node', { state: 'attached' });
  }

  // Failed counter.
  if (leaves.length) {
    await page.evaluate(() => { location.hash = ''; });
    await settle(page);
    await page.click('#fail-next');
    await page.click('#fail-next');
    s = await state(page);
    const two = s.pos === `${Math.min(2, leaves.length)} of ${leaves.length} failed`;
    const other = S.stages.find(n => !leaves.includes(n)) || nodes.find(n => !leaves.includes(n));
    if (other) {
      await page.evaluate(id => { location.hash = 'node=' + id; }, other.id);
      await settle(page);
      s = await state(page);
      check(`${tag} counter resets to "${leaves.length} failed" on non-failed ${other.name}`, two && s.pos === `${leaves.length} failed`, s);
      await page.click('#fail-next');
      s = await state(page);
      check(`${tag} next after reset goes to first failed leaf`, s.graph === leaves[0].id && s.pos === `1 of ${leaves.length} failed`, s);
    }
    const k = leaves.length - 1;
    await page.evaluate(id => { location.hash = 'node=' + id; }, leaves[k].id);
    await settle(page);
    s = await state(page);
    check(`${tag} hash to failed leaf shows its position`, s.pos === `${k + 1} of ${leaves.length} failed`, s);
    // First step after a selection pushes, later steps replace: a walk costs one entry and one Back returns to the selection.
    const A = other || nodes.find(n => n.id !== leaves[k].id);
    if (A) {
      await page.click(`.node[data-id="${A.id}"]`);
      s = await state(page);
      const len = s.len;
      for (let i = 0; i < 5; i++) await page.click('#fail-next');
      s = await state(page);
      check(`${tag} stepping 5 failures adds exactly one entry`, s.len === len + 1, { len, s });
      await back(page);
      s = await state(page);
      check(`${tag} one Back after failed nav returns to clicked ${A.name}`, s.graph === A.id && s.hash === '#node=' + A.id, s);
      await page.click('#fail-next');
      await page.click('#fail-next');
      await page.click('#fail-next');
      s = await state(page);
      const stepped = s.graph;
      await back(page);
      s = await state(page);
      check(`${tag} step after Back pushes again, Back returns to ${A.name}`, s.graph === A.id && stepped !== A.id, s);
    }
    if (leaves.length > 1 || !leaves.includes(want)) {
      const fresh = await ctx.newPage();
      await fresh.goto(url);
      await fresh.waitForSelector('.node', { state: 'attached' });
      const l0 = (await state(fresh)).len;
      await fresh.click('#fail-next');
      await fresh.click('#fail-next');
      s = await state(fresh);
      check(`${tag} stepping right after open adds one entry`, s.len === l0 + 1 && s.graph !== want.id, { l0, s });
      await back(fresh);
      s = await state(fresh);
      check(`${tag} Back after open-then-step returns to opening ${want.name}`, s.graph === want.id, s);
      await fresh.close();
    }
  }

  // List mode Back.
  if (await page.isVisible('#mode-list')) {
    await page.click('#mode-list');
    await page.evaluate(id => { location.hash = 'node=' + id; }, want.id);
    await settle(page);
    const rows = nodes.filter(n => n.id !== want.id).slice(-2);
    if (rows.length === 2) {
      const len1 = (await state(page)).len;
      await page.click(`.sl-sel[data-id="${rows[0].id}"]`);
      await page.click(`.sl-sel[data-id="${rows[1].id}"]`);
      s = await state(page);
      const pushed = s.list === rows[1].id;
      await back(page);
      s = await state(page);
      const r1 = s.list === rows[0].id && s.graph === rows[0].id;
      await back(page);
      s = await state(page);
      check(`${tag} list: Back undoes row selections`, pushed && r1 && s.list === want.id, { pushed, r1, s, rows: rows.map(r => r.id), want: want.id, len1 });
      const rowShown = await page.$eval(`.sl-sel[data-id="${want.id}"]`, b => b.getClientRects().length > 0);
      check(`${tag} list: row selected by Back is shown`, rowShown);
    }
    await page.click('#mode-graph');
  }

  // Caption on build-level message only.
  const cap = await page.evaluate(() => {
    const c = document.querySelector('header .fail-cap'), m = document.querySelector('header .fail-msg');
    return { cap: c && c.textContent, next: c && c.nextElementSibling === m, msg: m && m.textContent, msgKids: m ? m.children.length : -1, steps: !!document.querySelector('#steps .fail-cap') };
  });
  const inHeader = !!(S.failureMessage && !(S.failureMessageSource === 'node' && find(S.stages, S.failureNodeId)));
  check(`${tag} "Build error" caption ${inHeader ? 'before header message' : 'absent'}`, inHeader
    ? cap.cap === 'Build error' && cap.next && cap.msg === S.failureMessage && cap.msgKids === 0 && !cap.steps
    : cap.cap === null && !cap.steps, cap);
  if (inHeader) await page.screenshot({ path: `${SHOTS}/${browserName}-${path.basename(file, '.html')}-1280-caption.png`, clip: { x: 0, y: 0, width: 1280, height: 260 } });

  check(`${tag} no console errors`, errors.length === 0, errors);
  check(`${tag} no network requests`, net.length === 0, net);
  await page.evaluate(() => localStorage.clear());
  await browser.close();
}

async function narrow(browserName, file) {
  const tag = `[${browserName} ${path.basename(file)} 390]`;
  const S = snapshot(file), nodes = all(S.stages);
  const browser = await pw[browserName].launch();
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  const errors = [], net = [];
  watch(page, errors, net);
  await page.goto('file://' + path.resolve(file));
  await page.waitForSelector('.sl-sel');
  // The dt stretches to its dd's height, so count the text's line boxes instead.
  const dts = await page.$$eval('.meta dt', ds => ds.map(d => { const r = document.createRange(); r.selectNodeContents(d);
    return { t: d.textContent, lines: new Set([...r.getClientRects()].map(x => Math.round(x.top))).size }; }));
  check(`${tag} meta labels on one line`, dts.length > 0 && dts.every(d => d.lines === 1), dts);
  const hx = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
  check(`${tag} no page h-scroll`, hx <= 0, hx);
  await page.screenshot({ path: `${SHOTS}/${browserName}-${path.basename(file, '.html')}-390-meta.png`, clip: { x: 0, y: 0, width: 390, height: 300 } });

  // Stacked columns: a step click brings the log up.
  const n = nodes.find(x => x.steps.length);
  if (n) {
    await page.evaluate(id => { location.hash = 'node=' + id; }, n.id);
    await settle(page);
    await page.evaluate(() => window.scrollTo(0, 0));
    const st = n.steps[0];
    await page.$eval(`.step-btn[data-step="${st.id}"]`, b => b.scrollIntoView({ block: 'center' }));
    const pre = await logVisible(page);
    await page.click(`.step-btn[data-step="${st.id}"]`);
    await settle(page);
    const post = await logVisible(page);
    check(`${tag} step click shows log`, post >= 48, { pre, post });
    await back(page);
    const s = await state(page);
    check(`${tag} Back undoes step at 390`, !s.step && s.list === n.id, s);
  }
  check(`${tag} no console errors`, errors.length === 0, errors);
  check(`${tag} no network requests`, net.length === 0, net);
  await browser.close();
}

(async () => {
  fs.mkdirSync(SHOTS, { recursive: true });
  for (const b of ['chromium', 'firefox']) for (const f of process.argv.slice(2)) {
    try { await wide(b, f); } catch (e) { failures++; console.log(`FAIL [${b} ${f}] wide threw: ${e.stack}`); }
    try { await narrow(b, f); } catch (e) { failures++; console.log(`FAIL [${b} ${f}] narrow threw: ${e.stack}`); }
  }
  console.log(failures ? `${failures} FAILED` : 'ALL PASSED');
  process.exit(failures ? 1 : 0);
})();
