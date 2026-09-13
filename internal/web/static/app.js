const chatLog = document.getElementById("chat-log");
const chatForm = document.getElementById("chat-form");
const chatInput = document.getElementById("chat-input");
const chatSend = document.getElementById("chat-send");
const folderIndicator = document.getElementById("folder-indicator");
const appFrame = document.getElementById("app-frame");
const sidebar = document.getElementById("sidebar");
const btnCollapse = document.getElementById("btn-collapse");
const btnExpand = document.getElementById("btn-expand");
const btnRecover = document.getElementById("btn-recover");
const modelSelect = document.getElementById("model-select");
const pendingImagesEl = document.getElementById("pending-images");

// Each turn (a user message or an auto-fix) gets its own collapsible
// "Agent activity" block, open and live while it runs, collapsed once
// the reply lands — there is no longer one shared console for the whole
// conversation. currentActivity is that turn's block while one is open,
// and null between turns; status/log/serverlog lines go wherever it
// points, or nowhere if nothing is running (ambient dev-server chatter
// with no turn attached to it isn't shown).
let currentActivity = null;
let currentPhase = null;
let busy = false;

const phaseLabels = { build: "Building...", load: "Loading..." };

function setBusy(isBusy) {
  busy = isBusy;
  chatSend.disabled = busy;
}

// There's no visible process-state pill: the dev server starts and
// recovers on its own, so ordinary "starting"/"running" states aren't
// shown. The one exception is btnRecover, offered only once automatic
// recovery has actually given up (see recoverFromCrash server-side).
function setProcessState(state) {
  btnRecover.hidden = state !== "error";
}

function addMessage(role, text, usage, images) {
  const wrapper = document.createElement("div");
  wrapper.className = "msg msg-" + role;
  // Long messages (a stack trace, a wall of npm output relayed as an
  // error) are collapsed behind a one-line summary rather than dumped in
  // full — expand to read the whole thing.
  if (text.length > 400 || text.split("\n").length > 6) {
    const details = document.createElement("details");
    const summary = document.createElement("summary");
    summary.textContent = firstLine(text);
    const body = document.createElement("pre");
    body.textContent = text;
    details.append(summary, body);
    wrapper.appendChild(details);
  } else {
    wrapper.textContent = text;
  }
  // Screenshots the message was sent with, echoed back on the same
  // "user" chat event that carried the text (see chatMessage.Images
  // server-side) — appended after the text above, which replaces
  // wrapper's children wholesale when it runs.
  if (images && images.length) {
    const gallery = document.createElement("div");
    gallery.className = "msg-images";
    for (const src of images) {
      const image = document.createElement("img");
      image.src = src;
      gallery.appendChild(image);
    }
    wrapper.appendChild(gallery);
  }
  // Only ever set on an assistant reply that actually cost something the
  // provider reported (see newUsageInfo server-side). Grouped with the
  // bubble in its own tight little column, rather than as another child
  // of #chat-log's own flex gap, so it reads as that one reply's footer
  // and not a separate line in the conversation.
  if (usage) {
    const group = document.createElement("div");
    group.className = "msg-group";
    const caption = document.createElement("div");
    caption.className = "usage-note";
    caption.textContent = formatUsage(usage);
    group.append(wrapper, caption);
    chatLog.appendChild(group);
  } else {
    chatLog.appendChild(wrapper);
  }
  chatLog.scrollTop = chatLog.scrollHeight;
  return wrapper;
}

// Cached-token counts only ever apply to input: every provider that
// tracks it caches (part of) the prompt, never the output, since output
// is generated fresh every time — so there's no "out (cached)" to show.
function formatUsage(usage) {
  let text = `in: ${usage.input.toLocaleString()}`;
  if (usage.cached) text += ` (${usage.cached.toLocaleString()} cached)`;
  text += ` | out: ${usage.output.toLocaleString()}`;
  return text;
}

// Screenshot paste: most models the terminal talks to aren't multimodal
// at all, so this is a web-UI-only, best-effort affordance — a model
// that can't see images simply gets an ordinary text turn instead, same
// as if nothing had been pasted.
const MAX_PENDING_IMAGES = 6;
const MAX_IMAGE_DIMENSION = 1600;
let pendingImages = []; // data URLs, already downscaled, queued to send

chatInput.addEventListener("paste", (event) => {
  const items = event.clipboardData && event.clipboardData.items;
  if (!items) return;
  let pastedImage = false;
  for (const item of items) {
    if (!item.type || !item.type.startsWith("image/")) continue;
    pastedImage = true;
    const file = item.getAsFile();
    if (file) queuePastedImage(file);
  }
  // Only swallow the paste when it was actually an image: an ordinary
  // text paste (which also shows up as a clipboard item) must still land
  // in the textarea normally.
  if (pastedImage) event.preventDefault();
});

function queuePastedImage(file) {
  if (pendingImages.length >= MAX_PENDING_IMAGES) return;
  const reader = new FileReader();
  reader.onload = () => {
    const image = new Image();
    image.onload = () => {
      pendingImages.push(downscale(image));
      renderPendingImages();
    };
    image.src = reader.result;
  };
  reader.readAsDataURL(file);
}

// A retina screenshot can be several megapixels; shrinking it in the
// browser before it's ever sent keeps both the request payload and
// whatever the provider bills for vision tokens reasonable, without
// visibly softening the text a bug report screenshot needs to stay
// legible at ordinary display sizes.
function downscale(image) {
  const scale = Math.min(1, MAX_IMAGE_DIMENSION / Math.max(image.width, image.height));
  const canvas = document.createElement("canvas");
  canvas.width = Math.round(image.width * scale) || 1;
  canvas.height = Math.round(image.height * scale) || 1;
  canvas.getContext("2d").drawImage(image, 0, 0, canvas.width, canvas.height);
  return canvas.toDataURL("image/png");
}

function renderPendingImages() {
  pendingImagesEl.innerHTML = "";
  pendingImagesEl.hidden = pendingImages.length === 0;
  pendingImages.forEach((dataURL, index) => {
    const chip = document.createElement("div");
    chip.className = "pending-thumb";
    const image = document.createElement("img");
    image.src = dataURL;
    const remove = document.createElement("button");
    remove.type = "button";
    remove.title = "Remove";
    remove.textContent = "×";
    remove.addEventListener("click", () => {
      pendingImages.splice(index, 1);
      renderPendingImages();
    });
    chip.append(image, remove);
    pendingImagesEl.appendChild(chip);
  });
}

function firstLine(text) {
  const line = text.split("\n")[0].trim();
  if (line) return line.length > 80 ? line.slice(0, 80) + "…" : line;
  return "(click to expand)";
}

// openActivity starts this turn's own activity block, open and marked
// running, right after its message bubble.
function openActivity() {
  const details = document.createElement("details");
  details.className = "activity running";
  details.open = true;
  const summary = document.createElement("summary");
  summary.textContent = phaseLabels[currentPhase] || "Agent activity";
  const log = document.createElement("div");
  log.className = "activity-log";
  details.append(summary, log);
  chatLog.appendChild(details);
  chatLog.scrollTop = chatLog.scrollHeight;
  currentActivity = details;
}

// closeActivity collapses the current turn's activity block once its
// reply has landed, so the block a viewer lands on is the reply, not a
// wall of steps that already finished.
function closeActivity() {
  if (!currentActivity) return;
  currentActivity.classList.remove("running");
  currentActivity.querySelector("summary").textContent = "Agent activity";
  currentActivity.open = false;
  currentActivity = null;
}

function addActivityLine(text) {
  if (!currentActivity) return;
  const log = currentActivity.querySelector(".activity-log");
  log.textContent += text + "\n";
  log.scrollTop = log.scrollHeight;
}

function reloadApp() {
  if (!appProxyOrigin) return;
  appFrame.src = appProxyOrigin + "/?t=" + Date.now();
}

// The app being built is proxied on its own dedicated port, chosen fresh
// each run, so it can't be hardcoded into the iframe's src up front.
let appProxyOrigin = null;
fetch("/config")
  .then((response) => response.json())
  .then((config) => {
    appProxyOrigin = "http://localhost:" + config.appProxyPort;
    appFrame.src = appProxyOrigin + "/";
    if (config.folder) {
      folderIndicator.textContent = config.folder;
      folderIndicator.title = config.folder;
    }
  })
  .catch(() => {});

// The model list comes from the endpoint's own /v1/models, the same way
// `yolocoder model` on the terminal picks one — best-effort, since not
// every provider supports listing. Each entry is {id, provider}; provider
// is only ever set when the endpoint's own listing reports one (see
// modelOption server-side) — a single-vendor endpoint (MindsHub included)
// usually leaves it empty, in which case there is nothing to group by and
// this falls back to exactly the flat list it showed before.
fetch("/models")
  .then((response) => response.json())
  .then((data) => {
    const models = data.models || [];
    if (data.current && !models.some((model) => model.id === data.current)) {
      models.unshift({ id: data.current, provider: "" });
    }
    modelSelect.innerHTML = "";
    const distinctProviders = new Set(models.map((model) => model.provider).filter(Boolean));
    if (distinctProviders.size > 1) {
      const groups = new Map();
      for (const model of models) {
        const key = model.provider || "Other";
        if (!groups.has(key)) groups.set(key, document.createElement("optgroup"));
        const group = groups.get(key);
        group.label = key;
        group.appendChild(modelOptionEl(model));
      }
      for (const group of groups.values()) modelSelect.appendChild(group);
    } else {
      for (const model of models) modelSelect.appendChild(modelOptionEl(model));
    }
    if (data.current) modelSelect.value = data.current;
    modelSelect.disabled = !!data.locked || models.length <= 1;
    modelSelect.title = data.locked
      ? "Set by OPENAI_MODEL; restart to change it"
      : "Model";
  })
  .catch(() => {
    modelSelect.hidden = true;
  });

function modelOptionEl(model) {
  const option = document.createElement("option");
  option.value = model.id;
  option.textContent = model.id;
  return option;
}

modelSelect.addEventListener("change", () => {
  fetch("/model", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ model: modelSelect.value }),
  });
});

// The ground truth behind busy/phase/process, fetched whenever the SSE
// connection below (re)opens. A one-shot event a tab happens to miss —
// a reconnect during a long build, a tab opened mid-task — would
// otherwise leave the UI stuck showing whatever it last knew, forever;
// this is what lets it catch up instead.
function resyncState() {
  fetch("/state")
    .then((response) => response.json())
    .then((state) => {
      currentPhase = state.phase || null;
      setBusy(!!state.busy);
      if (state.process) setProcessState(state.process);
    })
    .catch(() => {});
}

const events = new EventSource("/events");
events.addEventListener("open", resyncState);
events.addEventListener("chat", (event) => {
  const data = JSON.parse(event.data);
  if (data.role === "user" || data.role === "auto-fix") {
    addMessage(data.role, data.text, null, data.images);
    openActivity();
  } else {
    addMessage(data.role, data.text, data.usage);
  }
});
events.addEventListener("phase", (event) => {
  currentPhase = JSON.parse(event.data).phase;
  if (currentActivity) {
    currentActivity.querySelector("summary").textContent = phaseLabels[currentPhase] || "Agent activity";
  }
});
events.addEventListener("status", (event) => {
  const data = JSON.parse(event.data);
  if (data.text) addActivityLine("… " + data.text);
});
events.addEventListener("busy", (event) => {
  const isBusy = JSON.parse(event.data).busy;
  setBusy(isBusy);
  // Tied to busy rather than the assistant's reply arriving: a task
  // that errors out (the model call itself failing, say) never sends
  // one, and the activity block would otherwise spin forever. busy
  // going false covers every path, success or not, since it's set with
  // a defer server-side.
  if (!isBusy) closeActivity();
});
events.addEventListener("log", (event) => {
  addActivityLine(JSON.parse(event.data).text);
});
events.addEventListener("serverlog", (event) => {
  addActivityLine(JSON.parse(event.data).text);
});
// Fired after any applied change, whatever's running the dev server on
// its own (Vite HMR, tsx watch) can't be relied on to have visibly
// reflected in the iframe yet, so make sure it does.
events.addEventListener("reload", reloadApp);
events.addEventListener("process", (event) => {
  const data = JSON.parse(event.data);
  setProcessState(data.state);
  if (data.state === "running") reloadApp();
});

// Enter sends, Shift+Enter inserts a newline (the ordinary textarea
// behavior, kept available for anyone who wants to draft a longer,
// multi-line message before sending it). isComposing skips this while an
// IME is still resolving a character, so committing that character
// doesn't also send the message early.
chatInput.addEventListener("keydown", (event) => {
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) return;
  event.preventDefault();
  chatForm.requestSubmit();
});

chatForm.addEventListener("submit", (event) => {
  event.preventDefault();
  const message = chatInput.value.trim();
  if (!message && pendingImages.length === 0) return;
  chatInput.value = "";
  const images = pendingImages;
  pendingImages = [];
  renderPendingImages();
  fetch("/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message, images }),
  });
});

btnCollapse.addEventListener("click", () => {
  sidebar.classList.add("collapsed");
  btnExpand.hidden = false;
});
btnExpand.addEventListener("click", () => {
  sidebar.classList.remove("collapsed");
  btnExpand.hidden = true;
});
btnRecover.addEventListener("click", () => fetch("/process/restart", { method: "POST" }));

// The shim injected into the proxied app reports crashes to us via
// postMessage (it can't reach the yolocoder server directly: it doesn't
// know about it, only about its own parent window). Relay it to the
// backend the same way a server-side error arrives.
window.addEventListener("message", (event) => {
  const data = event.data;
  if (!data || (data.type !== "APP_RUNTIME_ERROR" && data.type !== "APP_UNHANDLED_REJECTION")) return;
  fetch("/client-error", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(data),
  });
});
