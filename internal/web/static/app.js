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
const btnAttach = document.getElementById("btn-attach");
const fileInput = document.getElementById("file-input");
const progressEl = document.getElementById("progress");
const btnLatest = document.getElementById("btn-latest");

// Sticky-scroll: the transcript is the only scroll container, so new output
// should only pull the view down when the viewer is already at the bottom.
// If they've scrolled up to read, we leave them there and offer a "latest"
// pill instead of yanking them away mid-read.
let pinned = true;
const PIN_THRESHOLD = 64;

function updateLatestBtn() {
  btnLatest.hidden = pinned;
}

let scrollFrame = 0;
let scrollReadPending = false;
let scrollWritePending = false;
let scrollForcePending = false;

// Scroll reads and writes are batched together. A build can publish many log
// lines in one frame; doing a layout read and scroll write for each one makes
// the chat compete with the app iframe for the same main thread. Reads happen
// first so a user scroll in the same frame always wins over a non-forced write.
function scheduleScrollFrame() {
  if (scrollFrame) return;
  scrollFrame = requestAnimationFrame(() => {
    const readPending = scrollReadPending;
    const writePending = scrollWritePending;
    const forceScroll = scrollForcePending;
    scrollReadPending = false;
    scrollWritePending = false;
    scrollForcePending = false;
    scrollFrame = 0;

    if (readPending) {
      pinned = chatLog.scrollHeight - chatLog.scrollTop - chatLog.clientHeight <= PIN_THRESHOLD;
    }
    if (writePending && (pinned || forceScroll)) {
      chatLog.scrollTop = chatLog.scrollHeight;
      if (forceScroll) pinned = true;
    }
    updateLatestBtn();
  });
}

function scrollToLatest(force) {
  scrollWritePending = true;
  scrollForcePending = scrollForcePending || !!force;
  scheduleScrollFrame();
}

chatLog.addEventListener("scroll", () => {
  scrollReadPending = true;
  scheduleScrollFrame();
}, { passive: true });

btnLatest.addEventListener("click", () => {
  pinned = true;
  scrollToLatest(true);
});

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
  updateSendState();
  progressEl.hidden = !isBusy;
}

// The send button is live only when there is something to send. It is
// the same condition the submit handler already enforces by returning
// early — stating it on the button means an empty press is not offered
// in the first place, rather than silently doing nothing.
function updateSendState() {
  chatSend.disabled = busy || (chatInput.value.trim() === "" && pendingImages.length === 0);
}
chatInput.addEventListener("input", updateSendState);

// There's no visible process-state pill: the dev server starts and
// recovers on its own, so ordinary "starting"/"running" states aren't
// shown. The one exception is btnRecover, offered only once automatic
// recovery has actually given up (see recoverFromCrash server-side).
function setProcessState(state) {
  btnRecover.hidden = state !== "error";
}

// Markdown, built as DOM nodes rather than parsed as HTML. Nothing here
// ever touches innerHTML, so there is no path by which a reply — which
// may be quoting a file that contains anything at all — becomes markup.
// It covers what the agent actually writes: fenced code, lists, inline
// code, bold and links. Anything else arrives as the text it was.
function renderMarkdown(text, into) {
  const lines = text.split("\n");
  const starts = /^```|^\s*[-*]\s+|^\s*\d+\.\s+|^#{1,4}\s/;
  let index = 0;

  while (index < lines.length) {
    const line = lines[index];

    if (/^```/.test(line)) {
      const fenced = [];
      index++;
      while (index < lines.length && !/^```/.test(lines[index])) fenced.push(lines[index++]);
      index++; // the closing fence, if it ever came
      const pre = document.createElement("pre");
      const code = document.createElement("code");
      code.textContent = fenced.join("\n");
      pre.appendChild(code);
      into.appendChild(pre);
      continue;
    }

    const bullet = /^\s*[-*]\s+/;
    const numbered = /^\s*\d+\.\s+/;
    for (const [pattern, tag] of [[bullet, "ul"], [numbered, "ol"]]) {
      if (!pattern.test(line)) continue;
      const list = document.createElement(tag);
      while (index < lines.length && pattern.test(lines[index])) {
        const item = document.createElement("li");
        renderInline(lines[index].replace(pattern, ""), item);
        list.appendChild(item);
        index++;
      }
      into.appendChild(list);
    }
    if (bullet.test(line) || numbered.test(line)) continue;

    const heading = /^(#{1,4})\s+(.*)$/.exec(line);
    if (heading) {
      const element = document.createElement("h" + Math.min(6, heading[1].length + 2));
      renderInline(heading[2], element);
      into.appendChild(element);
      index++;
      continue;
    }

    if (line.trim() === "") {
      index++;
      continue;
    }

    const paragraph = [];
    while (index < lines.length && lines[index].trim() !== "" && !starts.test(lines[index])) {
      paragraph.push(lines[index++]);
    }
    const element = document.createElement("p");
    renderInline(paragraph.join("\n"), element);
    into.appendChild(element);
  }
}

// Code spans are matched first so that a ** inside one stays literal.
function renderInline(text, into) {
  const token = /(`[^`]+`)|(\*\*[^*]+\*\*)|(\[[^\]]+\]\([^)\s]+\))/g;
  let last = 0;
  let match;
  while ((match = token.exec(text)) !== null) {
    if (match.index > last) into.appendChild(document.createTextNode(text.slice(last, match.index)));
    const found = match[0];
    if (found.startsWith("`")) {
      const code = document.createElement("code");
      code.textContent = found.slice(1, -1);
      into.appendChild(code);
    } else if (found.startsWith("**")) {
      const strong = document.createElement("strong");
      strong.textContent = found.slice(2, -2);
      into.appendChild(strong);
    } else {
      const split = found.indexOf("](");
      const label = found.slice(1, split);
      const href = found.slice(split + 2, -1);
      // Only http(s). A javascript: or data: URL written into a reply
      // must not become something clickable.
      if (/^https?:\/\//i.test(href)) {
        const link = document.createElement("a");
        link.href = href;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        link.textContent = label;
        into.appendChild(link);
      } else {
        into.appendChild(document.createTextNode(label));
      }
    }
    last = match.index + found.length;
  }
  if (last < text.length) into.appendChild(document.createTextNode(text.slice(last)));
}

function addMessage(role, text, usage, images, profile) {
  const wrapper = document.createElement("div");
  wrapper.className = "msg msg-" + role;
  // Long messages (a stack trace, a wall of npm output relayed as an
  // error) are collapsed behind a one-line summary rather than dumped in
  // full — expand to read the whole thing.
  // An assistant reply is written in markdown and rendered as such. It
  // is never collapsed: the long ones are lists of what changed, which
  // is the part worth reading, where the collapse exists for a stack
  // trace or a wall of npm output arriving as an error.
  if (role === "assistant") {
    renderMarkdown(text, wrapper);
  } else if (text.length > 400 || text.split("\n").length > 6) {
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
  // The profile line is independent of usage: a provider that reports no
  // tokens at all still has a wall clock, so a turn can have one footer,
  // the other, both, or neither.
  // One line, not a stack: tokens and time are the same kind of fact
  // about the same turn, and two dim lines under a reply read as two
  // separate remarks about it.
  const footers = [];
  if (usage) footers.push(formatUsage(usage));
  if (profile) footers.push(profile);
  if (footers.length) {
    const group = document.createElement("div");
    group.className = "msg-group";
    const caption = document.createElement("div");
    caption.className = "usage-note";
    caption.textContent = footers.join(" | ");
    group.append(wrapper, caption);
    chatLog.appendChild(group);
  } else {
    chatLog.appendChild(wrapper);
  }
  if (role === "user" || role === "auto-fix") {
    pinned = true;
    scrollToLatest(true);
  } else {
    scrollToLatest();
  }
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

// Picking a file goes through the same queue as pasting one: until now
// a screenshot could only arrive by paste, which is fine when you have
// just taken one and no help at all when it is already on disk.
btnAttach.addEventListener("click", () => fileInput.click());
fileInput.addEventListener("change", () => {
  for (const file of fileInput.files || []) {
    if (file.type && file.type.startsWith("image/")) queuePastedImage(file);
  }
  // Cleared so choosing the same file twice in a row still fires change.
  fileInput.value = "";
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
  // An image on its own is enough to send, so the button follows the
  // queue as well as the text.
  updateSendState();
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
  const label = document.createElement("span");
  label.className = "sum-label";
  label.textContent = phaseLabels[currentPhase] || "Agent activity";
  const dot = document.createElement("span");
  dot.className = "run-dot";
  summary.append(label, dot);
  const log = document.createElement("div");
  log.className = "activity-log";
  details.append(summary, log);
  chatLog.appendChild(details);
  scrollToLatest();
  currentActivity = details;
}

function setSummaryLabel(details, text) {
  const label = details.querySelector(".sum-label");
  if (label) label.textContent = text;
  else details.querySelector("summary").textContent = text;
}

// closeActivity collapses the current turn's activity block once its
// reply has landed, so the block a viewer lands on is the reply, not a
// wall of steps that already finished.
function closeActivity() {
  if (!currentActivity) return;
  currentActivity.classList.remove("running");
  const dot = currentActivity.querySelector(".run-dot");
  if (dot) dot.remove();
  setSummaryLabel(currentActivity, "Agent activity");
  currentActivity.open = false;
  currentActivity = null;
  scrollToLatest();
}

// Status lines ("… Mapping the folder...") become step headers with a
// green › mark; indented follow-ups render dimmer as continuations; raw
// server output stays dim and unmarked. One scroll container for the
// whole transcript — the log never scrolls on its own, so the wheel
// can't get trapped inside a finished turn.
function addActivityLine(text, kind) {
  if (!currentActivity) return;
  const log = currentActivity.querySelector(".activity-log");
  const line = document.createElement("div");
  if (kind === "step") {
    line.className = "alog-line";
    const mark = document.createElement("span");
    mark.className = "alog-mark";
    mark.textContent = "›";
    const body = document.createElement("span");
    body.textContent = text;
    line.append(mark, body);
  } else if (kind === "cont") {
    line.className = "alog-line";
    const mark = document.createElement("span");
    mark.className = "alog-mark";
    mark.textContent = "·";
    const body = document.createElement("span");
    body.className = "alog-cont";
    body.textContent = text;
    line.append(mark, body);
  } else {
    line.className = "alog-raw";
    line.textContent = text;
  }
  log.appendChild(line);
  scrollToLatest();
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
      // A tab that (re)connects after an offer was staged still renders it:
      // the offer event is one-shot, but /state carries the unanswered ones.
      for (const offer of state.offers || []) addErrorOffer(offer.id, offer.source, offer.text);
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
    addMessage(data.role, data.text, data.usage, undefined, data.profile);
  }
});
events.addEventListener("phase", (event) => {
  currentPhase = JSON.parse(event.data).phase;
  if (currentActivity) {
    setSummaryLabel(currentActivity, phaseLabels[currentPhase] || "Agent activity");
  }
});
events.addEventListener("status", (event) => {
  const data = JSON.parse(event.data);
  if (data.text) addActivityLine(data.text.trim(), "step");
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
  const text = JSON.parse(event.data).text || "";
  // Indented follow-ups ("  mapped 20 files · <1ms") render as dim
  // continuations under their step; anything unindented is raw output.
  addActivityLine(text.trim(), /^\s/.test(text) ? "cont" : "raw");
});
events.addEventListener("serverlog", (event) => {
  addActivityLine((JSON.parse(event.data).text || "").trim(), "raw");
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
// A staged error arrives as its own event (not a chat message), so nothing
// fires until the card's Fix button is pressed; a dismiss from another tab
// removes the card here too.
events.addEventListener("error-offer", (event) => {
  const data = JSON.parse(event.data);
  addErrorOffer(data.id, data.source, data.text || "");
});
events.addEventListener("error-offer-dismissed", (event) => {
  removeErrorOffer(JSON.parse(event.data).id);
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

// An error spotted while using the app becomes an ask-first card, not an
// automatic turn: Fix sends it back as the fix it would have been,
// Dismiss drops it (the server remembers the refusal until you drive
// again, so a still-broken page doesn't re-ask on every throw).
const errorOffers = new Map(); // id -> element, so a dismiss from another tab removes ours too

function addErrorOffer(id, source, text) {
  if (errorOffers.has(id)) return;
  const card = document.createElement("div");
  card.className = "error-offer";
  card.dataset.offerId = id;

  const head = document.createElement("div");
  head.className = "error-offer-head";
  const dot = document.createElement("span");
  dot.className = "error-offer-dot";
  const label = document.createElement("span");
  label.className = "error-offer-label";
  label.textContent = source === "server" ? "The app hit an error" : "Something broke in the app";
  const ask = document.createElement("span");
  ask.className = "error-offer-ask";
  ask.textContent = " — want me to fix it?";
  head.append(dot, label, ask);

  const title = document.createElement("div");
  title.className = "error-offer-title";
  title.textContent = firstLine(text);

  card.append(head, title);
  if (text.split("\n").length > 1 || text.length > 120) {
    const details = document.createElement("details");
    details.className = "error-offer-details";
    const summary = document.createElement("summary");
    summary.textContent = "show error";
    const body = document.createElement("pre");
    body.textContent = text;
    details.append(summary, body);
    card.appendChild(details);
  }

  const actions = document.createElement("div");
  actions.className = "error-offer-actions";
  const fix = document.createElement("button");
  fix.type = "button";
  fix.className = "error-offer-fix";
  fix.textContent = "Fix it";
  fix.addEventListener("click", () => {
    fix.disabled = true;
    fix.textContent = "Fixing…";
    fetch("/error-offer/" + encodeURIComponent(id) + "/fix", { method: "POST" }).catch(() => {
      fix.disabled = false;
      fix.textContent = "Fix it";
    });
  });
  const dismiss = document.createElement("button");
  dismiss.type = "button";
  dismiss.className = "error-offer-dismiss";
  dismiss.textContent = "Dismiss";
  dismiss.addEventListener("click", () => {
    fetch("/error-offer/" + encodeURIComponent(id) + "/dismiss", { method: "POST" }).catch(() => {});
    removeErrorOffer(id);
  });
  actions.append(fix, dismiss);
  card.appendChild(actions);

  chatLog.appendChild(card);
  errorOffers.set(id, card);
  pinned = true;
  scrollToLatest(true);
}

function removeErrorOffer(id) {
  const card = errorOffers.get(id);
  if (!card) return;
  errorOffers.delete(id);
  card.remove();
}

// The shim injected into the proxied app reports crashes to us via
// postMessage (it can't reach the yolocoder server directly: it doesn't
// know about it, only about its own parent window). Relay it to the
// backend, which stages it as a card instead of firing a fix on its own.
window.addEventListener("message", (event) => {
  const data = event.data;
  if (
    !data ||
    (data.type !== "APP_RUNTIME_ERROR" &&
      data.type !== "APP_UNHANDLED_REJECTION" &&
      data.type !== "APP_CONSOLE_ERROR")
  )
    return;
  fetch("/client-error", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(data),
  });
});

// The composer is where every session starts, so it starts focused, with
// the send button already reflecting an empty box.
updateSendState();
chatInput.focus();
