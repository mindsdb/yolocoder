const chatLog = document.getElementById("chat-log");
const consoleLog = document.getElementById("console-log");
const chatForm = document.getElementById("chat-form");
const chatInput = document.getElementById("chat-input");
const chatSend = document.getElementById("chat-send");
const processState = document.getElementById("process-state");
const appFrame = document.getElementById("app-frame");
const busyIndicator = document.getElementById("busy-indicator");
const busyText = document.getElementById("busy-text");

function setBusy(busy) {
  busyIndicator.hidden = !busy;
  chatSend.disabled = busy;
  if (busy) busyText.textContent = "Working...";
}

function addMessage(role, text) {
  const div = document.createElement("div");
  div.className = "msg msg-" + role;
  div.textContent = text;
  chatLog.appendChild(div);
  chatLog.scrollTop = chatLog.scrollHeight;
}

function addConsoleLine(text) {
  consoleLog.textContent += text + "\n";
  consoleLog.parentElement.scrollTop = consoleLog.parentElement.scrollHeight;
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

const events = new EventSource("/events");
events.addEventListener("chat", (event) => {
  const data = JSON.parse(event.data);
  addMessage(data.role, data.text);
});
events.addEventListener("status", (event) => {
  const data = JSON.parse(event.data);
  if (data.text) addConsoleLine("… " + data.text);
  if (data.text && !busyIndicator.hidden) busyText.textContent = data.text;
});
events.addEventListener("busy", (event) => {
  setBusy(JSON.parse(event.data).busy);
});
events.addEventListener("log", (event) => {
  addConsoleLine(JSON.parse(event.data).text);
});
events.addEventListener("serverlog", (event) => {
  addConsoleLine(JSON.parse(event.data).text);
});
events.addEventListener("process", (event) => {
  const data = JSON.parse(event.data);
  processState.textContent = data.state;
  processState.className = "pill pill-" + data.state;
  if (data.state === "running" && appProxyOrigin) {
    // A fresh dev server after a restart is still the same proxy URL;
    // force a reload so the iframe doesn't keep showing a torn-down page.
    appFrame.src = appProxyOrigin + "/?t=" + Date.now();
  }
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

document.getElementById("btn-start").addEventListener("click", () => fetch("/process/start", { method: "POST" }));
document.getElementById("btn-restart").addEventListener("click", () => fetch("/process/restart", { method: "POST" }));
document.getElementById("btn-stop").addEventListener("click", () => fetch("/process/stop", { method: "POST" }));

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
