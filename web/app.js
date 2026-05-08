let wasmReady = false;
let nodeRunning = false;

const statusEl = document.getElementById('runtimeStatus');
const responseEl = document.getElementById('response');

function appendLog(line) {
  const logs = document.getElementById('logs');
  if (!logs) {
    return;
  }
  const atBottom = logs.scrollTop + logs.clientHeight >= logs.scrollHeight - 8;
  logs.textContent += `${line}\n`;
  if (atBottom) {
    logs.scrollTop = logs.scrollHeight;
  }
}

window.yggDemoAppendLog = appendLog;

function setStatus(kind, text) {
  statusEl.className = `status ${kind}`;
  statusEl.textContent = text;
}

function syncButtons() {
  document.getElementById('startNode').disabled = !wasmReady;
  document.getElementById('stopNode').disabled = !wasmReady || !nodeRunning;
  document.getElementById('refreshState').disabled = !wasmReady || !nodeRunning;
  document.getElementById('runRequest').disabled = !wasmReady || !nodeRunning;
}

function startConfig() {
  return {
    manualPeers: document.getElementById('manualPeers').value,
    countries: document.getElementById('countries').value,
    transportSchemes: document.getElementById('schemes').value,
    dnsFallback: document.getElementById('dnsFallback').value,
  };
}

function requestConfig() {
  return {
    method: document.getElementById('method').value,
    url: document.getElementById('requestUrl').value.trim(),
    headers: document.getElementById('headers').value,
    body: document.getElementById('body').value,
  };
}

function renderState(state) {
  document.getElementById('address').textContent = state.address || '-';
  document.getElementById('subnet').textContent = state.subnet || '-';
  const tbody = document.getElementById('peers');
  tbody.textContent = '';
  if (!state.peers || state.peers.length === 0) {
    const row = tbody.insertRow();
    const cell = row.insertCell();
    cell.colSpan = 4;
    cell.textContent = 'No peers yet.';
    return;
  }
  for (const peer of state.peers) {
    const row = tbody.insertRow();
    row.insertCell().textContent = peer.uri || '';
    row.insertCell().textContent = peer.up ? 'up' : 'down';
    row.insertCell().textContent = peer.inbound ? 'inbound' : 'outbound';
    row.insertCell().textContent = peer.error || '';
  }
}

async function loadWasm() {
  try {
    const go = new Go();
    const response = await fetch('app.wasm');
    const bytes = await response.arrayBuffer();
    const result = await WebAssembly.instantiate(bytes, go.importObject);
    go.run(result.instance);
    wasmReady = true;
    setStatus('ready', 'Ready');
    appendLog('UI ready');
  } catch (error) {
    console.error(error);
    appendLog(`UI wasm load failed: ${error.message || error}`);
    setStatus('error', `WASM load failed: ${error.message || error}`);
  }
  syncButtons();
}

async function startNode() {
  setStatus('loading', 'Starting node');
  responseEl.textContent = '';
  syncButtons();
  try {
    const raw = await yggDemoStart(JSON.stringify(startConfig()));
    nodeRunning = true;
    renderState(JSON.parse(raw));
    setStatus('success', 'Node running');
  } catch (error) {
    appendLog(`UI start failed: ${error.message || error}`);
    nodeRunning = false;
    setStatus('error', `Start failed: ${error.message || error}`);
  }
  syncButtons();
}

async function stopNode() {
  try {
    await yggDemoStop();
  } finally {
    nodeRunning = false;
    renderState({ peers: [] });
    document.getElementById('address').textContent = '-';
    document.getElementById('subnet').textContent = '-';
    setStatus('ready', 'Stopped');
    syncButtons();
  }
}

async function refreshState() {
  try {
    const raw = await yggDemoState();
    renderState(JSON.parse(raw));
  } catch (error) {
    appendLog(`UI refresh failed: ${error.message || error}`);
    setStatus('error', `Refresh failed: ${error.message || error}`);
  }
}

async function runRequest() {
  responseEl.textContent = '';
  setStatus('loading', 'Running request');
  try {
    const raw = await yggDemoRequest(JSON.stringify(requestConfig()));
    const result = JSON.parse(raw);
    responseEl.textContent = [
      `${result.status || ''}`.trim(),
      '',
      JSON.stringify(result.headers || {}, null, 2),
      '',
      result.body || '',
    ].join('\n');
    setStatus('success', `Request completed with ${result.statusCode}`);
    await refreshState();
  } catch (error) {
    responseEl.textContent = `Error: ${error.message || error}`;
    appendLog(`UI request failed: ${error.message || error}`);
    setStatus('error', `Request failed: ${error.message || error}`);
  }
}

document.getElementById('startNode').addEventListener('click', startNode);
document.getElementById('stopNode').addEventListener('click', stopNode);
document.getElementById('refreshState').addEventListener('click', refreshState);
document.getElementById('runRequest').addEventListener('click', runRequest);
syncButtons();
loadWasm();
