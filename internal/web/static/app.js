// app.js — vanilla chat client with username/password auth: log in / register, then
// connect over a WebSocket, send, render, and auto-reconnect. No framework, no build.

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

  const maxBackoff = 10000; // cap on reconnect backoff, ms (NFR-U2).
  const roomsPoll = 3000; // how often to refresh the room list, ms.

  const els = {
    // Auth screen.
    auth: document.getElementById("auth"),
    tabLogin: document.getElementById("tab-login"),
    tabRegister: document.getElementById("tab-register"),
    authForm: document.getElementById("auth-form"),
    authUsername: document.getElementById("auth-username"),
    authDisplay: document.getElementById("auth-display"),
    authPassword: document.getElementById("auth-password"),
    authSubmit: document.getElementById("auth-submit"),
    authError: document.getElementById("auth-error"),
    // Chat app.
    app: document.getElementById("app"),
    accountAvatar: document.getElementById("account-avatar"),
    accountName: document.getElementById("account-name"),
    logout: document.getElementById("logout"),
    rooms: document.getElementById("rooms"),
    joinForm: document.getElementById("join-form"),
    roomInput: document.getElementById("room-input"),
    presence: document.getElementById("presence"),
    currentRoom: document.getElementById("current-room"),
    status: document.getElementById("status"),
    statusText: document.getElementById("status-text"),
    online: document.getElementById("online"),
    rate: document.getElementById("rate"),
    messages: document.getElementById("messages"),
    emptyState: document.getElementById("empty-state"),
    messageForm: document.getElementById("message-form"),
    messageInput: document.getElementById("message-input"),
    sendBtn: document.querySelector("#message-form button"),
  };

  const state = {
    user: null, // {username, display_name} when logged in, else null.
    mode: "login", // "login" | "register"
    ws: null,
    room: null,
    backoff: 500,
    rooms: [],
    roomsTimer: null,
  };

  // connected reports whether the socket is open and usable.
  function connected() {
    return state.ws && state.ws.readyState === WebSocket.OPEN;
  }

  // --- avatars ----------------------------------------------------------------

  // initials returns the first character of a name, uppercased, for an avatar.
  function initials(name) {
    return (name || "?").trim().charAt(0).toUpperCase() || "?";
  }

  // avatarColor derives a stable HSL colour from a name, so each user has a
  // consistent avatar tint across the UI.
  function avatarColor(name) {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
    return `hsl(${h % 360} 52% 52%)`;
  }

  // avatarEl builds a coloured initial avatar element.
  function avatarEl(name, small) {
    const a = document.createElement("span");
    a.className = small ? "avatar sm" : "avatar";
    a.style.background = avatarColor(name);
    a.textContent = initials(name);
    return a;
  }

  // --- authentication ---------------------------------------------------------

  // checkSession asks the server who we are; it shows the chat if a session exists,
  // otherwise the login screen.
  async function checkSession() {
    try {
      const res = await fetch("/api/me");
      if (res.ok) {
        const data = await res.json();
        if (data.authenticated) {
          state.user = data;
          enterApp();
          return;
        }
      }
    } catch {
      // fall through to the login screen
    }
    showAuth();
  }

  // setMode switches the auth form between login and register.
  function setMode(mode) {
    state.mode = mode;
    els.tabLogin.classList.toggle("active", mode === "login");
    els.tabRegister.classList.toggle("active", mode === "register");
    els.authDisplay.hidden = mode !== "register";
    els.authSubmit.textContent = mode === "login" ? "Log in" : "Create account";
    els.authPassword.autocomplete = mode === "login" ? "current-password" : "new-password";
    els.authError.textContent = "";
  }

  // submitAuth logs in or registers, then enters the app on success or shows the
  // server's error message on failure.
  async function submitAuth(e) {
    e.preventDefault();
    els.authError.textContent = "";
    const body = {
      username: els.authUsername.value.trim(),
      password: els.authPassword.value,
    };
    if (state.mode === "register") body.display_name = els.authDisplay.value.trim();

    try {
      const res = await fetch(state.mode === "login" ? "/api/login" : "/api/register", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        els.authError.textContent = (await res.text()).trim() || "could not authenticate";
        return;
      }
      state.user = await res.json();
      els.authPassword.value = "";
      enterApp();
    } catch {
      els.authError.textContent = "network error";
    }
  }

  // logout ends the session server-side and returns to the login screen.
  async function logout() {
    try {
      await fetch("/api/logout", { method: "POST" });
    } catch {
      // ignore — we tear down locally regardless
    }
    teardownApp();
    showAuth();
  }

  function showAuth() {
    els.app.hidden = true;
    els.auth.hidden = false;
    els.authUsername.focus();
  }

  // enterApp reveals the chat, sets the account avatar, opens the WebSocket, and
  // starts polling rooms.
  function enterApp() {
    els.auth.hidden = true;
    els.app.hidden = false;
    els.accountName.textContent = state.user.display_name;
    els.accountAvatar.style.background = avatarColor(state.user.display_name);
    els.accountAvatar.textContent = initials(state.user.display_name);
    updateRoomView();
    connect();
    refreshRooms();
    state.roomsTimer = setInterval(refreshRooms, roomsPoll);
  }

  // teardownApp closes the socket and resets all chat state (on logout). Clearing the
  // user first stops the close handler from reconnecting.
  function teardownApp() {
    state.user = null;
    state.room = null;
    state.rooms = [];
    if (state.roomsTimer) {
      clearInterval(state.roomsTimer);
      state.roomsTimer = null;
    }
    if (state.ws) {
      state.ws.close();
      state.ws = null;
    }
    els.messages.replaceChildren();
    els.presence.replaceChildren();
    els.rooms.replaceChildren();
    els.currentRoom.textContent = "No room";
  }

  // updateRoomView shows the message list + composer when in a room, or the empty
  // state otherwise.
  function updateRoomView() {
    const inRoom = Boolean(state.room);
    els.messages.hidden = !inRoom;
    els.emptyState.hidden = inRoom;
    els.messageForm.hidden = !inRoom;
  }

  // --- websocket --------------------------------------------------------------

  // wsURL builds the ws(s):// URL for /ws. Identity comes from the session cookie, so
  // no name is passed here.
  function wsURL() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    return `${proto}//${location.host}/ws`;
  }

  // setStatus reflects the connection state in the header and enables the composer
  // only when we can actually send (connected and in a room).
  function setStatus(isConnected) {
    els.status.className = "status " + (isConnected ? "connected" : "disconnected");
    els.statusText.textContent = isConnected ? "online" : "offline";
    const canSend = isConnected && Boolean(state.room);
    els.messageInput.disabled = !canSend;
    els.sendBtn.disabled = !canSend;
  }

  // connect opens the WebSocket and auto-reconnects with backoff while logged in
  // (NFR-U2). It is a no-op once logged out.
  function connect() {
    if (!state.user) return;
    const ws = new WebSocket(wsURL());
    state.ws = ws;

    ws.addEventListener("open", () => {
      state.backoff = 500;
      setStatus(true);
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
      if (state.user) {
        setTimeout(connect, state.backoff);
        state.backoff = Math.min(state.backoff * 2, maxBackoff);
      }
    });

    ws.addEventListener("error", () => ws.close());
  }

  // send serializes and sends an envelope if the socket is open.
  function send(envelope) {
    if (connected()) state.ws.send(JSON.stringify(envelope));
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

  // formatTime renders a short HH:MM timestamp.
  function formatTime(iso) {
    return iso ? new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "";
  }

  // renderMessage appends one chat bubble; mine aligns the local user's own messages
  // to the right (and omits the avatar/sender, since it's you).
  function renderMessage(m, mine) {
    const li = document.createElement("li");
    li.className = "msg " + (mine ? "mine" : "other");

    if (!mine) li.appendChild(avatarEl(m.sender_name, false));

    const bubble = document.createElement("div");
    bubble.className = "bubble";

    const meta = document.createElement("div");
    meta.className = "bubble-meta";
    if (!mine) {
      const sender = document.createElement("span");
      sender.className = "sender";
      sender.textContent = m.sender_name;
      sender.style.color = avatarColor(m.sender_name);
      meta.appendChild(sender);
    }
    const time = document.createElement("span");
    time.className = "time";
    time.textContent = formatTime(m.created_at);
    meta.appendChild(time);

    const body = document.createElement("div");
    body.className = "bubble-body";
    body.textContent = m.content;

    bubble.append(meta, body);
    li.appendChild(bubble);
    els.messages.appendChild(li);
    els.messages.scrollTop = els.messages.scrollHeight;
  }

  // renderSystem appends a centered system pill (joins, leaves, errors).
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
      li.append(avatarEl(name, true), document.createTextNode(name));
      els.presence.appendChild(li);
    });
  }

  // renderRooms draws the clickable room list with member counts, highlighting the
  // active room (FR-10).
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

  // refreshRooms polls the REST endpoint for active rooms, the connection count, and
  // the message rate (FR-10, NFR-O1). Errors are ignored; the next tick retries.
  async function refreshRooms() {
    try {
      const res = await fetch("/api/rooms");
      if (!res.ok) return;
      const stats = await res.json();
      renderRooms(stats.rooms || []);
      els.online.textContent = `${stats.connections || 0} online`;
      els.rate.textContent = `${stats.message_rate || 0}/s`;
    } catch {
      // transient
    }
  }

  // joinRoom switches the active room: leave the previous one, clear the view, and
  // join the new one (FR-3/FR-4).
  function joinRoom(room) {
    if (!room || room === state.room) return;
    if (state.room) send({ type: Type.Leave, room: state.room });
    state.room = room;
    els.currentRoom.textContent = room;
    els.messages.replaceChildren();
    els.presence.replaceChildren();
    updateRoomView();
    send({ type: Type.Join, room });
    renderRooms(state.rooms);
    setStatus(connected());
  }

  // --- event wiring -----------------------------------------------------------

  els.tabLogin.addEventListener("click", () => setMode("login"));
  els.tabRegister.addEventListener("click", () => setMode("register"));
  els.authForm.addEventListener("submit", submitAuth);
  els.logout.addEventListener("click", logout);

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
    // The server does not echo a message to its sender, so render it locally.
    renderMessage({ content: text, created_at: new Date().toISOString() }, true);
    els.messageInput.value = "";
  });

  // Bootstrap: pick the default auth mode and find out if we are already logged in.
  setMode("login");
  checkSession();
})();
