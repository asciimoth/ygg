const statusEl = document.querySelector("#status");
const refreshButton = document.querySelector("#refresh");
const commandSelect = document.querySelector("#command");
const argumentsInput = document.querySelector("#arguments");
const output = document.querySelector("#output");

async function call(request, args = {}) {
  const response = await fetch("/.yggapi", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ request, arguments: args })
  });
  const payload = await response.json();
  if (payload.status !== "success") {
    throw new Error(payload.error || "admin request failed");
  }
  return payload.response;
}

function setText(id, value) {
  document.querySelector(id).textContent = value || "-";
}

function peerURI(peer) {
  return peer.uri || peer.remote || peer.address || peer.endpoint || peer.box_pub_key || "-";
}

function peerRows(peers) {
  const tbody = document.querySelector("#peer-rows");
  tbody.textContent = "";
  const values = Object.values(peers || {});
  if (values.length === 0) {
    const row = document.createElement("tr");
    const cell = document.createElement("td");
    cell.colSpan = 4;
    cell.textContent = "No peers";
    row.append(cell);
    tbody.append(row);
    return;
  }
  for (const peer of values) {
    const row = document.createElement("tr");
    for (const value of [
      peerURI(peer),
      peer.state || peer.uptime || "connected",
      peer.bytes_recvd || peer.bytes_received || peer.rx || "-",
      peer.bytes_sent || peer.tx || "-"
    ]) {
      const cell = document.createElement("td");
      cell.textContent = value;
      row.append(cell);
    }
    tbody.append(row);
  }
}

async function refresh() {
  statusEl.textContent = "Loading";
  statusEl.classList.remove("error");
  const [list, self, peers, sessions] = await Promise.all([
    call("list"),
    call("getSelf"),
    call("getPeers"),
    call("getSessions").catch(() => ({}))
  ]);

  setText("#address", self.address);
  setText("#subnet", self.subnet);
  const peerMap = peers.peers || peers.Peers || {};
  setText("#peers", String(Object.keys(peerMap).length));
  setText("#sessions", String(Object.keys(sessions.sessions || sessions.Sessions || {}).length));
  peerRows(peerMap);

  commandSelect.textContent = "";
  for (const entry of list.list || []) {
    const option = document.createElement("option");
    option.value = entry.command;
    option.textContent = entry.command;
    commandSelect.append(option);
  }
  statusEl.textContent = "Ready";
}

document.querySelector("#console").addEventListener("submit", async (event) => {
  event.preventDefault();
  output.classList.remove("error");
  try {
    const args = JSON.parse(argumentsInput.value || "{}");
    const response = await call(commandSelect.value, args);
    output.textContent = JSON.stringify(response, null, 2);
  } catch (err) {
    output.classList.add("error");
    output.textContent = err.message;
  }
});

refreshButton.addEventListener("click", () => {
  refresh().catch((err) => {
    statusEl.classList.add("error");
    statusEl.textContent = err.message;
  });
});

refresh().catch((err) => {
  statusEl.classList.add("error");
  statusEl.textContent = err.message;
});
