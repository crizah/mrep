(() => {
  const params = new URLSearchParams(location.search);
  const token = params.get("t") || "";

  const feed = document.getElementById("feed");
  const statusEl = document.getElementById("status");
  const form = document.getElementById("composer");
  const textInput = document.getElementById("text");
  const fileInput = document.getElementById("file");

  const seen = new Set();
  let lastId = 0;
  let attempt = 0;
  let ws = null;

  function setStatus(text, cls) {
    statusEl.textContent = text;
    statusEl.className = "status " + (cls || "");
  }

  function connect() {
    const scheme = location.protocol === "https:" ? "wss" : "ws";
    const url = `${scheme}://${location.host}/ws?t=${encodeURIComponent(token)}&since=${lastId}`;
    ws = new WebSocket(url);
    ws.onopen = () => { attempt = 0; setStatus("live", "live"); };
    ws.onmessage = (e) => handleFrame(JSON.parse(e.data));
    ws.onclose = () => { setStatus("reconnecting…", "reconnecting"); scheduleReconnect(); };
    ws.onerror = () => ws.close();
  }

  function scheduleReconnect() {
    const delay = Math.min(1000 * 2 ** attempt++, 15000) + Math.random() * 500;
    setTimeout(connect, delay);
  }

  function handleFrame(frame) {
    if (frame.kind === "history") {
      for (const m of frame.messages) renderMessage(m);
    } else if (frame.kind === "msg") {
      renderMessage(frame.message);
    }
  }

  function renderMessage(m) {
    if (seen.has(m.id)) return;
    seen.add(m.id);
    if (m.id > lastId) lastId = m.id;

    const bubble = document.createElement("div");
    bubble.className = "bubble " + (m.sender === "phone" ? "phone" : "laptop");

    if (m.type === "text") {
      bubble.textContent = m.content;
    } else {
      const label = document.createElement("div");
      label.textContent = m.filename;
      bubble.appendChild(label);

      if (m.type === "image") {
        const img = document.createElement("img");
        img.src = m.content;
        img.alt = m.filename;
        bubble.appendChild(img);
      }

      const meta = document.createElement("div");
      meta.className = "meta";
      meta.textContent = `${m.mime} · ${humanSize(m.size)}`;
      bubble.appendChild(meta);

      bubble.appendChild(fileCard(m));
    }

    feed.appendChild(bubble);
    feed.scrollTop = feed.scrollHeight;
  }

  // fileCard gives every file — images included — a "Save to Files" download
  // link (HLD §11.1). Images additionally get native long-press "Save to
  // Photos" for free via the <img> tag above; the Share button here is a
  // progressive enhancement gated on a secure context (HLD §11.2).
  function fileCard(m) {
    const card = document.createElement("div");
    card.className = "filecard";

    const dl = document.createElement("a");
    dl.href = m.content + "?download=1";
    dl.textContent = "save to files";
    card.appendChild(dl);

    if (window.isSecureContext && navigator.canShare) {
      const shareBtn = document.createElement("button");
      shareBtn.type = "button";
      shareBtn.textContent = "share…";
      shareBtn.onclick = async () => {
        try {
          const resp = await fetch(m.content);
          const blob = await resp.blob();
          const file = new File([blob], m.filename, { type: m.mime });
          if (navigator.canShare({ files: [file] })) {
            await navigator.share({ files: [file] });
          }
        } catch (err) {
          console.error("share failed", err);
        }
      };
      card.appendChild(shareBtn);
    }

    return card;
  }

  function humanSize(n) {
    if (n < 1024) return `${n} B`;
    const units = ["KiB", "MiB", "GiB"];
    let i = -1;
    do { n /= 1024; i++; } while (n >= 1024 && i < units.length - 1);
    return `${n.toFixed(1)} ${units[i]}`;
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();

    const file = fileInput.files[0];
    if (file) {
      const body = new FormData();
      body.append("file", file);
      await fetch(`/upload?t=${encodeURIComponent(token)}`, { method: "POST", body });
      fileInput.value = "";
      return;
    }

    const text = textInput.value.trim();
    if (!text || !ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ kind: "send_text", content: text }));
    textInput.value = "";
  });

  connect();
})();
