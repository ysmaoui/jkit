// Stage list view: toggle, filter, Failed only, selection, keyboard, persistence, 390px. Usage: node list.test.js page.html...
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
function index(list, parent = null, m = new Map()) {
  for (const n of list || []) { m.set(n.id, { n, parent }); index(n.children, n, m); }
  return m;
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
// Model of the page filter: matches plus ancestors, plus the selected row's path.
// Under Failed only, a failed leaf matches the text through its own or an ancestor's name.
function expected(idx, q, failedOnly, leafIds, selId) {
  const keep = new Set();
  let matches = 0;
  q = (q || '').trim().toLowerCase();
  const path = id => { const out = [id]; for (let p = idx.get(id).parent; p; p = idx.get(p.id).parent) out.push(p.id); return out; };
  for (const [id, e] of idx) {
    if (failedOnly && !leafIds.has(id)) continue;
    const names = (failedOnly ? path(id) : [id]).map(x => idx.get(x).n.name.toLowerCase());
    if (q && !names.some(n => n.includes(q))) continue;
    matches++;
    path(id).forEach(x => keep.add(x));
  }
  if (selId && idx.has(selId)) path(selId).forEach(x => keep.add(x));
  keep.matches = matches;
  return keep;
}
const curSel = page => page.evaluate(() => (document.querySelector('.sl-sel[aria-pressed="true"]') || { dataset: {} }).dataset.id);
const collapsedIds = page => page.$$eval('.sl-item.collapsed > .sl-row > .sl-sel', bs => bs.map(b => b.dataset.id).sort().join());

const visibleRows = page => page.$$eval('.sl-sel', bs => bs.filter(b => b.getClientRects().length).map(b => b.dataset.id));
const sameSet = (a, b) => a.length === b.size && a.every(x => b.has(x));
const selRow = page => page.evaluate(() => {
  const b = document.querySelector('.sl-sel[aria-pressed="true"]');
  if (!b) return null;
  const box = document.getElementById('graph-scroll'), tools = document.getElementById('sl-tools');
  const r = b.getBoundingClientRect(), g = box.getBoundingClientRect(), t = tools.getBoundingClientRect();
  return { id: b.dataset.id, shown: b.getClientRects().length > 0,
    inPane: r.top >= t.bottom - 1 && r.bottom <= g.bottom + 1 && r.left >= g.left - 1 && r.right <= g.right + 1,
    hash: location.hash, pos: document.getElementById('fail-pos').textContent,
    graphPressed: (document.querySelector('.node[aria-pressed="true"]') || {}).dataset?.id };
});
const modeOf = page => page.evaluate(() => ({
  list: !document.getElementById('stage-list').hidden, graph: !document.getElementById('graph').hidden,
  pressed: document.getElementById('mode-list').getAttribute('aria-pressed'),
  stored: (() => { try { return localStorage.getItem('jkit.export.layout'); } catch (e) { return 'throws'; } })(),
}));

function watch(page, errors, net) {
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
  page.on('pageerror', e => errors.push(String(e)));
  page.on('request', r => { if (!/^(file|data|blob):/.test(r.url())) net.push(r.url()); });
}

async function wide(browserName, file) {
  const tag = `[${browserName} ${path.basename(file)} 1280]`;
  const S = snapshot(file), idx = index(S.stages), leaves = failedLeaves(S.stages);
  const browser = await pw[browserName].launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  const errors = [], net = [];
  watch(page, errors, net);
  const url = 'file://' + path.resolve(file);
  await page.goto(url);
  await page.waitForSelector('.node', { state: 'attached' });

  let m = await modeOf(page);
  check(`${tag} wide default is graph`, m.graph && !m.list && m.pressed === 'false', m);
  await page.click('#mode-list');
  m = await modeOf(page);
  check(`${tag} List shows list, hides graph, stores mode`, m.list && !m.graph && m.pressed === 'true' && JSON.parse(m.stored).mode === 'list', m);

  const rows = await page.$$eval('.sl-sel', bs => bs.map(b => ({ id: b.dataset.id, name: b.querySelector('.sl-name').textContent, dots: b.querySelectorAll('.sl-sum .dot').length })));
  check(`${tag} one row per stage, names from snapshot`, rows.length === idx.size && rows.every(r => idx.has(r.id) && idx.get(r.id).n.name === r.name &&
    r.dots === (idx.get(r.id).n.children || []).length), { rows: rows.length, nodes: idx.size });
  check(`${tag} all rows visible initially`, (await visibleRows(page)).length === idx.size);
  const want = find(S.stages, S.failureNodeId) || defaultNode(S.stages);
  let s = await selRow(page);
  check(`${tag} list opens on ${want.name}, row in view`, s && s.id === want.id && s.shown && s.inPane, s);

  const hscroll = await page.$eval('#graph-scroll', g => g.scrollWidth - g.clientWidth);
  check(`${tag} list has no horizontal scroll`, hscroll <= 0, hscroll);

  // Filter: a word from a nested stage name.
  const deep = [...idx.values()].map(e => e.n).find(n => idx.get(n.id).parent && idx.get(n.id).parent.children.length > 1) || [...idx.values()][0].n;
  const word = deep.name.split(/[\s-]+/).filter(w => w.length > 3).pop() || deep.name;
  const q = word.toUpperCase();
  const leafIds = new Set(leaves.map(n => n.id));
  await page.fill('#sl-filter', q);
  let vis = await visibleRows(page);
  let exp = expected(idx, q, false, leafIds, want.id);
  check(`${tag} filter "${q}" keeps matches + ancestors + selected (${exp.size})`, sameSet(vis, exp), { vis: vis.length, exp: exp.size });
  await page.fill('#sl-filter', 'zz-no-such-stage-zz');
  vis = await visibleRows(page);
  check(`${tag} no match: empty hint, only selected path left`, sameSet(vis, expected(idx, 'zz-no-such-stage-zz', false, leafIds, want.id)) &&
    (await page.textContent('#sl-empty')) === 'No stages match.' && (await page.getAttribute('#sl-empty', 'aria-live')) === 'polite', vis);
  await page.fill('#sl-filter', '');
  check(`${tag} clearing filter restores all`, (await visibleRows(page)).length === idx.size && !(await page.isVisible('#sl-empty')));

  // Failed only.
  await page.check('#sl-failed');
  vis = await visibleRows(page);
  exp = expected(idx, '', true, leafIds, want.id);
  check(`${tag} Failed only keeps ${leaves.length} failed leaves + ancestors (${exp.size})`, sameSet(vis, exp) && (await page.isVisible('#sl-empty')) === (leaves.length === 0), { vis: vis.length, exp: exp.size });
  if (leaves.length) {
    const w2 = leaves[leaves.length - 1].name.slice(0, 6);
    await page.fill('#sl-filter', w2);
    vis = await visibleRows(page);
    exp = expected(idx, w2, true, leafIds, want.id);
    check(`${tag} Failed only + filter "${w2}" combine`, sameSet(vis, exp), { vis: vis.length, exp: exp.size });
    // A branch name finds the failures under it, even when the leaf's own name lacks the word.
    const viaAncestor = leaves.map(l => ({ l, a: (() => { for (let p = idx.get(l.id).parent; p; p = idx.get(p.id).parent) if (p.type === 'PARALLEL') return p; return null; })() }))
      .find(x => x.a && !x.l.name.toLowerCase().includes(x.a.name.toLowerCase()));
    if (viaAncestor) {
      const bq = viaAncestor.a.name;
      await page.fill('#sl-filter', bq);
      vis = await visibleRows(page);
      exp = expected(idx, bq, true, leafIds, want.id);
      check(`${tag} Failed only + branch name "${bq}" shows its failed leaf ${viaAncestor.l.name}`, sameSet(vis, exp) && vis.includes(viaAncestor.l.id) &&
        !(await page.isVisible('#sl-empty')), { vis: vis.length, exp: exp.size });
    }
    // Part of a branch name (service-mod-charlie-src), not all of it.
    if (/^wide-failed/.test(path.basename(file))) {
      await page.fill('#sl-filter', 'charlie');
      vis = await visibleRows(page);
      check(`${tag} "charlie" + Failed only finds the charlie branch failure`, vis.some(id => leafIds.has(id) && id !== want.id) && !(await page.isVisible('#sl-empty')), vis);
    }
    await page.fill('#sl-filter', '');
  }
  await page.uncheck('#sl-failed');

  // A filter keeps the selected row even when it does not match; hash and fail nav both select.
  {
    const other = [...idx.values()].map(e => e.n).find(n => !n.name.toLowerCase().includes('zz-no-such') && n.id !== want.id && idx.get(n.id).parent);
    await page.fill('#sl-filter', 'zz-no-such-stage-zz');
    await page.evaluate(id => { location.hash = 'node=' + encodeURIComponent(id); }, other.id);
    await page.waitForTimeout(150);
    s = await selRow(page);
    vis = await visibleRows(page);
    check(`${tag} hash to filtered-out stage shows its row`, s.id === other.id && s.shown && s.inPane && sameSet(vis, expected(idx, 'zz-no-such-stage-zz', false, leafIds, other.id)), { s, vis });
    if (leaves.length) {
      await page.click('#fail-next');
      s = await selRow(page);
      check(`${tag} fail nav under non-matching filter shows row`, leafIds.has(s.id) && s.shown && s.inPane, s);
    }
    await page.fill('#sl-filter', '');
    await page.evaluate(id => { location.hash = 'node=' + encodeURIComponent(id); }, want.id);
    await page.waitForTimeout(150);
  }

  // Clearing the filter puts chevrons back as they were.
  {
    const parents = [...idx.values()].filter(e => (e.n.children || []).length).map(e => e.n);
    const target = parents.find(p => !idx.get(want.id) || ![...(function* a(id) { for (let p = idx.get(id).parent; p; p = idx.get(p.id).parent) yield p.id; })(want.id)].includes(p.id));
    if (target) {
      const c = page.locator(`li:has(> .sl-row > .sl-sel[data-id="${target.id}"]) > .sl-row > .sl-chev`);
      await c.click();
      const before = await collapsedIds(page);
      await page.fill('#sl-filter', target.children[0].name);
      const during = await c.getAttribute('aria-expanded');
      await page.fill('#sl-filter', '');
      const after = await collapsedIds(page);
      check(`${tag} filter opens collapsed ${target.name}, clearing restores chevrons`, before.includes(target.id) && during === 'true' && after === before, { before, during, after });
      await c.click();
    }
  }

  // Row selection by click.
  const pick = [...idx.values()].map(e => e.n).filter(n => n.id !== want.id).pop();
  await page.click(`.sl-sel[data-id="${pick.id}"]`);
  s = await selRow(page);
  const steps = await page.textContent('#steps-h');
  check(`${tag} click row selects ${pick.name}`, s.id === pick.id && s.inPane && s.graphPressed === pick.id && s.hash.includes('node=' + encodeURIComponent(pick.id)) &&
    steps === 'Steps of ' + pick.name, { s, steps });

  // Keyboard: the list is one tab stop (roving tabindex), arrows move over shown rows.
  {
    const stops = () => page.$$eval('#sl-tree button', bs => bs.filter(b => b.tabIndex >= 0).map(b => b.dataset.id || b.className));
    const focused = () => page.evaluate(() => document.activeElement.dataset.id || document.activeElement.id);
    const inTree = () => page.evaluate(() => document.getElementById('sl-tree').contains(document.activeElement));
    const press = async (k, n = 1) => { for (let i = 0; i < n; i++) await page.keyboard.press(k); };
    // Home, then ArrowDown until focus stops moving: the order arrows visit.
    const walk = async () => {
      await press('Home');
      const seen = [await focused()];
      for (;;) {
        await press('ArrowDown');
        const id = await focused();
        if (id === seen[seen.length - 1] || seen.length > idx.size) return seen;
        seen.push(id);
      }
    };
    const setFilter = (v) => page.evaluate(v => { const f = document.getElementById('sl-filter'); f.value = v; f.dispatchEvent(new Event('input')); }, v);

    let st = await stops();
    check(`${tag} list is one tab stop, on the selected row`, st.length === 1 && st[0] === pick.id, st);
    await page.focus('#sl-failed');
    await press('Tab');
    check(`${tag} Tab from Failed only enters the list on the selected row`, (await focused()) === pick.id, await focused());
    await press('Tab');
    check(`${tag} next Tab leaves the list`, !(await inTree()), await focused());
    await press('Shift+Tab');
    check(`${tag} Shift+Tab comes back to the same row`, (await focused()) === pick.id, await focused());

    let order = await walk();
    let vis = await visibleRows(page);
    check(`${tag} Home + ArrowDown visit every shown row in order`, order.join() === vis.join(), { order: order.length, vis: vis.length });
    await press('End');
    const last = await focused();
    await press('ArrowDown');
    check(`${tag} End goes to the last row, ArrowDown stops there`, last === vis[vis.length - 1] && (await focused()) === last, last);
    await press('ArrowUp');
    check(`${tag} ArrowUp moves to the previous row`, (await focused()) === vis[vis.length - 2]);
    await press('Home');
    check(`${tag} Home goes to the first row`, (await focused()) === vis[0]);
    st = await stops();
    check(`${tag} focused row is the only tab stop`, st.length === 1 && st[0] === vis[0], st);
    check(`${tag} arrows move focus, not selection`, (await selRow(page)).id === pick.id);

    // Right and Left on a parent row.
    // A leaf first child: Left on an open child would collapse it rather than move up.
    const parent = ([...idx.values()].find(e => (e.n.children || []).length && !(e.n.children[0].children || []).length) || {}).n;
    const chevOf = id => page.locator(`li:has(> .sl-row > .sl-sel[data-id="${id}"]) > .sl-row > .sl-chev`);
    if (parent) {
      const c = chevOf(parent.id);
      if ((await c.getAttribute('aria-expanded')) === 'false') await c.click();
      await page.focus(`.sl-sel[data-id="${parent.id}"]`);
      await press('ArrowRight');
      check(`${tag} Right on open ${parent.name} moves to its first child`, (await focused()) === parent.children[0].id, await focused());
      await press('ArrowLeft');
      check(`${tag} Left on a child moves to its parent`, (await focused()) === parent.id, await focused());
      await press('ArrowLeft');
      check(`${tag} Left on open parent collapses it, focus stays`, (await c.getAttribute('aria-expanded')) === 'false' && (await focused()) === parent.id);
      order = await walk();
      vis = await visibleRows(page);
      check(`${tag} arrows skip rows under a collapsed parent`, order.join() === vis.join() && !order.includes(parent.children[0].id), { order: order.length, vis: vis.length });
      await page.focus(`.sl-sel[data-id="${parent.id}"]`);
      await press('ArrowLeft');
      check(`${tag} Left on a closed top-level row does nothing`, idx.get(parent.id).parent || ((await focused()) === parent.id && (await c.getAttribute('aria-expanded')) === 'false'));
      await page.focus(`.sl-sel[data-id="${parent.id}"]`);
      await press('ArrowRight');
      check(`${tag} Right on closed parent expands it, focus stays`, (await c.getAttribute('aria-expanded')) === 'true' && (await focused()) === parent.id);
      const leaf = [...idx.values()].find(e => !(e.n.children || []).length).n;
      await page.focus(`.sl-sel[data-id="${leaf.id}"]`);
      await press('ArrowRight');
      check(`${tag} Right on a leaf does nothing`, (await focused()) === leaf.id);
    }

    // Filtered: arrows follow the filtered rows; the tab stop never sits on a hidden row.
    const hidden = order.find(id => id !== pick.id && ![...expected(idx, 'zz-no-such-stage-zz', false, leafIds, pick.id)].includes(id));
    await page.focus(`.sl-sel[data-id="${hidden}"]`);
    await page.fill('#sl-filter', q);
    await press('Tab');
    await press('Tab');
    vis = await visibleRows(page);
    const entered = await focused();
    check(`${tag} filtered: Tab enters on a shown row`, vis.includes(entered) && (await stops()).length === 1, { entered, hidden });
    order = await walk();
    check(`${tag} filtered: arrows visit exactly the shown rows`, order.join() === vis.join(), { order: order.length, vis: vis.length });
    await page.fill('#sl-filter', '');

    // The focused row gets hidden while it has focus: focus moves to the selected row.
    await page.focus(`.sl-sel[data-id="${hidden}"]`);
    await setFilter('zz-no-such-stage-zz');
    let f = await focused();
    check(`${tag} focused row filtered out: focus moves to the selected row`, f === pick.id && (await stops()).join() === pick.id, { f, hidden });
    await setFilter('');

    // Enter and Space select, as a click does.
    await press('Home');
    await press('ArrowDown');
    const target = await focused();
    await press('Enter');
    s = await selRow(page);
    check(`${tag} Enter on focused row selects it`, s.id === target && s.hash.includes('node=' + encodeURIComponent(target)) && (await focused()) === target, s);
    await press('ArrowUp');
    const t2 = await focused();
    await press('Space');
    check(`${tag} Space on focused row selects it`, (await selRow(page)).id === t2);
    await page.goBack();
    await page.waitForTimeout(150);
    check(`${tag} Back undoes a keyboard selection`, (await selRow(page)).id === target);

    // A selection from outside the list moves the tab stop to it.
    if (leaves.length) {
      await page.click('#fail-next');
      s = await selRow(page);
      check(`${tag} failed nav moves the tab stop to the selected row`, (await stops()).join() === s.id, { stops: await stops(), s: s.id });
    }

    // A chevron click keeps focus on the row, so arrows go on working.
    if (parent) {
      const c = chevOf(parent.id);
      await page.focus(`.sl-sel[data-id="${parent.id}"]`);
      await c.click();
      vis = await visibleRows(page);
      await press('ArrowDown');
      f = await focused();
      check(`${tag} chevron click, then ArrowDown moves to the next shown row`, (await c.getAttribute('aria-expanded')) === 'false' &&
        f === vis[vis.indexOf(parent.id) + 1], { f, next: vis[vis.indexOf(parent.id) + 1] });
      await c.click();
    }

    await page.click('#mode-graph');
    await page.click('#mode-list');
    await page.click('#mode-graph');
    await page.click('#mode-list');
    st = await stops();
    check(`${tag} one tab stop after Graph, List, Graph, List`, st.length === 1, st);
    if (await page.isVisible('#collapse-all')) {
      await page.click('#collapse-all');
      st = await stops();
      vis = await visibleRows(page);
      check(`${tag} one shown tab stop after Collapse all`, st.length === 1 && vis.includes(st[0]), st);
      await page.click('#expand-all');
      st = await stops();
      check(`${tag} one tab stop after Expand all`, st.length === 1, st);
    }
  }

  // Chevrons.
  const chev = await page.$$eval('button.sl-chev', cs => cs.map(c => {
    const u = document.getElementById(c.getAttribute('aria-controls'));
    return { ok: !!u && u.tagName === 'UL' && u.parentElement === c.closest('li') && c.getAttribute('aria-expanded') === 'true' && c.tabIndex === -1 && /^Nested stages of /.test(c.getAttribute('aria-label')) };
  }));
  const withKids = [...idx.values()].filter(e => (e.n.children || []).length).length;
  check(`${tag} chevron per parent row, aria-controls own kids, not a tab stop`, chev.length === withKids && chev.every(c => c.ok), { chev: chev.length, withKids });
  if (withKids) {
    const parent = [...idx.values()].find(e => (e.n.children || []).length).n;
    const c = page.locator(`li:has(> .sl-row > .sl-sel[data-id="${parent.id}"]) > .sl-row > .sl-chev`);
    await c.click();
    const kidsShown = await page.$$eval('.sl-sel', (bs, ids) => bs.filter(b => ids.includes(b.dataset.id) && b.getClientRects().length).length, parent.children.map(k => k.id));
    check(`${tag} chevron click collapses ${parent.name}`, (await c.getAttribute('aria-expanded')) === 'false' && kidsShown === 0 && (await c.textContent()) === '▸');
    await c.focus();
    await page.keyboard.press('Enter');
    check(`${tag} Enter on chevron expands`, (await c.getAttribute('aria-expanded')) === 'true');
    await page.keyboard.press('Space');
    check(`${tag} Space on chevron collapses`, (await c.getAttribute('aria-expanded')) === 'false');
    // Selecting a hidden child via hash opens its parent.
    await page.evaluate(id => { location.hash = 'node=' + encodeURIComponent(id); }, parent.children[parent.children.length - 1].id);
    await page.waitForTimeout(150);
    s = await selRow(page);
    check(`${tag} hash into collapsed row opens it`, s.id === parent.children[parent.children.length - 1].id && s.shown && s.inPane && (await c.getAttribute('aria-expanded')) === 'true', s);
  }

  // Collapse/expand all act on the list in list mode.
  if (await page.isVisible('#collapse-all')) {
    await page.click('#collapse-all');
    const r = await page.evaluate(() => ({ collapsed: document.querySelectorAll('.sl-item.collapsed').length, graphCollapsedBefore: document.querySelectorAll('.branch.collapsed').length }));
    s = await selRow(page);
    check(`${tag} collapse all collapses list, selected stays visible`, r.collapsed > 0 && s.shown && s.inPane, { r, s });
    await page.click('#expand-all');
    check(`${tag} expand all opens list`, (await page.$$eval('.sl-item.collapsed', x => x.length)) === 0);
  }

  // Failed navigation in list mode, short pane so scrolling is needed.
  // List mode has a taller minimum: the filter bar would cover an 80px pane.
  await page.focus('#graph-split');
  await page.keyboard.press('Home');
  {
    const r = await page.evaluate(() => ({ h: document.getElementById('graph-scroll').getBoundingClientRect().height,
      min: document.getElementById('graph-split').getAttribute('aria-valuemin'), now: +document.getElementById('graph-split').getAttribute('aria-valuenow') }));
    const rows = await page.evaluate(() => { const t = document.getElementById('sl-tools').getBoundingClientRect().bottom, g = document.getElementById('graph-scroll').getBoundingClientRect().bottom;
      return [...document.querySelectorAll('.sl-sel')].filter(b => { const x = b.getBoundingClientRect(); return x.height && x.top >= t - 1 && x.bottom <= g + 1; }).length; });
    check(`${tag} list min pane 144px, aria-valuemin 144, >=2 rows fit`, Math.abs(r.h - 144) <= 1 && r.min === '144' && Math.abs(r.now - 144) <= 1 && rows >= 2, { r, rows });
    await page.click('#mode-graph');
    await page.focus('#graph-split');
    await page.keyboard.press('Home');
    const g = await page.evaluate(() => ({ h: document.getElementById('graph-scroll').getBoundingClientRect().height, min: document.getElementById('graph-split').getAttribute('aria-valuemin') }));
    check(`${tag} graph mode min stays 80`, Math.abs(g.h - 80) <= 1 && g.min === '80', g);
    await page.click('#mode-list');
    const l2 = await page.evaluate(() => document.getElementById('graph-scroll').getBoundingClientRect().height);
    check(`${tag} switching to list with 80px pane lifts it to 144`, Math.abs(l2 - 144) <= 1, l2);
  }
  // Collapse all, then a full fail cycle leaves the list collapsed as before.
  if (leaves.length && await page.isVisible('#collapse-all')) {
    await page.click('#collapse-all');
    const before = await page.$$eval('.sl-item.collapsed', x => x.length);
    for (let i = 0; i < leaves.length; i++) await page.click('#fail-next');
    const after = await page.$$eval('.sl-item.collapsed', x => x.length);
    s = await selRow(page);
    let anc = 0;
    for (let p = idx.get(s.id).parent; p; p = idx.get(p.id).parent) anc++;
    const parentsN = [...idx.values()].filter(e => (e.n.children || []).length).length;
    check(`${tag} list collapse survives full fail cycle`, before > 0 && after === parentsN - anc && s.shown, { before, after, parentsN, anc });
    await page.click('#expand-all');
  }
  if (leaves.length) {
    await page.evaluate(() => { location.hash = ''; });
    let ok = true;
    const seen = [];
    for (let i = 0; i < leaves.length; i++) {
      await page.click('#fail-next');
      s = await selRow(page);
      seen.push(s.id);
      const k = leaves.findIndex(n => n.id === s.id);
      if (!(k >= 0 && s.shown && s.inPane && s.pos === `${k + 1} of ${leaves.length} failed`)) { ok = false; console.log('  bad', JSON.stringify(s)); }
    }
    check(`${tag} failed next visits all ${leaves.length} leaves, row scrolled into view`, ok && new Set(seen).size === leaves.length, seen.slice(0, 10));
    await page.dblclick('#graph-split');
  }

  // Persistence.
  await page.reload();
  await page.waitForSelector('.node', { state: 'attached' });
  m = await modeOf(page);
  check(`${tag} list mode persists across reload`, m.list && !m.graph, m);
  s = await selRow(page);
  check(`${tag} after reload selected row from hash shown`, s && s.shown && s.inPane, s);
  await page.click('#mode-graph');
  await page.reload();
  await page.waitForSelector('.node', { state: 'attached' });
  m = await modeOf(page);
  const st = JSON.parse(m.stored);
  check(`${tag} graph mode persists, other prefs kept`, m.graph && !m.list && st.mode === 'graph', m);
  const gsel = await page.evaluate(() => { const b = document.querySelector('.node[aria-pressed="true"]'); return b && b.getBoundingClientRect().width; });
  check(`${tag} graph node selected after switch back`, gsel > 0, gsel);

  check(`${tag} no console errors`, errors.length === 0, errors);
  check(`${tag} no network requests`, net.length === 0, net);
  await page.evaluate(() => localStorage.clear());
  await page.setViewportSize({ width: 1600, height: 1000 });
  await page.reload();
  await page.click('#mode-list');
  await page.screenshot({ path: `${SHOTS}/${browserName}-${path.basename(file, '.html')}-1600-list.png` });
  await page.check('#sl-failed');
  await page.screenshot({ path: `${SHOTS}/${browserName}-${path.basename(file, '.html')}-1600-failed.png` });
  await page.evaluate(() => localStorage.clear());
  await browser.close();
}

async function narrow(browserName, file) {
  const tag = `[${browserName} ${path.basename(file)} 390]`;
  const S = snapshot(file), idx = index(S.stages), leaves = failedLeaves(S.stages);
  const browser = await pw[browserName].launch();
  const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } });
  const page = await ctx.newPage();
  const errors = [], net = [];
  watch(page, errors, net);
  const url = 'file://' + path.resolve(file);
  await page.goto(url);
  await page.waitForSelector('.sl-sel');
  let m = await modeOf(page);
  check(`${tag} narrow default is list, not stored`, m.list && !m.graph && m.pressed === 'true' && !(m.stored && JSON.parse(m.stored).mode), m);
  const lay = await page.evaluate(() => {
    const g = document.getElementById('graph-scroll');
    const over = [...document.querySelectorAll('.sl-sel')].filter(b => b.getClientRects().length).filter(b => b.getBoundingClientRect().right > g.getBoundingClientRect().right + 1).length;
    const names = [...document.querySelectorAll('.sl-name')].filter(n => n.getClientRects().length && n.clientWidth < 60).length;
    return { pageX: document.documentElement.scrollWidth - window.innerWidth, paneX: g.scrollWidth - g.clientWidth, over, narrowNames: names, h: g.clientHeight };
  });
  check(`${tag} no page or pane h-scroll, rows fit`, lay.pageX <= 0 && lay.paneX <= 0 && lay.over === 0, lay);
  check(`${tag} names readable (>=60px)`, lay.narrowNames === 0, lay);
  const want = find(S.stages, S.failureNodeId) || defaultNode(S.stages);
  let s = await selRow(page);
  check(`${tag} opens on ${want.name}, in view`, s && s.id === want.id && s.inPane, s);
  await page.fill('#sl-filter', want.name.slice(0, 5));
  const vis = await visibleRows(page);
  const exp = expected(idx, want.name.slice(0, 5), false, new Set(leaves.map(l => l.id)), want.id);
  check(`${tag} filter works at 390`, sameSet(vis, exp), { vis: vis.length, exp: exp.size });
  await page.fill('#sl-filter', '');
  if (leaves.length) {
    await page.check('#sl-failed');
    const v2 = await visibleRows(page);
    check(`${tag} Failed only at 390`, sameSet(v2, expected(idx, '', true, new Set(leaves.map(l => l.id)), want.id)));
    await page.screenshot({ path: `${SHOTS}/${browserName}-${path.basename(file, '.html')}-390-failed.png`, fullPage: false });
    await page.uncheck('#sl-failed');
  }
  const other = [...idx.keys()].find(id => id !== want.id);
  await page.click(`.sl-sel[data-id="${other}"]`);
  s = await selRow(page);
  check(`${tag} tap row selects at 390`, s.id === other && s.inPane);
  await page.evaluate(id => { location.hash = 'node=' + encodeURIComponent(id); }, want.id);
  await page.waitForTimeout(100);
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: `${SHOTS}/${browserName}-${path.basename(file, '.html')}-390-list.png` });
  // An explicit choice wins over the narrow default.
  await page.click('#mode-graph');
  await page.reload();
  await page.waitForSelector('.node', { state: 'attached' });
  m = await modeOf(page);
  check(`${tag} chosen graph kept at 390 after reload`, m.graph && !m.list, m);
  check(`${tag} no console errors`, errors.length === 0, errors);
  check(`${tag} no network requests`, net.length === 0, net);

  // Throwing storage: narrow default still applies, no errors.
  const p2 = await ctx.newPage();
  const e2 = [];
  watch(p2, e2, net);
  await p2.addInitScript(() => { Object.defineProperty(window, 'localStorage', { get() { throw new Error('denied'); } }); });
  await p2.goto(url);
  await p2.waitForSelector('.sl-sel');
  await p2.click('#mode-graph');
  await p2.click('#mode-list');
  m = await modeOf(p2);
  check(`${tag} throwing localStorage: list works, no errors`, m.list && e2.length === 0, { m, e2 });
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
