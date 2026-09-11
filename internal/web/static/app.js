const chatLog = document.getElementById("chat-log");
const agentConsoleLog = document.getElementById("agent-console-log");
const serverConsoleLog = document.getElementById("server-console-log");
const chatForm = document.getElementById("chat-form");
const chatInput = document.getElementById("chat-input");
const chatSend = document.getElementById("chat-send");
const processState = document.getElementById("process-state");
const appFrame = document.getElementById("app-frame");
const busyIndicator = document.getElementById("busy-indicator");
const busyText = document.getElementById("busy-text");
const reviewNote = document.getElementById("review-note");
const sidebar = document.getElementById("sidebar");
const btnCollapse = document.getElementById("btn-collapse");
const btnExpand = document.getElementById("btn-expand");

// The three-step loop this UI reflects: build (the agent works), load
// (the iframe picks up what changed), review (the dev server's log is
// watched for anything the change broke, which either ends the loop or
// starts it again as an auto-fix). Only "build" and "load" are phases a
// single task moves through and reports here; "review" has no end of its
// own — the error watcher runs for as long as the dev server does — so
// it's shown as a standing note rather than a step in the spinner.
const phaseLabels = { build: "Building...", load: "Loading..." };
let currentPhase = null;
let busy = false;

function setBusy(isBusy) {
  busy = isBusy;
  busyIndicator.hidden = !busy;
  chatSend.disabled = busy;
  updateReviewNote();
}

function updateReviewNote() {
  reviewNote.hidden = busy || processState.textContent !== "running";
}

function setProcessState(state) {
  processState.textContent = state;
  processState.className = "pill pill-" + state;
  updateReviewNote();
}

function addMessage(role, text) {
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
  chatLog.appendChild(wrapper);
  chatLog.scrollTop = chatLog.scrollHeight;
}

function firstLine(text) {
  const line = text.split("\n")[0].trim();
  if (line) return line.length > 80 ? line.slice(0, 80) + "…" : line;
  return "(click to expand)";
}

function addLine(container, text) {
  container.textContent += text + "\n";
  container.parentElement.scrollTop = container.parentElement.scrollHeight;
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
  })
  .catch(() => {});

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
      if (currentPhase) busyText.textContent = phaseLabels[currentPhase] || currentPhase;
      setBusy(!!state.busy);
      if (state.process) setProcessState(state.process);
    })
    .catch(() => {});
}

const events = new EventSource("/events");
events.addEventListener("open", resyncState);
events.addEventListener("chat", (event) => {
  const data = JSON.parse(event.data);
  addMessage(data.role, data.text);
});
events.addEventListener("phase", (event) => {
  currentPhase = JSON.parse(event.data).phase;
  busyText.textContent = phaseLabels[currentPhase] || currentPhase;
});
events.addEventListener("status", (event) => {
  const data = JSON.parse(event.data);
  if (data.text) addLine(agentConsoleLog, "… " + data.text);
  // The agent's own fine-grained status ("reading your message...",
  // "applying the patch...") is more informative than the coarse "build"
  // label while it's the one actually running.
  if (data.text && busy && currentPhase === "build") busyText.textContent = data.text;
});
events.addEventListener("busy", (event) => {
  setBusy(JSON.parse(event.data).busy);
});
events.addEventListener("log", (event) => {
  addLine(agentConsoleLog, JSON.parse(event.data).text);
});
events.addEventListener("serverlog", (event) => {
  addLine(serverConsoleLog, JSON.parse(event.data).text);
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
