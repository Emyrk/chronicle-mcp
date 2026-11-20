'use strict';
(() => {
  const byId = (id) => document.getElementById(id);
  const state = byId('state'), service = byId('service'), connections = byId('connections');
  const refresh = byId('refresh'), shutdown = byId('shutdown'), message = byId('message');
  const dialog = byId('confirmation');
  let timer, latest, failures = 0, stopped = false, busy = false, polling = false;
  const text = (tag, value, className) => {
    const node = document.createElement(tag);
    node.textContent = String(value);
    if (className) node.className = className;
    return node;
  };
  function field(list, label, value) { list.append(text('dt', label), text('dd', value)); }
  async function request(path, body) {
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 5000);
    try {
      const response = await fetch(path, {
        method: body === undefined ? 'GET' : 'POST', cache: 'no-store', credentials: 'omit',
        headers: body === undefined ? {} : {'Content-Type': 'application/json', 'X-Chronicle-Dashboard': '1'},
        body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal,
      });
      const data = await response.json();
      if (!response.ok) { const error = new Error(data.message || data.error || `HTTP ${response.status}`); error.status = response.status; throw error; }
      return data;
    } finally { clearTimeout(timeout); }
  }
  function confirm(title, detail, action) {
    byId('confirm-title').textContent = title;
    byId('confirm-detail').textContent = detail;
    byId('confirm-action').textContent = action;
    dialog.returnValue = 'cancel';
    return new Promise((resolve) => {
      dialog.addEventListener('close', () => resolve(dialog.returnValue === 'confirm'), {once: true});
      dialog.showModal();
    });
  }
  function enableActions() {
    refresh.disabled = busy || polling;
    shutdown.disabled = busy || stopped || !latest;
    for (const button of connections.querySelectorAll('button')) button.disabled = busy || stopped || button.dataset.unowned === 'true';
  }
  function render(data) {
    latest = data;
    state.textContent = 'Online';
    service.replaceChildren();
    field(service, 'Version', `${data.version} · protocol ${data.protocol}`);
    field(service, 'Uptime', data.uptime);
    field(service, 'Agent sessions', data.agents);
    field(service, 'Pending operations', data.pending);
    field(service, 'Idle shutdown', data.idle.disabled ? 'Disabled' : `${data.idle.remaining} remaining${data.idle.pendingCall ? ' · pending work prevents idle shutdown' : ''}`);
    field(service, 'Last agent activity', new Date(data.lastAgentActivity).toLocaleString());
    connections.replaceChildren();
    byId('empty').hidden = data.connections.length !== 0;
    byId('empty').textContent = 'No browser tabs connected. Connect a tab from Chronicle to see it here.';
    for (const connection of data.connections) {
      const card = text('article', '', 'connection');
      card.append(text('h3', connection.name));
      const fields = document.createElement('dl');
      field(fields, 'Connection ID', connection.id);
      field(fields, 'Owner', connection.owner ? `${connection.owner.name} (${connection.owner.version}) · ${connection.owner.id}` : 'Available');
      field(fields, 'Pending work', connection.pending ? 'Yes' : 'None');
      field(fields, 'Ownership revision', connection.ownershipRevision);
      fields.append(text('dt', 'Page'));
      const page = document.createElement('dd');
      const rawURL = connection.page?.url || '';
      try {
        const url = new URL(rawURL);
        if (!['http:', 'https:'].includes(url.protocol)) throw new Error('Unsupported URL');
        const link = text('a', rawURL); link.href = url.href; link.target = '_blank'; link.rel = 'noopener noreferrer'; page.append(link);
      } catch { page.textContent = rawURL || 'Unknown'; }
      fields.append(page); card.append(fields);
      const revoke = text('button', 'Revoke agent control…'); revoke.type = 'button'; revoke.dataset.unowned = String(!connection.owner);
      revoke.addEventListener('click', () => revokeConnection(connection)); card.append(revoke); connections.append(card);
    }
    enableActions();
  }
  function schedule(delay) { clearTimeout(timer); if (!stopped) timer = setTimeout(poll, delay); }
  async function poll() {
    if (polling || busy || stopped) return;
    polling = true; enableActions();
    try {
      const data = await request('/dashboard/api/status');
      if (stopped) return;
      render(data); failures = 0; schedule(2000);
    }
    catch (error) {
      if (stopped) return;
      failures++; state.textContent = 'Offline'; latest = undefined;
      message.textContent = `Service unavailable: ${error.message}. ${failures >= 4 ? 'Automatic polling stopped. Use Refresh after restarting the daemon.' : 'Retrying briefly…'}`;
      if (failures >= 4) stopped = true; else schedule(Math.min(1000 * 2 ** failures, 8000));
    } finally { polling = false; enableActions(); }
  }
  async function revokeConnection(connection) {
    if (busy || stopped) return;
    busy = true; clearTimeout(timer); enableActions();
    try {
      if (!await confirm('Revoke agent control?', `Release ${connection.owner?.name || 'the agent'} from “${connection.name}”? Pending work and approvals will be canceled. Already-running mutations cannot be rolled back. Browser edits remain unchanged.`, 'Revoke control')) return;
      await request('/dashboard/api/revoke', {connectionId: connection.id, expectedOwnershipRevision: connection.ownershipRevision});
      message.textContent = 'Agent control released. Browser working copy is unchanged.';
    } catch (error) { message.textContent = `Revoke failed: ${error.message}. Refresh and confirm again; no automatic retry was sent.`; }
    finally { busy = false; enableActions(); schedule(0); }
  }
  shutdown.addEventListener('click', async () => {
    if (busy || stopped || !latest) return;
    busy = true; clearTimeout(timer); enableActions();
    const active = latest.agents > 0 || latest.pending > 0;
    try {
      if (!await confirm(active ? 'Force shutdown with active agents?' : 'Shut down local daemon?', active ? `${latest.agents} agent session(s) and ${latest.pending} pending operation(s) are active. This explicitly disconnects all agents and cancels work. Browser edits are not saved or discarded.` : 'Stop this local service? Browser tabs and their working copies remain open and unchanged.', active ? 'Force shutdown' : 'Shut down')) return;
      await request('/dashboard/api/shutdown', {force: active});
      stopped = true; latest = undefined; state.textContent = 'Offline'; clearTimeout(timer);
      message.textContent = 'Shutdown requested. Polling stopped. Restart the daemon and use Refresh to reconnect.';
    } catch (error) { message.textContent = `Shutdown failed: ${error.message}. No automatic forced retry was sent.`; }
    finally { busy = false; enableActions(); if (!stopped) schedule(0); }
  });
  refresh.addEventListener('click', () => { if (busy || polling) return; stopped = false; failures = 0; clearTimeout(timer); poll(); });
  window.addEventListener('pagehide', () => {stopped = true; clearTimeout(timer);});
  poll();
})();
