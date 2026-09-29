// The --web-fe client. The browser owns the project: its files, its
// history and the signature the server put on both, kept in IndexedDB.
// Each message sends the whole project to the API, which edits it in
// memory and sends back what changed; the preview is Sandpack, running
// the files right here. Nothing about the project lives on the server.
import { loadSandpackClient } from "https://esm.sh/@codesandbox/sandpack-client@2.19.8";

const API = (window.YOLOCODER_API || "").replace(/\/$/, "");
// Tailwind's browser build, loaded into the preview's own head: the
// bundler ignores the <head> of public/index.html, so it cannot come
// from the project. It generates each class as it appears in the page.
const TAILWIND = "https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4/dist/index.global.js";
const MAX_IMAGES = 3;
const MAX_IMAGE_SIDE = 1600;
// Auto-fix is for the error a change just introduced. One attempt per
// distinct error, then it is left to the person, so a fix that does not
// take cannot loop on the shared key.
const AUTO_FIX_ATTEMPTS = 1;

const $ = (id) => document.getElementById(id);
const chatLog = $("chat-log");
const input = $("chat-input");
const sendButton = $("chat-send");
const frame = $("app-frame");

let project = null; // { kind, files, history, state, chat }
let busy = false;
let sandpack = null;
let pendingImages = [];
let lastChanged = new Set();
let selectedFile = "";
let shownError = "";
const fixAttempts = new Map();

// ---- storage -------------------------------------------------------
// IndexedDB, wrapped so a browser that refuses it (a private window, say)
// still runs the demo; the project just does not survive a reload there.

function openStore() {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open("yolocoder-web-fe", 1);
    request.onupgradeneeded = () => request.result.createObjectStore("kv");
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

async function load() {
  try {
    const db = await openStore();
    return await new Promise((resolve) => {
      const request = db.transaction("kv").objectStore("kv").get("project");
      request.onsuccess = () => resolve(request.result || null);
      request.onerror = () => resolve(null);
    });
  } catch {
    return null;
  }
}

async function save() {
  try {
    const db = await openStore();
    db.transaction("kv", "readwrite").objectStore("kv").put(project, "project");
  } catch {
    // Not saved; the page still works for this visit.
  }
}

// ---- project -------------------------------------------------------

async function startProject(kind) {
  const response = await fetch(`${API}/api/starter?kind=${encodeURIComponent(kind)}`);
  if (!response.ok) throw new Error(`Could not load the ${kind} starter (${response.status}).`);
  const started = await response.json();
  project = { kind, files: started.files, history: started.history, state: started.state, chat: [] };
  lastChanged = new Set();
  selectedFile = "";
  fixAttempts.clear();
  hideError();
  const intro = kind === "game"
    ? "A small canvas game is running on the right. Tell me what game to make of it."
    : "A blank React + Tailwind app is running on the right. Tell me what to build.";
  addMessage({ role: "system", text: intro });
  await save();
  renderChat();
  renderFiles();
  await showPreview(true);
}

// ---- preview -------------------------------------------------------

function sandboxSetup() {
  const files = {};
  for (const [path, code] of Object.entries(project.files)) files["/" + path] = { code };
  return { files, template: "create-react-app-typescript" };
}

async function showPreview(fresh) {
  if (sandpack && !fresh) {
    sandpack.updateSandbox(sandboxSetup());
    return;
  }
  if (sandpack) {
    sandpack.destroy();
    sandpack = null;
  }
  sandpack = await loadSandpackClient(frame, sandboxSetup(), {
    externalResources: [TAILWIND],
    showOpenInCodeSandbox: false,
    showErrorScreen: true,
    showLoadingScreen: true,
  });
  sandpack.listen(onPreviewMessage);
}

// An error is held briefly before it is believed: a change arrives as
// several evaluations in a row, and the error one of them throws on the
// way is often gone by the last. Only the one still standing is shown.
const ERROR_SETTLE_MS = 1200;
let pendingError = null;

function onPreviewMessage(message) {
  if (message.type === "done" && !message.compilatonError) {
    hideError();
  }
  if (message.type === "action" && message.action === "show-error") {
    const text = `${message.title || "Error"}: ${message.message || ""}`;
    // Sandpack's own hot-reload plumbing (the $csb/_csb names) failing
    // is not the app's error, and no edit fixes it; a full refresh does.
    if (/\$csb|_csb/.test(text)) {
      sandpack.dispatch({ type: "refresh" });
      return;
    }
    const where = message.path ? `\n  at ${message.path}${message.line ? ":" + message.line : ""}` : "";
    clearTimeout(pendingError);
    pendingError = setTimeout(() => reportError((text + where).trim()), ERROR_SETTLE_MS);
  }
}

function reportError(text) {
  if (!text || text === shownError) return;
  shownError = text;
  $("preview-error-text").textContent = text;
  $("preview-error").hidden = false;
  const attempts = fixAttempts.get(text) || 0;
  if (!busy && project.history.length > 0 && attempts < AUTO_FIX_ATTEMPTS) {
    fixAttempts.set(text, attempts + 1);
    sendTurn(fixMessage(text), [], "auto-fix");
  }
}

function fixMessage(text) {
  return `The preview shows this error. Fix it.\n\n${text}`;
}

function hideError() {
  clearTimeout(pendingError);
  shownError = "";
  $("preview-error").hidden = true;
}

$("btn-fix").addEventListener("click", () => {
  const text = $("preview-error-text").textContent;
  if (!busy && text) sendTurn(fixMessage(text), [], "auto-fix");
});
$("btn-dismiss").addEventListener("click", hideError);

// ---- chat ----------------------------------------------------------

function addMessage(message) {
  project.chat.push(message);
  if (project.chat.length > 200) project.chat.splice(0, project.chat.length - 200);
  return message;
}

function renderChat() {
  chatLog.replaceChildren(...project.chat.map(renderMessage));
  chatLog.scrollTop = chatLog.scrollHeight;
}

function renderMessage(message) {
  const item = document.createElement("div");
  item.className = `message ${message.role}`;
  if (message.role === "auto-fix") {
    const label = document.createElement("div");
    label.className = "message-label";
    label.textContent = "auto-fix";
    item.append(label);
  }
  const text = document.createElement("div");
  text.className = "message-text";
  text.textContent = message.text;
  item.append(text);
  for (const image of message.images || []) {
    const img = document.createElement("img");
    img.src = image;
    img.alt = "attached image";
    item.append(img);
  }
  if (message.running) {
    // The step happening now, with its own clock and the turn's. The
    // clocks tick between events (see tick), because the longest step —
    // the model thinking — sends nothing until it is over.
    const live = document.createElement("div");
    live.className = "live";
    const step = document.createElement("span");
    step.className = "live-step";
    step.textContent = message.status || "Working...";
    const time = document.createElement("span");
    time.className = "live-time";
    time.textContent = liveTime(message);
    live.append(step, time);
    item.append(live);
  }
  if (message.log) {
    const details = document.createElement("details");
    details.className = "activity";
    details.open = !!message.running;
    const summary = document.createElement("summary");
    summary.textContent = "Agent activity";
    const lines = document.createElement("pre");
    lines.textContent = message.log.join("\n");
    details.append(summary, lines);
    item.append(details);
  }
  const footer = [message.took ? `took ${message.took}` : "", message.usage || ""].filter(Boolean).join(" · ");
  if (footer) {
    const usage = document.createElement("div");
    usage.className = "usage";
    usage.textContent = footer;
    item.append(usage);
  }
  return item;
}

function seconds(ms) {
  return `${(ms / 1000).toFixed(1)}s`;
}

function liveTime(message) {
  const now = Date.now();
  return `${seconds(now - message.stepStarted)} · total ${seconds(now - message.started)}`;
}

// tick keeps the running turn's clocks moving between events.
setInterval(() => {
  const running = project && project.chat[project.chat.length - 1];
  const time = chatLog.querySelector(".message:last-child .live-time");
  if (running && running.running && time) time.textContent = liveTime(running);
}, 100);

function setBusy(value) {
  busy = value;
  sendButton.disabled = value;
  document.body.classList.toggle("busy", value);
}

async function sendTurn(message, images, role = "user") {
  if (busy || !project) return;
  setBusy(true);
  hideError();
  addMessage({ role, text: message, images });
  const started = Date.now();
  const activity = addMessage({ role: "assistant", text: "", log: [], running: true, status: "Sending the project...", started, stepStarted: started });
  renderChat();

  const refresh = () => {
    const nodes = chatLog.children;
    const node = nodes[nodes.length - 1];
    if (!node) return;
    const fresh = renderMessage(activity);
    node.replaceWith(fresh);
    const lines = fresh.querySelector(".activity pre");
    if (lines) lines.scrollTop = lines.scrollHeight;
    chatLog.scrollTop = chatLog.scrollHeight;
  };

  try {
    const response = await fetch(`${API}/api/turn`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ files: project.files, history: project.history, state: project.state, message, images }),
    });
    if (!response.ok) {
      const reason = (await response.text()).trim();
      if (response.status === 409) {
        throw new Error(`${reason}\n\nUse New App or New Game above to start again.`);
      }
      throw new Error(reason || `The server answered ${response.status}.`);
    }
    let finished = false;
    await readEvents(response, (name, data) => {
      if (name === "status") {
        if (data.text !== activity.status) activity.stepStarted = Date.now();
        activity.status = data.text;
        refresh();
      } else if (name === "log") {
        activity.log.push(data.text);
        refresh();
      } else if (name === "done") {
        finished = true;
        applyTurn(data);
        activity.text = data.reply;
        activity.usage = usageLine(data.usage);
      } else if (name === "failed") {
        finished = true;
        activity.text = "Error: " + data.error;
        activity.role = "system";
      }
    });
    if (!finished) throw new Error("The connection closed before the turn finished.");
  } catch (error) {
    activity.role = "system";
    activity.text = "Error: " + error.message;
  } finally {
    activity.running = false;
    activity.took = seconds(Date.now() - started);
    delete activity.status;
    delete activity.started;
    delete activity.stepStarted;
    if (activity.log && activity.log.length === 0) delete activity.log;
    refresh();
    setBusy(false);
    await save();
  }
}

function applyTurn(data) {
  const changed = data.changed || {};
  Object.assign(project.files, changed);
  project.history = data.history;
  project.state = data.state;
  lastChanged = new Set(Object.keys(changed));
  renderFiles();
  if (lastChanged.size > 0) showPreview(false);
}

function usageLine(usage) {
  if (!usage || !usage.TotalTokens) return "";
  const cached = usage.CachedTokens ? ` (${usage.CachedTokens.toLocaleString()} cached)` : "";
  return `in: ${usage.InputTokens.toLocaleString()}${cached} | out: ${usage.OutputTokens.toLocaleString()}`;
}

// readEvents reads a Server-Sent Events body from a fetch response. It
// is not EventSource, because EventSource cannot POST.
async function readEvents(response, onEvent) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let boundary;
    while ((boundary = buffer.indexOf("\n\n")) >= 0) {
      const block = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      let name = "message";
      let data = "";
      for (const line of block.split("\n")) {
        if (line.startsWith("event: ")) name = line.slice(7);
        else if (line.startsWith("data: ")) data += line.slice(6);
      }
      if (data) onEvent(name, JSON.parse(data));
    }
  }
}

// ---- composer ------------------------------------------------------

$("chat-form").addEventListener("submit", (event) => {
  event.preventDefault();
  const message = input.value.trim();
  if (!message || busy) return;
  const images = pendingImages;
  input.value = "";
  pendingImages = [];
  renderPendingImages();
  sendTurn(message, images);
});

input.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    $("chat-form").requestSubmit();
  }
});

input.addEventListener("paste", (event) => {
  const files = [...event.clipboardData.items].filter((item) => item.type.startsWith("image/")).map((item) => item.getAsFile());
  if (files.length) {
    event.preventDefault();
    attachImages(files);
  }
});

$("btn-attach").addEventListener("click", () => $("file-input").click());
$("file-input").addEventListener("change", (event) => {
  attachImages([...event.target.files]);
  event.target.value = "";
});

async function attachImages(files) {
  for (const file of files) {
    if (pendingImages.length >= MAX_IMAGES) break;
    try {
      pendingImages.push(await shrink(file));
    } catch {
      // An image the browser cannot decode is simply not attached.
    }
  }
  renderPendingImages();
}

// shrink redraws an image at no more than MAX_IMAGE_SIDE a side, as JPEG,
// so a pasted screenshot fits the API's per-image limit.
async function shrink(file) {
  const bitmap = await createImageBitmap(file);
  const scale = Math.min(1, MAX_IMAGE_SIDE / Math.max(bitmap.width, bitmap.height));
  const canvas = document.createElement("canvas");
  canvas.width = Math.round(bitmap.width * scale);
  canvas.height = Math.round(bitmap.height * scale);
  canvas.getContext("2d").drawImage(bitmap, 0, 0, canvas.width, canvas.height);
  return canvas.toDataURL("image/jpeg", 0.85);
}

function renderPendingImages() {
  const holder = $("pending-images");
  holder.hidden = pendingImages.length === 0;
  holder.replaceChildren(...pendingImages.map((image, index) => {
    const button = document.createElement("button");
    button.type = "button";
    button.title = "Remove";
    const img = document.createElement("img");
    img.src = image;
    img.alt = "attached image";
    button.append(img);
    button.addEventListener("click", () => {
      pendingImages.splice(index, 1);
      renderPendingImages();
    });
    return button;
  }));
}

// ---- code view -----------------------------------------------------

function renderFiles() {
  const paths = Object.keys(project.files).sort();
  if (!paths.includes(selectedFile)) selectedFile = paths.find((path) => path.endsWith("App.tsx")) || paths[0];
  $("file-list").replaceChildren(...paths.map((path) => {
    const item = document.createElement("li");
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = path;
    button.classList.toggle("selected", path === selectedFile);
    button.classList.toggle("changed", lastChanged.has(path));
    button.addEventListener("click", () => {
      selectedFile = path;
      renderFiles();
    });
    item.append(button);
    return item;
  }));
  $("file-view").textContent = project.files[selectedFile] || "";
}

for (const tab of document.querySelectorAll("[data-tab]")) {
  tab.addEventListener("click", () => {
    for (const other of document.querySelectorAll("[data-tab]")) other.setAttribute("aria-selected", String(other === tab));
    $("preview-pane").hidden = tab.dataset.tab !== "preview";
    $("code-pane").hidden = tab.dataset.tab !== "code";
  });
}

for (const button of document.querySelectorAll("[data-kind]")) {
  button.addEventListener("click", async () => {
    if (busy) return;
    const hasWork = project && project.history.length > 0;
    if (hasWork && !confirm("Start a new project? This one will be replaced.")) return;
    try {
      await startProject(button.dataset.kind);
    } catch (error) {
      addMessage({ role: "system", text: "Error: " + error.message });
      renderChat();
    }
  });
}

// ---- boot ----------------------------------------------------------

(async () => {
  project = await load();
  if (project) {
    renderChat();
    renderFiles();
    await showPreview(true);
    return;
  }
  try {
    await startProject("app");
  } catch (error) {
    project = { kind: "app", files: {}, history: [], state: "", chat: [] };
    addMessage({ role: "system", text: "Error: " + error.message });
    renderChat();
  }
})();
