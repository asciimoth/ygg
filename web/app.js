let wasmReady = false;
let nodeRunning = false;
let ircConnected = false;

const statusEl = document.getElementById('runtimeStatus');
const responseEl = document.getElementById('response');

function randomIRCName(prefix) {
  const bytes = new Uint8Array(4);
  crypto.getRandomValues(bytes);
  const suffix = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  return `${prefix}${suffix}`;
}

function setRandomIRCIdentity() {
  const nick = randomIRCName('ygg');
  document.getElementById('ircNick').value = nick;
  document.getElementById('ircUsername').value = randomIRCName('web');
}

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

function appendIRC(kind, at, line) {
  const chat = document.getElementById('ircMessages');
  if (!chat) {
    return;
  }
  const atBottom = chat.scrollTop + chat.clientHeight >= chat.scrollHeight - 8;
  const row = document.createElement('div');
  row.className = `irc-line irc-${kind || 'in'}`;
  const time = document.createElement('span');
  time.className = 'irc-time';
  time.textContent = at || '';
  const text = document.createElement('span');
  text.textContent = line || '';
  row.append(time, text);
  chat.append(row);
  if (atBottom) {
    chat.scrollTop = chat.scrollHeight;
  }
}

window.yggDemoAppendIRC = appendIRC;

function setStatus(kind, text) {
  statusEl.className = `status ${kind}`;
  statusEl.textContent = text;
}

function syncButtons() {
  document.getElementById('startNode').disabled = !wasmReady;
  document.getElementById('stopNode').disabled = !wasmReady || !nodeRunning;
  document.getElementById('refreshState').disabled = !wasmReady || !nodeRunning;
  document.getElementById('runRequest').disabled = !wasmReady || !nodeRunning;
  document.getElementById('ircConnect').disabled = !wasmReady || !nodeRunning;
  document.getElementById('ircDisconnect').disabled = !wasmReady || !nodeRunning || !ircConnected;
  document.getElementById('ircSend').disabled = !wasmReady || !nodeRunning || !ircConnected;
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

function ircConnectConfig() {
  return {
    server: document.getElementById('ircServer').value.trim(),
    nick: document.getElementById('ircNick').value.trim(),
    username: document.getElementById('ircUsername').value.trim(),
    realname: document.getElementById('ircRealname').value.trim(),
    channel: document.getElementById('ircChannel').value.trim(),
    nickServMode: document.getElementById('nickServMode').value,
    nickServPassword: document.getElementById('nickServPassword').value,
    nickServEmail: document.getElementById('nickServEmail').value.trim(),
  };
}

function ircSendConfig() {
  return {
    target: document.getElementById('ircTarget').value.trim(),
    text: document.getElementById('ircText').value,
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
    ircConnected = false;
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

async function connectIRC() {
  setStatus('loading', 'Connecting IRC');
  syncButtons();
  try {
    await yggDemoIRCConnect(JSON.stringify(ircConnectConfig()));
    ircConnected = true;
    setStatus('success', 'IRC connected');
    await refreshState();
  } catch (error) {
    ircConnected = false;
    appendIRC('error', new Date().toLocaleTimeString(), `Error: ${error.message || error}`);
    setStatus('error', `IRC failed: ${error.message || error}`);
  }
  syncButtons();
}

async function disconnectIRC() {
  try {
    await yggDemoIRCDisconnect();
  } finally {
    ircConnected = false;
    setStatus('ready', nodeRunning ? 'Node running' : 'Stopped');
    syncButtons();
  }
}

async function sendIRC() {
  const text = document.getElementById('ircText');
  try {
    await yggDemoIRCSend(JSON.stringify(ircSendConfig()));
    text.value = '';
  } catch (error) {
    appendIRC('error', new Date().toLocaleTimeString(), `Error: ${error.message || error}`);
    setStatus('error', `IRC send failed: ${error.message || error}`);
  }
}

function syncNickServFields() {
  const mode = document.getElementById('nickServMode').value;
  document.getElementById('nickServPassword').disabled = mode === 'none';
  document.getElementById('nickServEmailLabel').classList.toggle('hidden', mode !== 'register');
}

setRandomIRCIdentity();
syncNickServFields();
document.getElementById('startNode').addEventListener('click', startNode);
document.getElementById('stopNode').addEventListener('click', stopNode);
document.getElementById('refreshState').addEventListener('click', refreshState);
document.getElementById('runRequest').addEventListener('click', runRequest);
document.getElementById('ircConnect').addEventListener('click', connectIRC);
document.getElementById('ircDisconnect').addEventListener('click', disconnectIRC);
document.getElementById('ircSend').addEventListener('click', sendIRC);
document.getElementById('nickServMode').addEventListener('change', syncNickServFields);
document.getElementById('ircText').addEventListener('keydown', (event) => {
  if (event.key === 'Enter' && !event.shiftKey) {
    event.preventDefault();
    if (!document.getElementById('ircSend').disabled) {
      sendIRC();
    }
  }
});
syncButtons();
loadWasm();
