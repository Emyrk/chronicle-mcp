import test from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import vm from 'node:vm';

const source = await readFile(new URL('../internal/relay/dashboard/app.js', import.meta.url), 'utf8');
class Node {
  constructor(tag = '') { this.tag = tag; this.children = []; this.events = new Map(); this.dataset = {}; this.textContent = ''; this.disabled = false; }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  addEventListener(name, listener, options = {}) {
    const listeners = this.events.get(name) || []; listeners.push({listener, once: options.once}); this.events.set(name, listeners);
  }
  dispatch(name, event = {}) {
    const listeners = [...(this.events.get(name) || [])];
    this.events.set(name, listeners.filter(({once}) => !once));
    return Promise.all(listeners.map(({listener}) => listener(event)));
  }
  querySelectorAll(tag) { return this.children.flatMap(child => [...(child.tag === tag ? [child] : []), ...child.querySelectorAll(tag)]); }
  showModal() { this.open = true; }
  close(value) { this.returnValue = value; this.open = false; return this.dispatch('close'); }
}
function status() {
  return {version: '0.2.1', protocol: 3, uptime: '4s', agents: 1, pending: 0,
    idle: {disabled: false, remaining: '5h59m59.564321s', pendingCall: false}, lastAgentActivity: '2026-01-01T00:00:00Z',
    connections: [{id: 'tab', name: '<img src=x onerror=alert(1)>', page: {url: 'https://chronicleclassic.com/edit'}, owner: {id: 'a'.repeat(64), name: 'agent', version: '1'}, ownershipRevision: 1, pending: false}]};
}
const settle = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve; const promise = new Promise(r => {resolve = r;}); return {promise, resolve}; }
function harness() {
  const elements = new Map(), window = new Node(), timers = new Map(), calls = [], responses = [];
  let timerID = 0;
  const get = id => { if (!elements.has(id)) elements.set(id, new Node()); return elements.get(id); };
  const context = {document: {getElementById: get, createElement: tag => new Node(tag)}, window, URL, Date, AbortController,
    setTimeout(fn, delay) { const id = ++timerID; timers.set(id, {fn, delay}); return id; }, clearTimeout(id) {timers.delete(id);},
    async fetch(path, options) {
      calls.push({path, options});
      const next = responses.length ? responses.shift() : status();
      if (next instanceof Error) throw next;
      const data = await next;
      return {ok: true, json: async () => data};
    },
  };
  vm.runInNewContext(source, context, {filename: 'dashboard/app.js'});
  return {get, window, calls, responses, timers, async runTimer(delay) {
    const entry = [...timers].find(([, timer]) => timer.delay === delay); assert.ok(entry, `missing ${delay}ms timer`);
    timers.delete(entry[0]); entry[1].fn(); await settle();
  }, buttons() {return get('connections').querySelectorAll('button');}};
}
function fields(node) { const result = new Map(); for (let i = 0; i < node.children.length; i += 2) result.set(node.children[i].textContent, node.children[i + 1]); return result; }

test('dashboard disables stale actions offline, clears outage on recovery, and formats metadata', async () => {
  const h = harness(); await settle();
  assert.equal(h.get('state').textContent, 'Online'); assert.equal(h.buttons()[0].disabled, false);
  assert.equal(fields(h.get('service')).get('Idle shutdown').textContent, '5h 59m 59s remaining');
  const card = h.get('connections').children[0]; assert.equal(card.children[0].textContent, '<img src=x onerror=alert(1)>');
  const details = fields(card.children[1]); assert.equal(details.get('Owner').textContent, 'agent (1)');
  assert.equal(details.get('Owner ID').textContent, 'aaaaaaaaaaaa…'); assert.equal(details.get('Owner ID').title, 'a'.repeat(64));
  h.responses.push(new Error('connection refused')); await h.runTimer(2000);
  assert.equal(h.get('state').textContent, 'Offline'); assert.equal(h.buttons()[0].disabled, true); assert.equal(h.get('shutdown').disabled, true);
  const count = h.calls.length; await h.buttons()[0].dispatch('click'); assert.equal(h.calls.length, count); assert.equal(h.get('confirmation').open, undefined);
  assert.match(h.get('message').textContent, /Service unavailable/);
  h.responses.push(status()); await h.runTimer(2000);
  assert.equal(h.get('state').textContent, 'Online'); assert.equal(h.get('message').textContent, ''); assert.equal(h.buttons()[0].disabled, false);
});

test('dashboard BFCache restore resumes with fresh status and ignores pre-hide responses', async () => {
  const h = harness(); await settle(); const old = deferred(); h.responses.push(old.promise); await h.runTimer(2000);
  await h.window.dispatch('pagehide'); assert.equal(h.buttons()[0].disabled, true);
  await h.window.dispatch('pageshow', {persisted: true}); await h.runTimer(0);
  const stale = status(); stale.version = 'stale-version'; old.resolve(stale); await settle();
  assert.doesNotMatch(fields(h.get('service')).get('Version').textContent, /stale-version/);
  assert.equal(h.buttons()[0].disabled, true);
  const fresh = status(); fresh.version = 'fresh-version'; h.responses.push(fresh); await h.runTimer(0);
  assert.match(fields(h.get('service')).get('Version').textContent, /fresh-version/); assert.equal(h.buttons()[0].disabled, false);
});

test('dashboard bounds automatic retries and explicit Refresh recovers without stale message', async () => {
  const h = harness(); await settle();
  for (const delay of [2000, 2000, 4000, 8000]) {h.responses.push(new Error('offline')); await h.runTimer(delay);}
  assert.equal(h.timers.size, 0); assert.match(h.get('message').textContent, /Automatic polling stopped/);
  const count = h.calls.length; await h.window.dispatch('pagehide'); await h.window.dispatch('pageshow', {persisted: true}); await settle();
  assert.equal(h.calls.length, count); assert.equal(h.timers.size, 0);
  h.responses.push(status()); await h.get('refresh').dispatch('click'); await settle();
  assert.equal(h.get('state').textContent, 'Online'); assert.equal(h.get('message').textContent, ''); assert.equal(h.buttons()[0].disabled, false);
});

test('confirmed shutdown stays stopped after BFCache restoration until explicit Refresh', async () => {
  const h = harness(); await settle(); const action = h.get('shutdown').dispatch('click'); await settle();
  assert.equal(h.get('confirmation').open, true); assert.equal(h.get('confirm-action').textContent, 'Force shutdown');
  h.responses.push({ok: true}); await h.get('confirmation').close('confirm'); await action; await settle();
  const mutation = h.calls.find(call => call.path === '/dashboard/api/shutdown'); assert.equal(JSON.parse(mutation.options.body).force, true);
  assert.equal(h.get('state').textContent, 'Offline'); assert.equal(h.buttons()[0].disabled, true); assert.equal(h.timers.size, 0);
  const count = h.calls.length; await h.window.dispatch('pagehide'); await h.window.dispatch('pageshow', {persisted: true}); await settle();
  assert.equal(h.calls.length, count); assert.equal(h.timers.size, 0); assert.match(h.get('message').textContent, /Shutdown requested/);
  h.responses.push(status()); await h.get('refresh').dispatch('click'); await settle(); assert.equal(h.get('state').textContent, 'Online'); assert.equal(h.get('message').textContent, '');
});
