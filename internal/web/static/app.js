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

function addMessage(role, text, usage) {
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
// every provider supports listing.
fetch("/models")
  .then((response) => response.json())
  .then((data) => {
    const models = data.models || [];
    if (data.current && !models.includes(data.current)) models.unshift(data.current);
    modelSelect.innerHTML = "";
    for (const name of models) {
      const option = document.createElement("option");
      option.value = name;
      option.textContent = name;
      modelSelect.appendChild(option);
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
    addMessage(data.role, data.text);
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

chatForm.addEventListener("submit", (event) => {
  event.preventDefault();
  const message = chatInput.value.trim();
  if (!message) return;
  chatInput.value = "";
  fetch("/chat", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message }),
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
