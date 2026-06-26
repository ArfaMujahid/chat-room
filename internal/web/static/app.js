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

  const els = {
    name: document.getElementById("name"),
    rooms: document.getElementById("rooms"),
    joinForm: document.getElementById("join-form"),
    roomInput: document.getElementById("room-input"),
    presence: document.getElementById("presence"),
    currentRoom: document.getElementById("current-room"),
    status: document.getElementById("status"),
    messages: document.getElementById("messages"),
    messageForm: document.getElementById("message-form"),
    messageInput: document.getElementById("message-input"),
    sendBtn: document.querySelector("#message-form button"),
  };

  const state = {
    ws: null,
    room: null,
    backoff: 500, // reconnect backoff in ms, capped and reset on open (NFR-U2).
  };

  // wsURL builds the ws:// or wss:// URL for the /ws endpoint on the current host.
  function wsURL() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}/ws`;
  }

  // setStatus reflects the live connection state in the header (NFR-U1).
  function setStatus(connected) {
    els.status.textContent = connected ? "connected" : "disconnected";
    els.status.className = connected ? "connected" : "disconnected";
    els.messageInput.disabled = !connected || !state.room;
    els.sendBtn.disabled = !connected || !state.room;
  }

  // connect opens the WebSocket and wires its lifecycle, auto-reconnecting with
  // exponential backoff when the connection drops (NFR-U2).
  function connect() {
    const ws = new WebSocket(wsURL());
    state.ws = ws;

    ws.addEventListener("open", () => {
      state.backoff = 500;
      setStatus(true);
      if (state.room) send({ type: Type.Join, room: state.room });
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
      setStatus(false);
      setTimeout(connect, state.backoff);
      state.backoff = Math.min(state.backoff * 2, 10000);
    });

    ws.addEventListener("error", () => ws.close());
  }

  // send serializes and sends an envelope if the socket is open.
  function send(envelope) {
    if (state.ws && state.ws.readyState === WebSocket.OPEN) {
      state.ws.send(JSON.stringify(envelope));
    }
  }

  // handleFrame dispatches an inbound envelope to the right renderer.
  function handleFrame(frame) {
    switch (frame.type) {
      case Type.Message:
        if (frame.message) renderMessage(frame.message);
        break;
      case Type.History:
        (frame.history || []).forEach(renderMessage);
        break;
      case Type.Presence:
        renderPresence(frame.presence);
        break;
      case Type.Error:
        renderSystem(`error: ${frame.error}`);
        break;
    }
  }

  // renderMessage appends one chat message to the list and keeps it scrolled.
  function renderMessage(m) {
    const li = document.createElement("li");
    const meta = document.createElement("span");
    meta.className = "meta";
    const when = m.created_at ? new Date(m.created_at).toLocaleTimeString() : "";
    meta.textContent = `${m.sender_name} · ${when}`;
    li.appendChild(meta);
    li.appendChild(document.createTextNode(m.content));
    els.messages.appendChild(li);
    els.messages.scrollTop = els.messages.scrollHeight;
  }

  // renderSystem appends an italic system line (joins, errors).
  function renderSystem(text) {
    const li = document.createElement("li");
    li.className = "system";
    li.textContent = text;
    els.messages.appendChild(li);
    els.messages.scrollTop = els.messages.scrollHeight;
  }

  // renderPresence updates the present-members list and notes the change.
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

  // joinRoom switches the active room and tells the server (FR-3).
  function joinRoom(room) {
    if (!room || room === state.room) return;
    if (state.room) send({ type: Type.Leave, room: state.room });
    state.room = room;
    els.currentRoom.textContent = room;
    els.messages.replaceChildren();
    send({ type: Type.Join, room });
    setStatus(state.ws && state.ws.readyState === WebSocket.OPEN);
  }

  els.joinForm.addEventListener("submit", (e) => {
    e.preventDefault();
    joinRoom(els.roomInput.value.trim());
    els.roomInput.value = "";
  });

  els.messageForm.addEventListener("submit", (e) => {
    e.preventDefault();
    const text = els.messageInput.value.trim();
    if (!text || !state.room) return;
    send({ type: Type.Message, room: state.room, text });
    els.messageInput.value = "";
  });

  connect();
})();
