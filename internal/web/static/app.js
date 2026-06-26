// app.js — vanilla WebSocket chat client: connect, send, render, auto-reconnect.
// Matches the message.Envelope protocol on the server. No framework, no build step.

(() => {
  "use strict";

  // Protocol frame types — keep in sync with internal/message/message.go.
  const Type = {
    Join: "join",
    Leave: "leave",
    Message: "message",
    History: "history",
    Presence: "presence",
    Error: "error",
  };

  // nameKey is the localStorage key that persists the display name across refreshes,
  // complementing the server's session cookie for identity continuity (FR-2).
  const nameKey = "chat_display_name";
  const maxBackoff = 10000; // cap on reconnect backoff, ms (NFR-U2).
  const roomsPoll = 3000; // how often to refresh the room list, ms.

  const els = {
    name: document.getElementById("name"),
    rooms: document.getElementById("rooms"),
    joinForm: document.getElementById("join-form"),
    roomInput: document.getElementById("room-input"),
    presence: document.getElementById("presence"),
    currentRoom: document.getElementById("current-room"),
    status: document.getElementById("status"),
    online: document.getElementById("online"),
    messages: document.getElementById("messages"),
    messageForm: document.getElementById("message-form"),
    messageInput: document.getElementById("message-input"),
    sendBtn: document.querySelector("#message-form button"),
  };

  const state = {
    ws: null,
    room: null,
    name: "",
    backoff: 500, // current reconnect delay, ms; reset to this on a clean open.
    rooms: [], // latest room list from /api/rooms, for re-render on room switch.
  };

  // connected reports whether the socket is open and usable.
  function connected() {
    return state.ws && state.ws.readyState === WebSocket.OPEN;
  }

  // wsURL builds the ws(s):// URL for /ws, carrying the chosen display name so the
  // server attributes messages correctly (FR-2).
  function wsURL() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}/ws?name=${encodeURIComponent(state.name)}`;
  }

  // setStatus reflects the live connection state in the header and enables the
  // composer only when we can actually send (connected and in a room) (NFR-U1).
  function setStatus(isConnected) {
    els.status.textContent = isConnected ? "connected" : "disconnected";
    els.status.className = isConnected ? "connected" : "disconnected";
    const canSend = isConnected && Boolean(state.room);
    els.messageInput.disabled = !canSend;
    els.sendBtn.disabled = !canSend;
  }

  // connect opens the WebSocket and wires its lifecycle, auto-reconnecting with
  // exponential backoff when the connection drops (NFR-U2). It is a no-op until a
  // display name has been chosen.
  function connect() {
    if (!state.name) {
      renderSystem("enter a display name to start chatting");
      return;
    }
    const ws = new WebSocket(wsURL());
    state.ws = ws;

    ws.addEventListener("open", () => {
      state.backoff = 500;
      setStatus(true);
      // Re-join the current room after a reconnect, clearing first so the replayed
      // history does not stack on top of what is already shown.
      if (state.room) {
        els.messages.replaceChildren();
        send({ type: Type.Join, room: state.room });
      }
    });

    ws.addEventListener("message", (ev) => {
      let frame;
      try {
        frame = JSON.parse(ev.data);
      } catch {
        return;
      }
      handleFrame(frame);
    });

    ws.addEventListener("close", () => {
      state.ws = null;
      setStatus(false);
      setTimeout(connect, state.backoff);
      state.backoff = Math.min(state.backoff * 2, maxBackoff);
    });

    ws.addEventListener("error", () => ws.close());
  }

  // send serializes and sends an envelope if the socket is open.
  function send(envelope) {
    if (connected()) {
      state.ws.send(JSON.stringify(envelope));
    }
  }

  // handleFrame dispatches an inbound envelope to the right renderer.
  function handleFrame(frame) {
    switch (frame.type) {
      case Type.Message:
        if (frame.message) renderMessage(frame.message, false);
        break;
      case Type.History:
        (frame.history || []).forEach((m) => renderMessage(m, false));
        break;
      case Type.Presence:
        renderPresence(frame.presence);
        break;
      case Type.Error:
        renderSystem(`error: ${frame.error}`);
        break;
    }
  }

  // renderMessage appends one chat message bubble and keeps the list scrolled to the
  // bottom. mine aligns the bubble to the right for the local user's own messages.
  function renderMessage(m, mine) {
    const li = document.createElement("li");
    li.className = mine ? "mine" : "other";

    const meta = document.createElement("span");
    meta.className = "meta";
    const when = m.created_at ? new Date(m.created_at).toLocaleTimeString() : "";
    meta.textContent = mine ? `you · ${when}` : `${m.sender_name} · ${when}`;

    const body = document.createElement("span");
    body.className = "body";
    body.textContent = m.content;

    li.append(meta, body);
    els.messages.appendChild(li);
    els.messages.scrollTop = els.messages.scrollHeight;
  }

  // renderSystem appends a centered, italic system line (joins, leaves, errors).
  function renderSystem(text) {
    const li = document.createElement("li");
    li.className = "system";
    li.textContent = text;
    els.messages.appendChild(li);
    els.messages.scrollTop = els.messages.scrollHeight;
  }

  // renderPresence notes the join/leave and redraws the present-members list (FR-9).
  function renderPresence(p) {
    if (!p) return;
    renderSystem(`${p.user} ${p.joined ? "joined" : "left"}`);
    els.presence.replaceChildren();
    (p.members || []).forEach((name) => {
      const li = document.createElement("li");
      li.textContent = name;
      els.presence.appendChild(li);
    });
  }

  // renderRooms draws the clickable room list with member counts, highlighting the
  // active room (FR-10). Clicking a room joins it.
  function renderRooms(rooms) {
    state.rooms = rooms;
    els.rooms.replaceChildren();
    rooms
      .slice()
      .sort((a, b) => a.name.localeCompare(b.name))
      .forEach((r) => {
        const li = document.createElement("li");
        li.className = r.name === state.room ? "active" : "";

        const name = document.createElement("span");
        name.className = "room-name";
        name.textContent = r.name;

        const count = document.createElement("span");
        count.className = "room-count";
        count.textContent = String(r.members);

        li.append(name, count);
        li.addEventListener("click", () => joinRoom(r.name));
        els.rooms.appendChild(li);
      });
  }

  // refreshRooms polls the REST endpoint for the active rooms and connection count
  // and updates the sidebar and header (FR-10, NFR-O1). Network errors are ignored;
  // the next tick retries.
  async function refreshRooms() {
    try {
      const res = await fetch("/api/rooms");
      if (!res.ok) return;
      const stats = await res.json();
      renderRooms(stats.rooms || []);
      const n = stats.connections || 0;
      els.online.textContent = `${n} online`;
    } catch {
      // transient — retried on the next interval.
    }
  }

  // joinRoom switches the active room: it leaves the previous one, clears the view,
  // and asks the server to join the new one (FR-3/FR-4). The server replies with
  // recent history and a presence update.
  function joinRoom(room) {
    if (!room || room === state.room) return;
    if (!state.name) {
      renderSystem("enter a display name first");
      els.name.focus();
      return;
    }
    if (state.room) send({ type: Type.Leave, room: state.room });
    state.room = room;
    els.currentRoom.textContent = room;
    els.messages.replaceChildren();
    els.presence.replaceChildren();
    send({ type: Type.Join, room });
    renderRooms(state.rooms); // refresh active highlight immediately
    setStatus(connected());
  }

  // applyName persists a new display name and (re)connects under it. Because the name
  // is carried on the WebSocket handshake, changing it requires a reconnect.
  function applyName(raw) {
    const name = raw.trim();
    if (!name || name === state.name) return;
    state.name = name;
    localStorage.setItem(nameKey, name);
    if (state.ws) {
      state.ws.close(); // close handler reconnects with the new name
    } else {
      connect();
    }
  }

  els.name.addEventListener("change", () => applyName(els.name.value));

  els.joinForm.addEventListener("submit", (e) => {
    e.preventDefault();
    joinRoom(els.roomInput.value.trim());
    els.roomInput.value = "";
  });

  els.messageForm.addEventListener("submit", (e) => {
    e.preventDefault();
    const text = els.messageInput.value.trim();
    if (!text || !state.room || !connected()) return;
    send({ type: Type.Message, room: state.room, text });
    // The server does not echo a message back to its sender, so render it locally
    // for instant feedback (optimistic echo).
    renderMessage({ sender_name: state.name, content: text, created_at: new Date().toISOString() }, true);
    els.messageInput.value = "";
  });

  // Bootstrap: restore any saved name, start the connection, and begin polling rooms.
  state.name = localStorage.getItem(nameKey) || "";
  els.name.value = state.name;
  connect();
  refreshRooms();
  setInterval(refreshRooms, roomsPoll);
})();
