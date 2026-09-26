import assert from 'node:assert/strict';
import { after, before, test } from 'node:test';
import { createServer } from 'node:http';
import { chromium, expect as baseExpect } from '@playwright/test';
import { dragFromGrabArea } from '../browser/drag.mjs';

const expect = baseExpect.configure({ timeout: 3000 });
let browser, server, origin;
const fixtures = new Map();
function html(mode, state) {
  const title = 'A useful card';
  const interior = mode.startsWith('all-') ? {
    'all-select': '<select aria-label="Move card"><option>A useful card</option></select>',
    'all-button': '<button>A useful card</button>',
    'all-label': '<label>A useful card</label>',
    'all-link': '<a href="#">A useful card</a>',
    'all-editable': '<div contenteditable="true">A useful card</div>',
    'all-role': '<div role="button">A useful card</div>',
  }[mode] : mode === 'blank' ? '' : '<p>A useful card</p><select aria-label="Move card"><option>Left</option></select>';
  return `<!doctype html><style>
    body{margin:20px;font:16px sans-serif}.board{display:flex;gap:40px}section{width:300px;height:260px;background:#eee;padding:20px}
    article{position:relative;border:3px solid #999;padding:10px;height:110px;background:white;box-sizing:border-box}
    p{margin:0 0 8px;line-height:20px}select{display:block;width:100%;height:50px}
    .all article{padding:0}.all article>*{display:block;margin:0;width:100%;height:100%;box-sizing:border-box}
    #overlay{position:absolute;z-index:10;background:#ccc}
  </style><main class="${mode.startsWith('all-') ? 'all' : ''}"><div class="board">
    <section aria-label="Left"><h2>Left</h2>${state === 'Left' ? `<article aria-label="${title}" draggable="true">${interior}</article>` : ''}</section>
    <section aria-label="Right"><h2>Right</h2>${state === 'Right' ? `<article aria-label="${title}" draggable="true">${interior}</article>` : ''}</section>
  </div></main><script>
    window.observed=[];
    for(const type of ['mousedown','dragstart','drop']) document.addEventListener(type,e=>observed.push({type,tag:e.target.tagName,region:e.target.closest('section')?.getAttribute('aria-label')}),true);
    const article=document.querySelector('article');
    article.addEventListener('dragstart',e=>e.dataTransfer.setData('text/plain','card'));
    const target=document.querySelector('section[aria-label="Right"]');
    target.addEventListener('dragover',e=>e.preventDefault());
    target.addEventListener('drop',async e=>{e.preventDefault();if(${JSON.stringify(mode)}==='broken')return;
      if(e.dataTransfer.getData('text/plain')!=='card')return;
      const r=await fetch(location.pathname,{method:'PATCH',body:'Right'});if(r.ok)target.append(article);
    });
    if(${JSON.stringify(mode)}==='overlay'){const r=article.getBoundingClientRect(),o=document.createElement('div');o.id='overlay';Object.assign(o.style,{left:r.x+'px',top:r.y+'px',width:r.width+'px',height:r.height+'px'});document.body.append(o);}
  </script>`;
}
before(async () => {
  server = createServer(async (req, res) => {
    const key = req.url.split('?')[0];
    const fixture = fixtures.get(key);
    if (!fixture) { res.writeHead(404).end(); return; }
    if (req.method === 'PATCH') {
      let body = ''; for await (const chunk of req) body += chunk;
      fixture.patches.push(body); fixture.state = body;
      res.writeHead(200, { 'Content-Type': 'application/json' }).end('{}'); return;
    }
    res.writeHead(200, { 'Content-Type': 'text/html' }).end(html(fixture.mode, fixture.state));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  origin = `http://127.0.0.1:${server.address().port}`;
  browser = await chromium.launch({ headless: true });
});
after(async () => { await browser?.close(); if (server) await new Promise(resolve => server.close(resolve)); });
async function runFixture(mode, fn) {
  const path = '/' + mode;
  const fixture = { mode, state: 'Left', patches: [] }; fixtures.set(path, fixture);
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  try {
    await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort());
    const page = await context.newPage(); page.setDefaultTimeout(3000);
    await page.goto(origin + path);
    const article = page.getByRole('article', { name: 'A useful card', exact: true });
    await fn({ page, article, target: page.getByRole('region', { name: 'Right', exact: true }), fixture });
  } finally { await context.close(); }
}
test('native-select center uses visible title; one real drag reaches unchanged target and persists', async () => {
  await runFixture('valid', async ({ page, article, target, fixture }) => {
    assert.equal(await article.evaluate(el => { const r = el.getBoundingClientRect(); return document.elementFromPoint(r.x+r.width/2,r.y+r.height/2).tagName; }), 'SELECT');
    await dragFromGrabArea(article, target, 'A useful card');
    await expect(target.getByRole('article', { name: 'A useful card', exact: true })).toBeVisible();
    const events = await page.evaluate(() => observed);
    assert.deepEqual(events, [{ type: 'mousedown', tag: 'P', region: 'Left' }, { type: 'dragstart', tag: 'ARTICLE', region: 'Left' }, { type: 'drop', tag: 'SECTION', region: 'Right' }]);
    assert.deepEqual(fixture.patches, ['Right']);
    await page.reload();
    await expect(target.getByRole('article', { name: 'A useful card', exact: true })).toBeVisible();
  });
});
test('broken application drop behavior still fails the unchanged visibility obligation', async () => {
  await runFixture('broken', async ({ page, article, target, fixture }) => {
    await dragFromGrabArea(article, target, 'A useful card');
    await assert.rejects(expect(target.getByRole('article', { name: 'A useful card', exact: true })).toBeVisible(), /3000ms/);
    assert.equal((await page.evaluate(() => observed.filter(e => e.type === 'dragstart'))).length, 1);
    assert.deepEqual(fixture.patches, []); assert.equal(fixture.state, 'Left');
  });
});
test('fully interactive cards decline before any pointer gesture', async t => {
  for (const mode of ['all-select', 'all-button', 'all-label', 'all-link', 'all-editable', 'all-role']) await t.test(mode, async () => {
    await runFixture(mode, async ({ page, article, target, fixture }) => {
      await assert.rejects(dragFromGrabArea(article, target, 'A useful card'), /No unobstructed noninteractive grab area/);
      assert.deepEqual(await page.evaluate(() => observed), []); assert.deepEqual(fixture.patches, []);
    });
  });
});
test('an external overlay declines rather than pressing it or dispatching synthetic events', async () => {
  await runFixture('overlay', async ({ page, article, target, fixture }) => {
    await assert.rejects(dragFromGrabArea(article, target, 'A useful card'), /No unobstructed noninteractive grab area/);
    assert.deepEqual(await page.evaluate(() => observed), []); assert.deepEqual(fixture.patches, []);
  });
});
test('a plain card without title text keeps working using an unobstructed interior sample', async () => {
  await runFixture('blank', async ({ page, article, target, fixture }) => {
    await dragFromGrabArea(article, target, 'A useful card');
    await expect(target.getByRole('article', { name: 'A useful card', exact: true })).toBeVisible();
    assert.equal((await page.evaluate(() => observed.filter(e => e.type === 'dragstart'))).length, 1);
    assert.deepEqual(fixture.patches, ['Right']);
  });
});
