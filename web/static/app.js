// Voice Notes test client. No framework, no build step.
// All server/model data is rendered with textContent (never innerHTML),
// because transcripts are user speech and model output is untrusted.
"use strict";

const $ = (id) => document.getElementById(id);
const KEY_STORAGE = "voice-notes-api-key";

// Stroke icons (24x24 viewBox), drawn inline: the CSP allows no external assets.
const ICON = {
  email: "M4 6h16v12H4z M4 7l8 6 8-6",
  call: "M5 4h4l2 5-2.5 1.5a11 11 0 0 0 5 5L15 13l5 2v4a2 2 0 0 1-2 2A16 16 0 0 1 3 6a2 2 0 0 1 2-2",
  meeting: "M4 6h16v14H4z M4 10h16 M8 3v4 M16 3v4",
  document: "M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8l-5-5Z M14 3v5h5 M9 13h6 M9 17h4",
  todo: "M5 4h14v16H5z M9 12l2 2 4-4",
  opportunity: "M3 17l6-6 4 4 8-8 M15 7h6v6",
  risk: "M12 3l10 18H2z M12 10v4 M12 17.5v.01",
  competitor: "M5 21V4 M5 4h12l-2.5 4L17 12H5",
  product_feedback: "M4 5h16v11H9l-5 4z M8 9h8 M8 12h5",
  complaint: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z M8.5 16.5a4.5 4.5 0 0 1 7 0 M9 10h.01 M15 10h.01",
  crm: "M16 19v-1a4 4 0 0 0-4-4H7a4 4 0 0 0-4 4v1 M9.5 10a3 3 0 1 0 0-6 3 3 0 0 0 0 6 M21 19v-1a4 4 0 0 0-3-3.9 M16 4.1a3 3 0 0 1 0 5.8",
  notes: "M6 3h11a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6z M6 3v18 M10 8h5 M10 12h5",
  link: "M10 14a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1 1 M14 10a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1-1",
  terminal: "M4 17l6-5-6-5 M12 19h8",
  table: "M4 5h16v14H4z M4 10h16 M4 15h16 M10 5v14",
};

const TASK_TYPES = {
  email: { label: "Email", icon: ICON.email },
  call: { label: "Call", icon: ICON.call },
  meeting: { label: "Meeting", icon: ICON.meeting },
  document: { label: "Send document", icon: ICON.document },
  todo: { label: "To-do", icon: ICON.todo },
};
const PRIORITY = { high: { label: "High", rank: 0 }, medium: { label: "Medium", rank: 1 }, low: { label: "Low", rank: 2 } };
const INSIGHT_TYPES = {
  opportunity: { label: "Opportunity", icon: ICON.opportunity },
  risk: { label: "Risk", icon: ICON.risk },
  competitor: { label: "Competitor", icon: ICON.competitor },
  product_feedback: { label: "Product feedback", icon: ICON.product_feedback },
  complaint: { label: "Complaint", icon: ICON.complaint },
};
// Destinations get a generic icon by kind, tinted per product. No vendor logos.
const SINKS = {
  hubspot: { label: "HubSpot", kind: "CRM", icon: ICON.crm },
  notion: { label: "Notion", kind: "Notes", icon: ICON.notes },
  sheets: { label: "Google Sheets", kind: "Spreadsheet", icon: ICON.table },
  webhook: { label: "Webhook", kind: "Any API", icon: ICON.link },
  console: { label: "Console", kind: "Server log", icon: ICON.terminal },
};
const sinkInfo = (name) => SINKS[name] || { label: name, kind: "", icon: ICON.link };

const state = { blob: null, name: "", maxMB: 20, recorder: null, timerId: null, elapsedId: null, meter: null };

init();

async function init() {
  try {
    const meta = await (await fetch("/meta")).json();
    state.maxMB = meta.max_upload_mb || state.maxMB;
    const chips = meta.sinks.map((name) => {
      const chip = el("span", null, `chip ${name in SINKS ? `sink-${name}` : "sink-other"}`);
      chip.append(svg(sinkInfo(name).icon), el("span", sinkInfo(name).label));
      return chip;
    });
    if (!chips.length) { // SINKS=none: the demo analyses but stores nothing
      const chip = el("span", null, "chip sink-other");
      chip.append(svg(ICON.document), el("span", "Analysis only"));
      chip.title = "This server analyses memos but doesn't deliver or store them";
      chips.push(chip);
    }
    $("destinations").replaceChildren(...chips);
    renderPicker(meta.sinks);
    $("dz-hint").textContent = `WAV, MP3, M4A, AAC, OGG, FLAC or WebM · up to ${state.maxMB} MB`;
    if (meta.auth_required) {
      $("api-key-row").hidden = false;
      $("api-key").value = sessionStorage.getItem(KEY_STORAGE) || "";
    }
  } catch {
    const chip = el("span", null, "chip sink-other");
    chip.append(svg(ICON.link), el("span", "Unavailable"));
    chip.title = "Could not load the server configuration";
    $("destinations").replaceChildren(chip);
  }

  $("record").addEventListener("click", toggleRecording);
  $("file").addEventListener("change", (e) => e.target.files[0] && pickFile(e.target.files[0]));
  $("clear").addEventListener("click", clearAudio);
  $("submit").addEventListener("click", submit);
  $("retry").addEventListener("click", submit);
  $("copy-raw").addEventListener("click", () => copy($("r-raw").textContent, $("copy-raw")));
  $("copy-transcript").addEventListener("click", () => copy($("r-transcript").textContent, $("copy-transcript")));
  $("api-key").addEventListener("change", (e) => sessionStorage.setItem(KEY_STORAGE, e.target.value));
  $("api-key-row").addEventListener("submit", (e) => e.preventDefault()); // Enter must not reload the page

  const dz = $("dropzone");
  dz.addEventListener("dragover", (e) => { e.preventDefault(); dz.classList.add("dragover"); });
  dz.addEventListener("dragleave", () => dz.classList.remove("dragover"));
  dz.addEventListener("drop", (e) => {
    e.preventDefault();
    dz.classList.remove("dragover");
    const f = e.dataTransfer.files[0];
    if (f) pickFile(f);
  });
}

// ---------- capture ----------

function pickFile(file) {
  const hint = $("dz-hint");
  hint.classList.remove("error");
  if (file.size > state.maxMB * 1024 * 1024) {
    hint.textContent = `That file is ${mb(file.size)}. The limit is ${state.maxMB} MB.`;
    hint.classList.add("error");
    return;
  }
  setAudio(file, file.name);
}

async function toggleRecording() {
  if (state.recorder && state.recorder.state === "recording") {
    state.recorder.stop();
    return;
  }
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({ audio: true });
  } catch {
    setStatus("Microphone access was blocked. Allow it in the browser's address bar and try again.", true);
    return;
  }
  setStatus("");
  const chunks = [];
  let recorder;
  try {
    recorder = new MediaRecorder(stream);
  } catch {
    stream.getTracks().forEach((t) => t.stop());
    setStatus("This browser can't record audio. Upload a file instead.", true);
    return;
  }
  state.recorder = recorder;
  recorder.ondataavailable = (e) => e.data.size && chunks.push(e.data);
  recorder.onstop = async () => {
    stream.getTracks().forEach((t) => t.stop());
    stopMeter();
    clearInterval(state.timerId);
    $("recorder").classList.remove("recording");
    $("record").setAttribute("aria-label", "Start recording");
    $("rec-state").textContent = "Processing…";
    const raw = new Blob(chunks, { type: recorder.mimeType });
    const name = `Recording ${new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}.wav`;
    try {
      setAudio(await toWav16kMono(raw), name);
    } catch {
      setAudio(raw, name.replace(".wav", "")); // fall back to the browser's native format
    }
    $("rec-state").textContent = "Record again";
  };
  recorder.start();
  $("recorder").classList.add("recording");
  $("record").setAttribute("aria-label", "Stop recording");
  $("rec-state").textContent = "Recording…";
  startTimer();
  startMeter(stream);
}

function startTimer() {
  const start = Date.now();
  const tick = () => ($("timer").textContent = clock((Date.now() - start) / 1000));
  tick();
  state.timerId = setInterval(tick, 250);
}

// Live level meter: draws bars from an AnalyserNode on the mic stream.
function startMeter(stream) {
  const canvas = $("meter");
  const ctx2d = canvas.getContext("2d");
  const audioCtx = new AudioContext();
  const analyser = audioCtx.createAnalyser();
  analyser.fftSize = 256;
  audioCtx.createMediaStreamSource(stream).connect(analyser);
  const data = new Uint8Array(analyser.frequencyBinCount);
  const color = getComputedStyle(document.documentElement).getPropertyValue("--danger").trim() || "#dc2626";
  const ratio = window.devicePixelRatio || 1;
  canvas.width = canvas.clientWidth * ratio;
  canvas.height = canvas.clientHeight * ratio;

  let raf;
  const draw = () => {
    analyser.getByteFrequencyData(data);
    const { width, height } = canvas;
    ctx2d.clearRect(0, 0, width, height);
    ctx2d.fillStyle = color;
    const bars = 28;
    const gap = 3 * ratio;
    const bw = (width - gap * (bars - 1)) / bars;
    for (let i = 0; i < bars; i++) {
      const v = data[Math.floor((i / bars) * data.length * 0.7)] / 255;
      const h = Math.max(3 * ratio, v * height);
      ctx2d.fillRect(i * (bw + gap), (height - h) / 2, bw, h);
    }
    raf = requestAnimationFrame(draw);
  };
  draw();
  state.meter = { stop: () => { cancelAnimationFrame(raf); audioCtx.close(); ctx2d.clearRect(0, 0, canvas.width, canvas.height); } };
}

function stopMeter() {
  if (state.meter) state.meter.stop();
  state.meter = null;
}

// Browsers record WebM/Opus (Chrome, Firefox) or MP4 (Safari). We convert to
// 16 kHz mono WAV: a format Gemini documents, and plenty for speech
// (~32 KB/s, so a 5-minute memo is ~10 MB).
async function toWav16kMono(blob) {
  const ctx = new AudioContext();
  const decoded = await ctx.decodeAudioData(await blob.arrayBuffer());
  ctx.close();
  const rate = 16000;
  const offline = new OfflineAudioContext(1, Math.ceil(decoded.duration * rate), rate);
  const src = offline.createBufferSource();
  src.buffer = decoded;
  src.connect(offline.destination);
  src.start();
  const pcm = (await offline.startRendering()).getChannelData(0);

  const buf = new ArrayBuffer(44 + pcm.length * 2);
  const v = new DataView(buf);
  const str = (o, s) => [...s].forEach((c, i) => v.setUint8(o + i, c.charCodeAt(0)));
  str(0, "RIFF"); v.setUint32(4, 36 + pcm.length * 2, true); str(8, "WAVE");
  str(12, "fmt "); v.setUint32(16, 16, true); v.setUint16(20, 1, true); v.setUint16(22, 1, true);
  v.setUint32(24, rate, true); v.setUint32(28, rate * 2, true); v.setUint16(32, 2, true); v.setUint16(34, 16, true);
  str(36, "data"); v.setUint32(40, pcm.length * 2, true);
  for (let i = 0; i < pcm.length; i++) {
    const s = Math.max(-1, Math.min(1, pcm[i]));
    v.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return new Blob([buf], { type: "audio/wav" });
}

function setAudio(blob, name) {
  state.blob = blob;
  state.name = name;
  const p = $("preview");
  if (p.src) URL.revokeObjectURL(p.src);
  p.src = URL.createObjectURL(blob);
  $("file-name").textContent = name;
  $("file-info").textContent = mb(blob.size);
  p.onloadedmetadata = () => {
    if (Number.isFinite(p.duration)) $("file-info").textContent = `${clock(p.duration)} · ${mb(blob.size)}`;
  };
  $("file-card").hidden = false;
  $("dropzone").hidden = true;
  $("submit").disabled = false;
  $("timer").textContent = "0:00";
  setStatus("");
}

function clearAudio() {
  state.blob = null;
  const p = $("preview");
  if (p.src) URL.revokeObjectURL(p.src);
  p.removeAttribute("src");
  $("file").value = "";
  $("file-card").hidden = true;
  $("dropzone").hidden = false;
  $("submit").disabled = true;
  $("rec-state").textContent = "Tap to record";
  $("timer").textContent = "0:00";
}

// ---------- submit ----------

async function submit() {
  if (!state.blob) return;
  const form = new FormData();
  form.append("audio", state.blob, state.name);
  if (!$("dest-picker").hidden) form.append("sinks", selectedSinks().join(",") || "none");

  const headers = {};
  const key = $("api-key").value.trim();
  if (key) headers["Authorization"] = `Bearer ${key}`;

  setBusy(true);
  setStatus("");
  showPanel("loading");
  const started = Date.now();
  state.elapsedId = setInterval(() => ($("elapsed").textContent = `${Math.floor((Date.now() - started) / 1000)}s`), 500);

  try {
    const res = await fetch("/v1/notes", { method: "POST", body: form, headers });
    const body = await res.json().catch(() => ({}));
    const secs = (Date.now() - started) / 1000;

    if (body.note) {
      render(body, secs);
      showPanel("result");
    } else {
      showPanel("empty");
    }
    if (res.status !== 201) {
      // The server sends a user-facing message, a stable code and whether a
      // retry makes sense; fall back to a generic text for unexpected bodies.
      setStatus(body.error || `Request failed (HTTP ${res.status}).`, true, {
        retryable: Boolean(body.retryable),
        ref: [body.code, `HTTP ${res.status}`, body.request_id && `request ${body.request_id}`].filter(Boolean).join(" · "),
      });
    }
  } catch {
    showPanel("empty");
    setStatus("Could not reach the server. Is it running?", true, { retryable: true });
  } finally {
    clearInterval(state.elapsedId);
    $("elapsed").textContent = "0s";
    setBusy(false);
  }
}

function setBusy(busy) {
  $("submit").disabled = busy || !state.blob;
  $("retry").disabled = busy;
  $("record").disabled = busy;
  $("submit").firstElementChild.textContent = busy ? "Analyzing…" : "Analyze memo";
}

function showPanel(which) {
  $("empty").hidden = which !== "empty";
  $("loading").hidden = which !== "loading";
  $("result").hidden = which !== "result";
}

// ---------- render ----------

function render(body, secs) {
  const n = body.note;
  $("r-title").textContent = n.title || "Untitled memo";

  const pill = $("r-sentiment");
  pill.textContent = n.sentiment || "neutral";
  pill.className = `pill ${n.sentiment || "neutral"}`;

  const meta = [languageName(n.language), `Processed in ${secs.toFixed(1)}s`].filter(Boolean).map((t) => el("span", t));
  if (body.request_id) {
    const id = el("button", `Request ${body.request_id}`, "copy-id mono");
    id.type = "button";
    id.title = "Copy request ID";
    id.addEventListener("click", () => copy(body.request_id, id));
    meta.push(id);
  }
  $("r-meta").replaceChildren(...meta);

  $("r-summary").textContent = n.summary;
  renderContact(n.contact || {});
  renderSteps(n.next_steps || []);
  renderInsights(n.insights || []);

  const tags = n.tags || [];
  $("r-tags-block").hidden = tags.length === 0;
  $("r-tags").replaceChildren(...tags.map((t) => el("span", t, "tag")));

  renderDeliveries(body.deliveries || []);
  const transcript = $("r-transcript");
  transcript.textContent = n.transcript;
  transcript.lang = n.language || ""; // correct hyphenation and screen-reader voice
  transcript.scrollTop = 0;
  const words = (n.transcript || "").split(/\s+/).filter(Boolean).length;
  $("r-words").textContent = words ? `${words} words` : "";
  $("r-raw").textContent = JSON.stringify(body, null, 2);
}

function renderContact(c) {
  const box = $("r-contact");
  if (!c.name && !c.company && !c.email) {
    box.replaceChildren(el("div", "No contact mentioned in this memo.", "none"));
    return;
  }
  const card = el("div", null, "contact");
  const initials = (c.name || c.company || "?").split(/\s+/).filter(Boolean).slice(0, 2).map((w) => w[0].toUpperCase()).join("");
  const info = el("div");
  info.append(el("div", c.name || "Unknown name", "contact-name"));
  if (c.company) info.append(el("div", c.company, "contact-line"));
  if (c.email) {
    const line = el("div", null, "contact-line");
    if (/^[^\s@<>"]+@[^\s@<>"]+\.[^\s@<>"]+$/.test(c.email)) {
      const a = el("a", c.email);
      a.href = `mailto:${c.email}`;
      line.append(a);
    } else {
      line.textContent = c.email;
    }
    info.append(line);
  }
  card.append(el("div", initials, "avatar"), info);
  box.replaceChildren(card);
}

// ---------- destination picker ----------

// renderPicker shows one toggle per configured destination, all on by default.
// The server validates the selection again; this is only a convenience.
function renderPicker(names) {
  $("dest-picker").hidden = names.length === 0;
  $("dest-options").replaceChildren(...names.map((name) => {
    const info = sinkInfo(name);
    const label = el("label", null, `dest-option ${name in SINKS ? `sink-${name}` : "sink-other"}`);
    const input = el("input");
    input.type = "checkbox";
    input.value = name;
    input.checked = true;
    input.addEventListener("change", () => ($("dest-hint").hidden = selectedSinks().length > 0));
    label.append(input, svg(info.icon), el("span", info.label));
    return label;
  }));
}

function selectedSinks() {
  return [...$("dest-options").querySelectorAll("input:checked")].map((i) => i.value);
}

function renderSteps(steps) {
  $("r-steps-count").textContent = steps.length ? String(steps.length) : "";
  const list = $("r-steps");
  if (!steps.length) {
    list.replaceChildren(el("li", "No follow-ups mentioned.", "none"));
    return;
  }
  // Most urgent first; the model's order breaks ties.
  const rank = (t) => PRIORITY[t.priority]?.rank ?? 1;
  const sorted = steps.map((t, i) => ({ t, i })).sort((a, b) => rank(a.t) - rank(b.t) || a.i - b.i).map((x) => x.t);

  list.replaceChildren(...sorted.map((t) => {
    const type = TASK_TYPES[t.type] || TASK_TYPES.todo;
    const li = el("li", null, "task");
    li.append(iconTile(type.icon, `type-${t.type in TASK_TYPES ? t.type : "todo"}`));

    const body = el("div", null, "task-body");
    body.append(el("div", t.title, "task-title"));
    const meta = el("div", null, "task-meta");
    meta.append(el("span", type.label, "task-type"));
    if (t.due_date) {
      const d = dueLabel(t.due_date);
      const chip = el("span", `Due ${d.label}`, d.soon ? "due soon" : "due");
      chip.title = t.due_date;
      meta.append(chip);
    }
    body.append(meta);

    const p = PRIORITY[t.priority] || PRIORITY.medium;
    const badge = el("span", p.label, `priority ${t.priority in PRIORITY ? t.priority : "medium"}`);
    badge.title = `${p.label} priority`;
    li.append(body, badge);
    return li;
  }));
}

function renderInsights(items) {
  $("r-insights-count").textContent = items.length ? String(items.length) : "";
  const list = $("r-insights");
  if (!items.length) {
    list.replaceChildren(el("li", "No opportunities, risks or competitors mentioned.", "none"));
    return;
  }
  list.replaceChildren(...items.map((x) => {
    const key = x.type in INSIGHT_TYPES ? x.type : "risk"; // class names only from known keys
    const li = el("li", null, `insight insight-${key}`);
    const head = el("div", null, "insight-head");
    head.append(svg(INSIGHT_TYPES[key].icon), el("span", INSIGHT_TYPES[key].label));
    li.append(head, el("p", x.text, "insight-text"));
    return li;
  }));
}

function renderDeliveries(ds) {
  $("r-deliveries-block").hidden = ds.length === 0;
  $("r-deliveries").replaceChildren(...ds.map((d) => {
    const info = sinkInfo(d.sink);
    const li = el("li", null, `delivery ${d.ok ? "ok" : "fail"}`);
    const name = el("div", null, "delivery-main");
    name.append(el("span", info.label, "delivery-name"), el("span", info.kind, "delivery-kind"));
    li.append(iconTile(info.icon, d.sink in SINKS ? `sink-${d.sink}` : "sink-other"), name);

    const detail = d.ok
      ? [d.id && `ID ${d.id}`, ...(d.refs || [])].filter(Boolean).join(" · ") || "Delivered"
      : d.error || "Failed";
    const status = el("span", null, "state");
    status.append(svg(d.ok ? "M5 12l5 5L19 7" : "M6 6l12 12M18 6L6 18"));
    li.append(el("span", detail, "delivery-detail"));

    if (d.ok && d.url && /^https:\/\//.test(d.url)) {
      const a = el("a", `Open in ${info.label} ↗`, "link");
      a.href = d.url;
      a.target = "_blank";
      a.rel = "noopener noreferrer";
      li.append(a);
    }
    li.append(status);
    return li;
  }));
}

// ---------- helpers ----------

function dueLabel(iso) {
  const [y, m, d] = iso.split("-").map(Number);
  const due = new Date(y, m - 1, d);
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  const days = Math.round((due - today) / 86400000);
  if (days === 0) return { label: "today", soon: true };
  if (days === 1) return { label: "tomorrow", soon: true };
  const opts = days > 1 && days < 7 ? { weekday: "short", day: "numeric", month: "short" } : { day: "numeric", month: "short", year: due.getFullYear() !== today.getFullYear() ? "numeric" : undefined };
  return { label: due.toLocaleDateString(undefined, opts), soon: days < 0 };
}

function languageName(code) {
  if (!code) return "";
  try {
    return new Intl.DisplayNames([navigator.language || "en"], { type: "language" }).of(code) || code;
  } catch {
    return code;
  }
}

async function copy(text, button) {
  try {
    await navigator.clipboard.writeText(text);
    const old = button.textContent;
    button.textContent = "Copied";
    setTimeout(() => (button.textContent = old), 1200);
  } catch { /* clipboard unavailable; ignore */ }
}

function clock(totalSeconds) {
  const s = Math.floor(totalSeconds);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

function mb(bytes) {
  return bytes < 1024 * 1024 ? `${Math.max(1, Math.round(bytes / 1024))} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function svg(d) {
  const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  s.setAttribute("viewBox", "0 0 24 24");
  s.setAttribute("aria-hidden", "true");
  const p = document.createElementNS("http://www.w3.org/2000/svg", "path");
  p.setAttribute("d", d);
  s.append(p);
  return s;
}

function iconTile(path, cls) {
  const tile = el("span", null, `tile ${cls}`);
  tile.append(svg(path));
  return tile;
}

function el(tag, text, cls) {
  const e = document.createElement(tag);
  if (text != null) e.textContent = text;
  if (cls) e.className = cls;
  return e;
}

function setStatus(msg, isError = false, { retryable = false, ref = "" } = {}) {
  $("status-box").hidden = !msg;
  $("status-box").classList.toggle("error", isError);
  $("status").textContent = msg;
  $("retry").hidden = !retryable;
  $("status-ref").textContent = ref;
}
