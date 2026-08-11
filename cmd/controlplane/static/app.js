// Control plane SPA shell. Vanilla JS, no framework/build step — kept
// simple on purpose (docs/04: thin MVP), but structured so a new section
// is one entry in VIEWS plus one render function, not a rewrite of the
// shell. Routing is just location.hash -> VIEWS lookup.

// Each icon name matches docs/07-glossary.md's What/How/Who/Where
// mental-model mapping (Task=what, Workflow=how, Worker=who,
// Repository=where) — not arbitrary filenames.
const VIEWS = {
  tasks: { label: "Tasks", render: renderTasks, icon: "/images/what.svg" },
  workflows: { label: "Workflows", render: renderWorkflows, icon: "/images/how.svg" },
  workers: { label: "Workers", render: renderWorkers, icon: "/images/who.svg" },
  repositories: { label: "Repositories", render: renderRepositories, icon: "/images/where.svg" },
  pending_approvals: { label: "Pending approvals", render: renderPendingApprovals, icon: "/images/pending-approvals.svg" },
  inbox: { label: "Inbox", render: renderInbox, icon: "/images/inbox.svg" },
  settings: { label: "Settings", render: renderSettings, icon: "/images/settings.svg" },
};

// buildIconSpan renders a themeable icon (app.css's .icon-mask: a CSS
// mask-image, so the SVG's own fill color is irrelevant — background-
// color: currentColor makes it track whatever text color already
// applies, correct in both themes with no separate dark-mode asset).
function buildIconSpan(src, extraClass) {
  const span = document.createElement("span");
  span.className = "icon-mask " + extraClass;
  span.style.setProperty("--icon-src", `url("${src}")`);
  span.setAttribute("aria-hidden", "true");
  return span;
}

const DEFAULT_VIEW = "repositories";

function currentViewID() {
  const hash = location.hash.replace(/^#\/?/, "");
  return VIEWS[hash] ? hash : DEFAULT_VIEW;
}

function renderNav() {
  const list = document.getElementById("nav-list");
  list.innerHTML = "";
  const active = currentViewID();
  for (const [id, view] of Object.entries(VIEWS)) {
    const li = document.createElement("li");
    const a = document.createElement("a");
    a.href = "#/" + id;
    a.className = "nav-item" + (id === active ? " active" : "");

    const contentSpan = document.createElement("span");
    contentSpan.className = "nav-item-content";
    if (view.icon) {
      contentSpan.appendChild(buildIconSpan(view.icon, "nav-icon"));
    }
    const label = document.createElement("span");
    label.textContent = view.label;
    contentSpan.appendChild(label);
    a.appendChild(contentSpan);

    if (id === "inbox" || id === "pending_approvals") {
      const badge = document.createElement("span");
      badge.className = "nav-badge";
      badge.id = id === "inbox" ? "nav-inbox-badge" : "nav-pending-approvals-badge";
      badge.style.display = "none";
      a.appendChild(badge);
    }

    li.appendChild(a);
    list.appendChild(li);
  }
  refreshInboxBadge();
  refreshPendingApprovalsBadge();
}

// refreshInboxBadge fetches the current pending-action count for the nav
// item's badge — called on every renderNav (so it's current no matter
// which view is open) and again after any Inbox action that changes the
// count (resume/cancel), so it doesn't wait for the next navigation to
// catch up. Silently no-ops on failure (leaves whatever count was last
// shown) rather than erroring on views that have nothing to do with the
// Inbox.
async function refreshInboxBadge() {
  const badge = document.getElementById("nav-inbox-badge");
  if (!badge) return;
  let pending;
  try {
    pending = await apiRequest("/api/inbox");
  } catch (err) {
    return;
  }
  const count = (pending || []).length;
  // Re-fetch a fresh reference: an intervening navigation may have
  // re-rendered the nav (and thus this badge element) while this
  // request was in flight.
  const current = document.getElementById("nav-inbox-badge");
  if (!current) return;
  current.textContent = String(count);
  current.style.display = count > 0 ? "" : "none";
}

// refreshPendingApprovalsBadge mirrors refreshInboxBadge exactly, one
// level down (Pending Approvals is the routine-volume counterpart to
// Inbox's exceptions-only queue — see internal/inbox.ListPendingApprovals'
// doc comment for why they're separate lists at all).
async function refreshPendingApprovalsBadge() {
  const badge = document.getElementById("nav-pending-approvals-badge");
  if (!badge) return;
  let pending;
  try {
    pending = await apiRequest("/api/pending-approvals");
  } catch (err) {
    return;
  }
  const count = (pending || []).length;
  const current = document.getElementById("nav-pending-approvals-badge");
  if (!current) return;
  current.textContent = String(count);
  current.style.display = count > 0 ? "" : "none";
}

// activePollTimer is the one live setInterval at any time — a view that
// wants auto-refresh calls startPolling from its own render function;
// renderView clears whatever the previous view left running before
// rendering the next one, so navigating away always stops it (no
// stacked, forgotten timers silently piling up requests in the
// background across view switches).
let activePollTimer = null;

function stopPolling() {
  if (activePollTimer) {
    clearInterval(activePollTimer);
    activePollTimer = null;
  }
}

function startPolling(fn, ms) {
  stopPolling();
  activePollTimer = setInterval(fn, ms);
}

function renderView() {
  const id = currentViewID();
  const view = VIEWS[id];
  const title = document.getElementById("topbar-title");
  title.innerHTML = "";
  if (view.icon) {
    title.appendChild(buildIconSpan(view.icon, "topbar-icon"));
  }
  title.appendChild(document.createTextNode(view.label));
  const content = document.getElementById("content");
  stopPolling();
  content.innerHTML = "";
  renderNav();
  view.render(content);
}

function setupSidebarToggle() {
  const sidebar = document.getElementById("sidebar");
  const toggle = document.getElementById("sidebar-toggle");
  const collapsed = localStorage.getItem("cp-sidebar-collapsed") === "1";
  sidebar.classList.toggle("collapsed", collapsed);
  toggle.addEventListener("click", () => {
    const nowCollapsed = sidebar.classList.toggle("collapsed");
    localStorage.setItem("cp-sidebar-collapsed", nowCollapsed ? "1" : "0");
  });
}

// applyThemeMode sets the "Slate Tech" theme's mode (.sketchpad/ui/themes.html
// — dark is the CSS default, data-mode="light" on <html> is the override).
// The button shows the mode a click would switch TO, not the current one.
function applyThemeMode(mode) {
  if (mode === "light") {
    document.documentElement.setAttribute("data-mode", "light");
  } else {
    document.documentElement.removeAttribute("data-mode");
  }
  const toggle = document.getElementById("theme-toggle");
  toggle.textContent = mode === "light" ? "\u{1F319}" : "☀️";
  toggle.title = mode === "light" ? "Switch to dark theme" : "Switch to light theme";
  toggle.setAttribute("aria-label", toggle.title);
}

function setupThemeToggle() {
  const toggle = document.getElementById("theme-toggle");
  const mode = localStorage.getItem("cp-theme-mode") === "light" ? "light" : "dark";
  applyThemeMode(mode);
  toggle.addEventListener("click", () => {
    const next = document.documentElement.getAttribute("data-mode") === "light" ? "dark" : "light";
    applyThemeMode(next);
    localStorage.setItem("cp-theme-mode", next);
  });
}

window.addEventListener("hashchange", renderView);
window.addEventListener("DOMContentLoaded", () => {
  setupSidebarToggle();
  setupThemeToggle();
  renderView();
  // Badges live in the sidebar, visible regardless of which view is
  // open — they need their own persistent poll, separate from
  // activePollTimer (which stopPolling clears on every view switch).
  // Without this, a badge only updated on navigation (renderNav), so a
  // human sitting on an unrelated view never saw a new Inbox escalation
  // or Pending Approval land until they happened to click away and back.
  setInterval(() => {
    refreshInboxBadge();
    refreshPendingApprovalsBadge();
  }, 5000);
});

// ---- api helpers ----

async function apiRequest(path, opts) {
  const res = await fetch(path, opts);
  if (!res.ok) {
    const text = await res.text();
    throw new Error(text || (res.status + " " + res.statusText));
  }
  if (res.status === 204) return null;
  return res.json();
}

const TEMPORAL_UI_WORKFLOW_BASE = "http://localhost:8080/namespaces/default/workflows/";

function recommendedResumeStep(item) {
  // The escalation's originating step is the safest default: it lets the
  // human make a different choice explicitly instead of silently replaying
  // an earlier agent step or skipping a deterministic tool step.
  return item.from_step || item.step_id || "";
}

function showRunDetails(task) {
  const backdrop = document.createElement("div");
  backdrop.className = "modal-backdrop";
  const dialog = document.createElement("section");
  dialog.className = "modal-dialog";
  dialog.setAttribute("role", "dialog");
  dialog.setAttribute("aria-modal", "true");
  dialog.setAttribute("aria-labelledby", "run-detail-title");
  dialog.innerHTML = `
    <div class="modal-kicker">RUN DETAIL</div>
    <div class="modal-header">
      <div><h2 id="run-detail-title">${task.run_id || "Run"}</h2><p class="modal-subtitle">Loading the projection timeline…</p></div>
      <button class="icon-button modal-close" aria-label="Close run detail">×</button>
    </div>
    <div class="modal-content"><div class="loading-state">Loading…</div></div>
  `;
  backdrop.appendChild(dialog);
  document.body.appendChild(backdrop);
  const close = () => backdrop.remove();
  dialog.querySelector(".modal-close").addEventListener("click", close);
  backdrop.addEventListener("click", (event) => { if (event.target === backdrop) close(); });
  const onKey = (event) => { if (event.key === "Escape") { close(); document.removeEventListener("keydown", onKey); } };
  document.addEventListener("keydown", onKey);

  apiRequest("/api/runs/" + encodeURIComponent(task.run_id))
    .then((detail) => {
      const events = detail.events || [];
      const content = dialog.querySelector(".modal-content");
      const subtitle = dialog.querySelector(".modal-subtitle");
      subtitle.textContent = [task.status, task.target_repo, task.workflow].filter(Boolean).join(" · ");

      const header = document.createElement("div");
      header.className = "run-detail-summary";
      const status = document.createElement("span");
      status.className = "badge " + (task.status === "FAILED" ? "failed" : task.status === "COMPLETED" ? "enabled" : "disabled");
      status.textContent = task.status || "UNKNOWN";
      header.appendChild(status);
      if (task.failure_reason) {
        const failure = document.createElement("p");
        failure.className = "text-negative modal-failure";
        failure.textContent = task.failure_reason;
        header.appendChild(failure);
      }
      content.replaceChildren(header);

      const artifacts = document.createElement("div");
      artifacts.className = "modal-artifacts";
      const temporalLink = document.createElement("a");
      temporalLink.href = TEMPORAL_UI_WORKFLOW_BASE + encodeURIComponent(task.run_id);
      temporalLink.target = "_blank";
      temporalLink.rel = "noreferrer";
      temporalLink.textContent = "Open Temporal history";
      artifacts.appendChild(temporalLink);
      content.appendChild(artifacts);

      const timeline = document.createElement("div");
      timeline.className = "run-timeline";
      if (events.length === 0) {
        timeline.textContent = "No projected events are available for this Run.";
      }
      for (const event of events) {
        const item = document.createElement("article");
        item.className = "timeline-event";
        const top = document.createElement("div");
        top.className = "timeline-event-top";
        const transition = document.createElement("strong");
        transition.textContent = `${event.from_step || "(start)"} → ${event.to_step}`;
        top.appendChild(transition);
        const occurred = document.createElement("time");
        occurred.dateTime = event.occurred_at;
        occurred.textContent = new Date(event.occurred_at).toLocaleString();
        top.appendChild(occurred);
        item.appendChild(top);
        const meta = document.createElement("div");
        meta.className = "list-row-meta";
        meta.textContent = [event.step_id, event.outcome, event.attempt_number ? `attempt ${event.attempt_number}` : ""].filter(Boolean).join(" · ");
        if (meta.textContent) item.appendChild(meta);
        if (event.failure_reason) {
          const failure = document.createElement("p");
          failure.className = "text-negative";
          failure.textContent = event.failure_reason;
          item.appendChild(failure);
        }
        if (event.summary) {
          const summary = document.createElement("p");
          summary.className = "timeline-summary";
          summary.textContent = event.summary;
          item.appendChild(summary);
        }
        timeline.appendChild(item);
      }
      content.appendChild(timeline);

      const actions = document.createElement("div");
      actions.className = "modal-actions";
      if (task.status === "FAILED") {
        const retry = document.createElement("button");
        retry.className = "primary";
        retry.textContent = "Retry task";
        retry.addEventListener("click", async () => {
          if (!confirm("Start a fresh Run for this failed Task? The previous Run will remain available in the history.")) return;
          retry.disabled = true;
          retry.textContent = "Starting…";
          try {
            const created = await apiRequest("/api/tasks/retry", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ task_id: task.task_id }),
            });
            close();
            alert(`Retry started as Run ${created.run_id}.`);
            location.hash = "#/tasks";
            window.dispatchEvent(new HashChangeEvent("hashchange"));
          } catch (err) {
            retry.disabled = false;
            retry.textContent = "Retry task";
            alert(String(err.message || err));
          }
        });
        actions.appendChild(retry);
      }
      const done = document.createElement("button");
      done.className = "link";
      done.textContent = "Close";
      done.addEventListener("click", close);
      actions.appendChild(done);
      content.appendChild(actions);
    })
    .catch((err) => {
      dialog.querySelector(".modal-content").textContent = String(err.message || err);
    });
}

// showWorkflowGraphModal is the Workflows view's "View Cytoscape"/"View
// BPMN" row toggles — same modal chrome as showRunDetails (backdrop,
// Escape/click-outside/close-button dismissal), but wide: a graph needs
// real canvas space, not a text-sized dialog (docs/06's own framing for
// why these renderers get a dedicated canvas area at all). kind is
// "cytoscape" or "bpmn"; info is one /api/workflows entry (its .path is
// what buildGraphViewShell's fixedPath renders).
function showWorkflowGraphModal(kind, info) {
  const backdrop = document.createElement("div");
  backdrop.className = "modal-backdrop";
  const dialog = document.createElement("section");
  dialog.className = "modal-dialog modal-dialog-wide";
  dialog.setAttribute("role", "dialog");
  dialog.setAttribute("aria-modal", "true");
  dialog.setAttribute("aria-labelledby", "workflow-graph-title");
  const kindLabel = kind === "bpmn" ? "BPMN diagram" : "Cytoscape graph";
  dialog.innerHTML = `
    <div class="modal-kicker">${kindLabel.toUpperCase()}</div>
    <div class="modal-header">
      <div><h2 id="workflow-graph-title">${info.workflow || info.path}</h2><p class="modal-subtitle">${info.path}</p></div>
      <button class="icon-button modal-close" aria-label="Close ${kindLabel}">×</button>
    </div>
    <div class="modal-content"></div>
  `;
  backdrop.appendChild(dialog);
  document.body.appendChild(backdrop);
  const close = () => backdrop.remove();
  dialog.querySelector(".modal-close").addEventListener("click", close);
  backdrop.addEventListener("click", (event) => { if (event.target === backdrop) close(); });
  const onKey = (event) => { if (event.key === "Escape") { close(); document.removeEventListener("keydown", onKey); } };
  document.addEventListener("keydown", onKey);

  const content = dialog.querySelector(".modal-content");
  if (kind === "bpmn") {
    renderWorkflowV4(content, info.path);
  } else {
    renderWorkflowV3(content, info.path);
  }
}

// populateWorkflowSelect fills a <select> from /api/workflows'
// WorkflowInfo objects — shared by the Repositories view (add form +
// per-row edit) and the Work view (task create form).
function populateWorkflowSelect(select, infos, selected) {
  select.innerHTML = "";
  const none = document.createElement("option");
  none.value = "";
  none.textContent = "(none)";
  select.appendChild(none);
  for (const info of infos) {
    const option = document.createElement("option");
    option.value = info.path;
    option.textContent = info.path + (info.valid ? "" : "  (invalid)");
    select.appendChild(option);
  }
  select.value = selected || "";
}

// populateHarnessSelect fills a <select> from /api/harnesses (backed by
// internal/workers.KnownHarnesses) — the Workers view's harness field, so
// only a registered identifier can ever be entered (found live: a Worker
// saved with a plausible-but-wrong harness string only failed once a
// real Run reached it, after real work already happened). No "(none)"
// option, unlike populateWorkflowSelect above: harness is required, and
// defaulting to the first real entry means a human never has to make a
// meaningless choice just to get past validation.
function populateHarnessSelect(select, harnesses, selected) {
  select.innerHTML = "";
  for (const harness of harnesses) {
    const option = document.createElement("option");
    option.value = harness;
    option.textContent = harness;
    select.appendChild(option);
  }
  select.value = selected || harnesses[0] || "";
}

// makeFormCardCollapsible turns a "create new item" card (Repositories,
// Workers, Tasks — always .card-header h2 followed by the form itself)
// into a collapsible one: everything after the header moves into a
// .card-body that a header toggle button shows/hides. State persists per
// view in localStorage (same mechanism as the sidebar collapse), so a
// human who collapses "Add worker" to see more rows below doesn't have
// it spring back open on every view switch.
function makeFormCardCollapsible(formCard, storageKey) {
  const header = formCard.querySelector(".card-header");
  const body = document.createElement("div");
  body.className = "card-body";
  for (const el of Array.from(formCard.children)) {
    if (el !== header) body.appendChild(el);
  }
  formCard.appendChild(body);

  const toggle = document.createElement("button");
  toggle.type = "button";
  toggle.className = "card-toggle";
  toggle.setAttribute("aria-label", "Toggle form");
  header.appendChild(toggle);

  function apply(collapsed) {
    formCard.classList.toggle("card-collapsed", collapsed);
    toggle.textContent = collapsed ? "▸" : "▾";
    toggle.setAttribute("aria-expanded", String(!collapsed));
  }
  apply(localStorage.getItem(storageKey) !== "0");

  toggle.addEventListener("click", () => {
    const collapsed = !formCard.classList.contains("card-collapsed");
    localStorage.setItem(storageKey, collapsed ? "1" : "0");
    apply(collapsed);
  });
}

// ---- YAML viewer (Workflows view's "View YAML" panel) ----
//
// A small hand-rolled line tokenizer, not a real YAML parser: good enough
// for a read-only viewer of checked-in Workflow Definitions, without
// vendoring a highlighting library into a project whose static assets
// ship as one embedded, dependency-free bundle (see cmd/controlplane/
// static/vendor/ — everything there is a visualization lib with no
// lightweight syntax-highlighter equivalent worth adding for this one
// view). Flow collections (`[a, b]`, `{a: b}`) and multi-line block
// scalars (`|`, `>`) are intentionally left uncolored rather than
// mis-highlighted — correctness of "what does this say" matters more
// here than full coverage.

// renderYamlSource builds a line-numbered <ol> from raw YAML text — one
// <li> per source line (an empty trailing line from the file's final
// newline is dropped so the numbering matches what's on disk) so line
// numbers come from list markers instead of a hand-maintained gutter.
function renderYamlSource(content) {
  const ol = document.createElement("ol");
  ol.className = "yaml-lines";
  const lines = content.split("\n");
  if (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  for (const line of lines) {
    const li = document.createElement("li");
    for (const tok of tokenizeYamlLine(line)) {
      if (tok.cls) {
        const span = document.createElement("span");
        span.className = tok.cls;
        span.textContent = tok.text;
        li.appendChild(span);
      } else {
        li.appendChild(document.createTextNode(tok.text));
      }
    }
    ol.appendChild(li);
  }
  return ol;
}

const YAML_SCALAR_RE = /^(true|false|null|~|yes|no|-?\d+(\.\d+)?)$/i;

// tokenizeYamlLine splits one line into {text, cls} tokens: leading
// indentation, any "- " list markers, then either a comment, a "key:"
// pair, or a bare value.
function tokenizeYamlLine(line) {
  const tokens = [];

  const indent = line.match(/^\s*/)[0];
  let rest = line.slice(indent.length);
  if (indent) tokens.push({ text: indent });
  if (rest === "") return tokens;

  while (rest === "-" || rest.startsWith("- ")) {
    const marker = rest === "-" ? "-" : "- ";
    tokens.push({ text: marker, cls: "yaml-punct" });
    rest = rest.slice(marker.length);
  }
  if (rest === "") return tokens;

  if (rest.startsWith("#")) {
    tokens.push({ text: rest, cls: "yaml-comment" });
    return tokens;
  }

  if (rest.startsWith("---") || rest.startsWith("...")) {
    tokens.push({ text: rest, cls: "yaml-punct" });
    return tokens;
  }

  const key = matchYamlKey(rest);
  if (key !== null) {
    tokens.push({ text: key, cls: "yaml-key" });
    tokens.push({ text: ":", cls: "yaml-punct" });
    rest = rest.slice(key.length + 1);
  }

  tokens.push(...tokenizeYamlValue(rest));
  return tokens;
}

// matchYamlKey returns the key text (quotes included, if quoted) when
// rest starts with a "key:" pair — the colon must be followed by
// whitespace or end-of-line, so a bare value containing ":" (a URL, a
// "host:port" string) on its own line isn't mistaken for a key.
function matchYamlKey(rest) {
  let m = rest.match(/^"[^"]*"(?=:(\s|$))/);
  if (m) return m[0];
  m = rest.match(/^'[^']*'(?=:(\s|$))/);
  if (m) return m[0];
  m = rest.match(/^[^:#\[\]{}\s][^:]*?(?=:(\s|$))/);
  if (m) return m[0];
  return null;
}

// tokenizeYamlValue handles the text after "key: " (or a whole bare
// value line): leading spaces, an optional trailing "# comment" (only
// outside quotes), then the scalar itself.
function tokenizeYamlValue(rest) {
  if (rest === "") return [];
  const tokens = [];
  const leadingSpace = rest.match(/^\s*/)[0];
  if (leadingSpace) tokens.push({ text: leadingSpace });
  let value = rest.slice(leadingSpace.length);
  if (value === "") return tokens;

  const commentIdx = findYamlCommentStart(value);
  let commentText = "";
  if (commentIdx !== -1) {
    commentText = value.slice(commentIdx);
    value = value.slice(0, commentIdx);
  }

  if (value !== "") tokens.push(...tokenizeYamlScalar(value));
  if (commentText) tokens.push({ text: commentText, cls: "yaml-comment" });
  return tokens;
}

function findYamlCommentStart(value) {
  let inSingle = false;
  let inDouble = false;
  for (let i = 0; i < value.length; i++) {
    const c = value[i];
    if (c === "'" && !inDouble) inSingle = !inSingle;
    else if (c === '"' && !inSingle) inDouble = !inDouble;
    else if (c === "#" && !inSingle && !inDouble && (i === 0 || /\s/.test(value[i - 1]))) {
      return i;
    }
  }
  return -1;
}

function tokenizeYamlScalar(value) {
  const isQuoted = (value.startsWith('"') && value.endsWith('"') && value.length >= 2) ||
    (value.startsWith("'") && value.endsWith("'") && value.length >= 2);
  if (isQuoted) return [{ text: value, cls: "yaml-string" }];

  const trimmed = value.replace(/\s+$/, "");
  const trailingSpace = value.slice(trimmed.length);
  if (YAML_SCALAR_RE.test(trimmed)) {
    const out = [{ text: trimmed, cls: "yaml-scalar" }];
    if (trailingSpace) out.push({ text: trailingSpace });
    return out;
  }
  return [{ text: value }];
}

// ---- shared graph-view scaffold (Cytoscape/BPMN, opened from Workflows) ----

// buildGraphViewShell is the common chrome both graph visualizations
// (Cytoscape, BPMN-styled — see the Workflows view's "View
// Cytoscape"/"View BPMN" row toggles) share: an error banner, a canvas
// area sized for a real graph (not a chat-bubble-sized card), and a
// legend row. Each caller supplies its own render(canvasEl, graph) —
// everything about *how* the graph is drawn and panned/zoomed is
// renderer-specific by design, but the surrounding chrome shouldn't be
// reinvented twice.
//
// opts.layouts is a list of {id, label, render(canvas, graph)} — a
// renderer can register several layout algorithms rather than one, so
// they're comparable live via the layout dropdown without leaving the
// view. The graph itself (including cluster membership — see
// cmd/controlplane's computeClusters) is fetched once and reused across
// every layout switch; only re-rendering is re-run, not a new API call.
//
// opts.fixedPath renders exactly one Workflow Definition (the Workflows
// view already knows which row's toggle was clicked) — no workflow
// picker at all, unlike the standalone-page shape this shell used to
// only have (back when workflow_v1/v2/v5 each had their own always-
// pick-a-workflow nav item; see docs/06's decision to prune those and
// access v3/v4 from the Workflows view instead).
function buildGraphViewShell(container, opts) {
  const wrap = document.createElement("div");
  wrap.className = "graph-view-shell";

  const errorBanner = document.createElement("div");
  errorBanner.className = "error-banner";
  errorBanner.style.display = "none";
  wrap.appendChild(errorBanner);
  function showError(err) {
    errorBanner.textContent = String(err.message || err);
    errorBanner.style.display = "block";
  }

  const layoutOptionsHTML = opts.layouts
    .map((l) => `<option value="${l.id}">${l.label}</option>`)
    .join("");

  const card = document.createElement("div");
  card.className = "card graph-card";
  card.innerHTML = `
    <div class="card-header">
      <h2>${opts.title}</h2>
      ${opts.fixedPath ? "" : '<select class="graph-workflow-select"><option value="">(loading...)</option></select>'}
      <select class="graph-layout-select">${layoutOptionsHTML}</select>
    </div>
    <div class="graph-canvas-wrap"><div class="graph-canvas"></div></div>
    <div class="graph-legend">
      <span class="graph-legend-item"><span class="graph-legend-swatch" style="color:var(--color-node-tool)"></span>tool step</span>
      <span class="graph-legend-item"><span class="graph-legend-swatch" style="color:var(--color-node-agent)"></span>agent step</span>
      <span class="graph-legend-item"><span class="graph-legend-swatch" style="color:var(--color-node-terminal)"></span>terminal state</span>
      <span class="graph-legend-item"><span class="graph-legend-swatch" style="color:var(--color-cluster-ring)"></span>back/forth loop (cluster)</span>
      <span class="graph-legend-item">${opts.interactionHint}</span>
    </div>
  `;
  wrap.appendChild(card);
  container.appendChild(wrap);

  const workflowSelect = card.querySelector(".graph-workflow-select");
  const layoutSelect = card.querySelector(".graph-layout-select");
  const canvas = card.querySelector(".graph-canvas");

  let currentGraph = null;

  if (opts.fixedPath) {
    loadGraph(opts.fixedPath);
  } else {
    apiRequest("/api/workflows")
      .then((infos) => {
        infos = infos || [];
        workflowSelect.innerHTML = "";
        for (const info of infos) {
          const option = document.createElement("option");
          option.value = info.path;
          option.textContent = info.path;
          workflowSelect.appendChild(option);
        }
        if (infos.length > 0) {
          loadGraph(infos[0].path);
        } else {
          showError(new Error("No workflow definitions found."));
        }
      })
      .catch(showError);
    workflowSelect.addEventListener("change", () => loadGraph(workflowSelect.value));
  }

  layoutSelect.addEventListener("change", renderCurrentLayout);

  function loadGraph(path) {
    if (!path) return;
    canvas.innerHTML = "";
    apiRequest("/api/workflow-graph?path=" + encodeURIComponent(path))
      .then((graph) => {
        currentGraph = graph;
        renderCurrentLayout();
      })
      .catch(showError);
  }

  function renderCurrentLayout() {
    if (!currentGraph) return;
    canvas.innerHTML = "";
    const layout = opts.layouts.find((l) => l.id === layoutSelect.value) || opts.layouts[0];
    try {
      // A layout's render may be async (ELK's layout() is Promise-based) —
      // handle both without every synchronous layout needing to wrap
      // itself in one just to match a common async signature.
      const result = layout.render(canvas, currentGraph);
      if (result && typeof result.catch === "function") {
        result.catch(showError);
      }
    } catch (err) {
      showError(err);
    }
  }

  return { canvas, showError };
}

// ---- repositories view ----

function renderRepositories(container) {
  const wrap = document.createElement("div");

  const errorBanner = document.createElement("div");
  errorBanner.className = "error-banner";
  errorBanner.style.display = "none";
  wrap.appendChild(errorBanner);

  function showError(err) {
    errorBanner.textContent = String(err.message || err);
    errorBanner.style.display = "block";
  }
  function clearError() {
    errorBanner.style.display = "none";
  }

  const formCard = document.createElement("div");
  formCard.className = "card";
  formCard.innerHTML = `
    <div class="card-header">
      <h2>Add GitHub repository</h2>
    </div>
    <div class="field-stack">
      <div class="field">
        <label for="rf-identity">Canonical identity</label>
        <input id="rf-identity" type="text" placeholder="github.com/owner/repo">
      </div>
      <div class="field">
        <label for="rf-test-command">Test command</label>
        <input id="rf-test-command" type="text" placeholder="e.g. node --check script.js">
      </div>
      <div class="field">
        <label for="rf-workflow">Default workflow</label>
        <select id="rf-workflow">
          <option value="">(none)</option>
        </select>
      </div>
      <div class="field">
        <label for="rf-worktree-root">Worktree root override</label>
        <input id="rf-worktree-root" type="text" placeholder="optional — leave blank to use the Settings default">
      </div>
      <button class="primary" id="rf-submit">+ Add repository</button>
    </div>
    <p class="hint">GitHub is the only managed provider in this release.</p>
  `;
  makeFormCardCollapsible(formCard, "cp-collapse-repo-form");
  wrap.appendChild(formCard);

  // Fetched once, reused both by the add-form's select and by every
  // row's edit-mode select (populateWorkflowSelect, module-scoped below
  // since the Work view's create form also needs it) — one round trip
  // regardless of how many rows get edited. /api/workflows returns
  // WorkflowInfo objects (see cmd/controlplane's Workflows view), not
  // bare paths — only .path/.valid are used here.
  let workflows = [];
  const workflowsReady = apiRequest("/api/workflows")
    .then((infos) => {
      workflows = infos || [];
      populateWorkflowSelect(document.getElementById("rf-workflow"), workflows, "");
    })
    .catch(showError);

  const listCard = document.createElement("div");
  listCard.className = "card";
  const listHeader = document.createElement("div");
  listHeader.className = "card-header";
  listHeader.innerHTML = `<h2 id="repo-count">Managed repositories</h2>`;
  listCard.appendChild(listHeader);
  const list = document.createElement("div");
  list.className = "list";
  listCard.appendChild(list);
  wrap.appendChild(listCard);

  container.appendChild(wrap);

  async function refresh() {
    clearError();
    let repos;
    try {
      repos = await apiRequest("/api/repositories");
    } catch (err) {
      showError(err);
      return;
    }
    renderList(repos || []);
  }

  function renderList(repos) {
    const enabledCount = repos.filter((r) => r.enabled).length;
    document.getElementById("repo-count").textContent =
      `Managed repositories — ${repos.length} total, ${enabledCount} enabled`;

    list.innerHTML = "";
    if (repos.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      empty.textContent = "No repositories registered yet.";
      list.appendChild(empty);
      return;
    }

    for (const repo of repos) {
      list.appendChild(buildViewRow(repo));
    }
  }

  function buildViewRow(repo) {
    const row = document.createElement("div");
    row.className = "list-row";

    const main = document.createElement("div");
    main.className = "list-row-main";
    const name = document.createElement("div");
    name.className = "list-row-name";
    name.textContent = repo.name;
    main.appendChild(name);
    const meta = document.createElement("div");
    meta.className = "list-row-meta";
    meta.textContent = (repo.default_workflow || "no default workflow") +
      (repo.test_command ? "  ·  " + repo.test_command : "") +
      (repo.worktree_root ? "  ·  root: " + repo.worktree_root : "");
    main.appendChild(meta);
    row.appendChild(main);

    const actions = document.createElement("div");
    actions.className = "list-row-actions";

    const editBtn = document.createElement("button");
    editBtn.className = "link";
    editBtn.textContent = "Edit";
    editBtn.addEventListener("click", () => {
      row.replaceWith(buildEditRow(repo));
    });
    actions.appendChild(editBtn);

    const deleteBtn = document.createElement("button");
    deleteBtn.className = "link";
    deleteBtn.textContent = "Delete";
    deleteBtn.addEventListener("click", async () => {
      if (!confirm(`Delete ${repo.name}? This can't be undone.`)) return;
      clearError();
      deleteBtn.disabled = true;
      try {
        await apiRequest("/api/repositories/delete", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: repo.name }),
        });
        await refresh();
      } catch (err) {
        showError(err);
        deleteBtn.disabled = false;
      }
    });
    actions.appendChild(deleteBtn);

    const badge = document.createElement("span");
    badge.className = "badge " + (repo.enabled ? "enabled" : "disabled");
    badge.textContent = repo.enabled ? "Enabled" : "Disabled";
    actions.appendChild(badge);

    const toggle = document.createElement("button");
    toggle.className = "link";
    toggle.textContent = repo.enabled ? "Disable" : "Enable";
    toggle.addEventListener("click", async () => {
      clearError();
      toggle.disabled = true;
      try {
        await apiRequest("/api/repositories/" + (repo.enabled ? "disable" : "enable"), {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name: repo.name }),
        });
        await refresh();
      } catch (err) {
        showError(err);
        toggle.disabled = false;
      }
    });
    actions.appendChild(toggle);

    row.appendChild(actions);
    return row;
  }

  function buildEditRow(repo) {
    const row = document.createElement("div");
    row.className = "list-row list-row-editing";

    const main = document.createElement("div");
    main.className = "list-row-main";
    const name = document.createElement("div");
    name.className = "list-row-name";
    name.textContent = repo.name;
    main.appendChild(name);

    const editForm = document.createElement("div");
    editForm.className = "field-stack";
    editForm.innerHTML = `
      <div class="field">
        <label>Test command</label>
        <input type="text" class="edit-test-command">
      </div>
      <div class="field">
        <label>Default workflow</label>
        <select class="edit-workflow"></select>
      </div>
      <div class="field">
        <label>Worktree root override</label>
        <input type="text" class="edit-worktree-root" placeholder="optional — leave blank to use the Settings default">
      </div>
    `;
    main.appendChild(editForm);
    row.appendChild(main);

    const testCommandInput = editForm.querySelector(".edit-test-command");
    testCommandInput.value = repo.test_command || "";
    const worktreeRootInput = editForm.querySelector(".edit-worktree-root");
    worktreeRootInput.value = repo.worktree_root || "";
    const workflowSelect = editForm.querySelector(".edit-workflow");
    workflowsReady.then(() => populateWorkflowSelect(workflowSelect, workflows, repo.default_workflow));

    const actions = document.createElement("div");
    actions.className = "list-row-actions";

    const saveBtn = document.createElement("button");
    saveBtn.className = "primary";
    saveBtn.textContent = "Save";
    saveBtn.addEventListener("click", async () => {
      clearError();
      saveBtn.disabled = true;
      try {
        await apiRequest("/api/repositories/update", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name: repo.name,
            test_command: testCommandInput.value.trim(),
            default_workflow: workflowSelect.value,
            worktree_root: worktreeRootInput.value.trim(),
          }),
        });
        await refresh();
      } catch (err) {
        showError(err);
        saveBtn.disabled = false;
      }
    });
    actions.appendChild(saveBtn);

    const cancelBtn = document.createElement("button");
    cancelBtn.className = "link";
    cancelBtn.textContent = "Cancel";
    cancelBtn.addEventListener("click", () => {
      row.replaceWith(buildViewRow(repo));
    });
    actions.appendChild(cancelBtn);

    row.appendChild(actions);
    return row;
  }

  document.getElementById("rf-submit").addEventListener("click", async () => {
    clearError();
    const identity = document.getElementById("rf-identity").value.trim();
    const testCommand = document.getElementById("rf-test-command").value.trim();
    const workflow = document.getElementById("rf-workflow").value.trim();
    const worktreeRoot = document.getElementById("rf-worktree-root").value.trim();
    if (!identity) {
      showError(new Error("Canonical identity is required."));
      return;
    }
    const submit = document.getElementById("rf-submit");
    submit.disabled = true;
    try {
      await apiRequest("/api/repositories", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          identity: identity,
          test_command: testCommand,
          default_workflow: workflow,
          worktree_root: worktreeRoot,
        }),
      });
      document.getElementById("rf-identity").value = "";
      document.getElementById("rf-test-command").value = "";
      document.getElementById("rf-workflow").value = "";
      document.getElementById("rf-worktree-root").value = "";
      await refresh();
    } catch (err) {
      showError(err);
    } finally {
      submit.disabled = false;
    }
  });

  refresh();
}

// ---- workflows view ----

function renderWorkflows(container) {
  const wrap = document.createElement("div");

  const errorBanner = document.createElement("div");
  errorBanner.className = "error-banner";
  errorBanner.style.display = "none";
  wrap.appendChild(errorBanner);

  function showError(err) {
    errorBanner.textContent = String(err.message || err);
    errorBanner.style.display = "block";
  }
  function clearError() {
    errorBanner.style.display = "none";
  }

  const listCard = document.createElement("div");
  listCard.className = "card";
  const header = document.createElement("div");
  header.className = "card-header";
  header.innerHTML = `<h2 id="wf-count">Workflow definitions</h2>`;
  listCard.appendChild(header);
  const list = document.createElement("div");
  list.className = "list";
  listCard.appendChild(list);
  wrap.appendChild(listCard);

  const hint = document.createElement("p");
  hint.className = "hint";
  hint.textContent = "Steps/DAG are read-only: scanned from workflows/ on disk, edited as YAML, " +
    "checked into git. Which Worker plays each role is not — assign it below; it takes effect on the " +
    "next Run, no YAML edit needed.";
  wrap.appendChild(hint);

  container.appendChild(wrap);

  // Fetched once, reused by every row's role picker — one round trip
  // regardless of how many roles across how many workflows need a
  // <select>.
  let allWorkers = [];
  const workersReady = apiRequest("/api/workers")
    .then((all) => {
      allWorkers = all || [];
    })
    .catch(showError);

  async function refresh() {
    clearError();
    let infos;
    try {
      infos = await apiRequest("/api/workflows");
    } catch (err) {
      showError(err);
      return;
    }
    renderList(infos || []);
  }

  function renderList(infos) {
    const validCount = infos.filter((i) => i.valid).length;
    document.getElementById("wf-count").textContent =
      `Workflow definitions — ${infos.length} total, ${validCount} valid`;

    list.innerHTML = "";
    if (infos.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      empty.textContent = "No workflow definitions found.";
      list.appendChild(empty);
      return;
    }

    for (const info of infos) {
      list.appendChild(buildWorkflowRow(info));
    }
  }

  function buildWorkflowRow(info) {
    const row = document.createElement("div");
    row.className = "list-row list-row-workflow";

    const rowHeader = document.createElement("div");
    rowHeader.className = "list-row-header";

    const main = document.createElement("div");
    main.className = "list-row-main";

    const name = document.createElement("div");
    name.className = "list-row-name";
    name.textContent = info.workflow ? `${info.workflow} (v${info.version})` : info.path;
    main.appendChild(name);

    const meta = document.createElement("div");
    meta.className = "list-row-meta";
    const bits = [info.path];
    if (info.step_count) bits.push(info.step_count + " steps");
    if (info.has_trigger) bits.push("triggered");
    meta.textContent = bits.join("  ·  ");
    main.appendChild(meta);

    if (!info.valid && info.errors && info.errors.length > 0) {
      const errList = document.createElement("div");
      errList.className = "list-row-meta text-negative";
      errList.textContent = info.errors.join("; ");
      main.appendChild(errList);
    }

    if (info.roles && info.roles.length > 0) {
      main.appendChild(buildRolesSection(info));
    }

    rowHeader.appendChild(main);

    const actions = document.createElement("div");
    actions.className = "list-row-actions";

    const sourceToggle = document.createElement("button");
    sourceToggle.className = "link";
    sourceToggle.textContent = "View YAML";
    actions.appendChild(sourceToggle);

    const cytoscapeToggle = document.createElement("button");
    cytoscapeToggle.className = "link";
    cytoscapeToggle.textContent = "View Cytoscape";
    cytoscapeToggle.addEventListener("click", () => showWorkflowGraphModal("cytoscape", info));
    actions.appendChild(cytoscapeToggle);

    const bpmnToggle = document.createElement("button");
    bpmnToggle.className = "link";
    bpmnToggle.textContent = "View BPMN";
    bpmnToggle.addEventListener("click", () => showWorkflowGraphModal("bpmn", info));
    actions.appendChild(bpmnToggle);

    const badge = document.createElement("span");
    badge.className = "badge " + (info.valid ? "valid" : "invalid");
    badge.textContent = info.valid ? "Valid" : "Invalid";
    actions.appendChild(badge);
    rowHeader.appendChild(actions);

    row.appendChild(rowHeader);
    row.appendChild(buildYamlSourceSection(info, sourceToggle));

    return row;
  }

  // buildYamlSourceSection is the Workflows view's read-only YAML viewer —
  // collapsed by default, fetched from /api/workflow-source only on first
  // expand (not for every row up front — some deployments could have many
  // Workflow Definitions, and most will never be opened in a session).
  // Read-only by construction: line-numbered <li> text content, not a
  // <textarea> or contenteditable — YAML changes still go through git,
  // per docs/04.
  function buildYamlSourceSection(info, toggle) {
    const wrap = document.createElement("div");
    wrap.className = "yaml-source-wrap collapsed";
    const box = document.createElement("div");
    box.className = "yaml-source";
    const status = document.createElement("div");
    status.className = "yaml-source-status";
    box.appendChild(status);
    wrap.appendChild(box);

    let loaded = false;
    toggle.addEventListener("click", async () => {
      const collapsed = wrap.classList.contains("collapsed");
      if (!collapsed) {
        wrap.classList.add("collapsed");
        toggle.textContent = "View YAML";
        return;
      }
      wrap.classList.remove("collapsed");
      toggle.textContent = "Hide YAML";
      if (loaded) return;
      status.textContent = "Loading…";
      try {
        const res = await apiRequest("/api/workflow-source?path=" + encodeURIComponent(info.path));
        box.innerHTML = "";
        box.appendChild(renderYamlSource(res.content));
        loaded = true;
      } catch (err) {
        status.textContent = "Failed to load: " + (err.message || err);
      }
    });

    return wrap;
  }

  // buildRolesSection renders one row per declared role — a label plus a
  // Worker <select> defaulted to the role's current assignment (empty
  // means "declared but unassigned," the same state taskintake.Submit
  // refuses to start a Run over). Each picker commits independently on
  // change, no separate edit-mode/Save step needed (unlike Repositories'
  // row, which batches several fields into one edit).
  function buildRolesSection(info) {
    const section = document.createElement("div");
    section.className = "role-assignments";

    for (const role of info.roles) {
      const roleRow = document.createElement("div");
      roleRow.className = "role-assignment-row";

      const label = document.createElement("span");
      label.className = "role-assignment-label";
      label.textContent = role.name;
      roleRow.appendChild(label);

      const select = document.createElement("select");
      select.className = "role-assignment-select";
      select.innerHTML = `<option value="">(unassigned)</option>`;
      workersReady.then(() => {
        for (const worker of allWorkers) {
          const opt = document.createElement("option");
          opt.value = worker.id;
          opt.textContent = `${worker.name} (${worker.harness}/${worker.model})`;
          select.appendChild(opt);
        }
        select.value = role.worker_id || "";
      });
      select.addEventListener("change", async () => {
        clearError();
        select.disabled = true;
        try {
          if (select.value) {
            await apiRequest("/api/role-assignments", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({
                workflow: info.workflow,
                role: role.name,
                worker_id: parseInt(select.value, 10),
              }),
            });
          } else {
            // "(unassigned)" — a real state, distinct from posting a
            // worker_id of 0 (which would just fail role_assignments'
            // foreign key).
            await apiRequest("/api/role-assignments/delete", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ workflow: info.workflow, role: role.name }),
            });
          }
        } catch (err) {
          showError(err);
        } finally {
          select.disabled = false;
        }
      });
      roleRow.appendChild(select);
      section.appendChild(roleRow);
    }

    return section;
  }

  refresh();
}

// ---- workers view ----

function renderWorkers(container) {
  const wrap = document.createElement("div");

  const errorBanner = document.createElement("div");
  errorBanner.className = "error-banner";
  errorBanner.style.display = "none";
  wrap.appendChild(errorBanner);

  function showError(err) {
    errorBanner.textContent = String(err.message || err);
    errorBanner.style.display = "block";
  }
  function clearError() {
    errorBanner.style.display = "none";
  }

  const formCard = document.createElement("div");
  formCard.className = "card";
  formCard.innerHTML = `
    <div class="card-header">
      <h2>Add worker</h2>
    </div>
    <div class="field-stack">
      <div class="field">
        <label for="wf-name">Name</label>
        <input id="wf-name" type="text" placeholder="e.g. Sonnet — high effort">
      </div>
      <div class="field">
        <label for="wf-harness">Harness</label>
        <select id="wf-harness"><option value="">(loading...)</option></select>
      </div>
      <div class="field">
        <label for="wf-model">Model</label>
        <input id="wf-model" type="text" placeholder="e.g. sonnet">
      </div>
      <div class="field">
        <label for="wf-effort">Effort</label>
        <input id="wf-effort" type="text" placeholder="e.g. low, medium, high (optional)">
      </div>
      <button class="primary" id="wf-submit">+ Add worker</button>
    </div>
  `;
  makeFormCardCollapsible(formCard, "cp-collapse-worker-form");
  wrap.appendChild(formCard);

  const listCard = document.createElement("div");
  listCard.className = "card";
  const header = document.createElement("div");
  header.className = "card-header";
  header.innerHTML = `<h2 id="worker-count">Workers</h2>`;
  listCard.appendChild(header);
  const list = document.createElement("div");
  list.className = "list";
  listCard.appendChild(list);
  wrap.appendChild(listCard);

  const hint = document.createElement("p");
  hint.className = "hint";
  hint.textContent = "A Worker is a (harness, model, effort) triad, independent of any Workflow. " +
    "Assign one to a role from the Workflows view — editing a Worker here immediately affects every " +
    "assignment pointing at it.";
  wrap.appendChild(hint);

  container.appendChild(wrap);

  // Fetched once and reused by both the add-form select and every
  // per-row edit-form select (populateHarnessSelect), same pattern as
  // the Inbox/Work views' workflowsReady — one fetch, not one per row.
  let harnesses = [];
  const harnessesReady = apiRequest("/api/harnesses")
    .then((list) => {
      harnesses = list || [];
      populateHarnessSelect(document.getElementById("wf-harness"), harnesses, "");
    })
    .catch(showError);

  async function refresh() {
    clearError();
    let all;
    try {
      all = await apiRequest("/api/workers");
    } catch (err) {
      showError(err);
      return;
    }
    renderList(all || []);
  }

  function renderList(all) {
    const usageCount = all.reduce((sum, w) => sum + w.usages.length, 0);
    document.getElementById("worker-count").textContent =
      `Workers — ${all.length} total, ${usageCount} role assignment${usageCount === 1 ? "" : "s"}`;

    list.innerHTML = "";
    if (all.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      empty.textContent = "No workers configured yet.";
      list.appendChild(empty);
      return;
    }

    for (const worker of all) {
      list.appendChild(buildViewRow(worker));
    }
  }

  function workerMeta(worker) {
    const bits = [`${worker.harness} / ${worker.model}`];
    if (worker.params && worker.params.effort) bits.push("effort: " + worker.params.effort);
    return bits.join("  ·  ");
  }

  function usageText(worker) {
    if (worker.usages.length === 0) return "not assigned to any role";
    return worker.usages.map((u) => `${u.workflow}: ${u.role}`).join(", ");
  }

  function buildViewRow(worker) {
    const row = document.createElement("div");
    row.className = "list-row";

    const main = document.createElement("div");
    main.className = "list-row-main";
    const name = document.createElement("div");
    name.className = "list-row-name";
    name.textContent = worker.name;
    main.appendChild(name);
    const meta = document.createElement("div");
    meta.className = "list-row-meta";
    meta.textContent = workerMeta(worker);
    main.appendChild(meta);
    const usage = document.createElement("div");
    usage.className = "list-row-meta";
    usage.textContent = usageText(worker);
    main.appendChild(usage);
    row.appendChild(main);

    const actions = document.createElement("div");
    actions.className = "list-row-actions";

    const editBtn = document.createElement("button");
    editBtn.className = "link";
    editBtn.textContent = "Edit";
    editBtn.addEventListener("click", () => {
      row.replaceWith(buildEditRow(worker));
    });
    actions.appendChild(editBtn);

    const deleteBtn = document.createElement("button");
    deleteBtn.className = "link";
    deleteBtn.textContent = "Delete";
    deleteBtn.addEventListener("click", async () => {
      if (!confirm(`Delete ${worker.name}? This can't be undone.`)) return;
      clearError();
      deleteBtn.disabled = true;
      try {
        await apiRequest("/api/workers/delete", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ id: worker.id }),
        });
        await refresh();
      } catch (err) {
        showError(err);
        deleteBtn.disabled = false;
      }
    });
    actions.appendChild(deleteBtn);

    const badge = document.createElement("span");
    badge.className = "badge " + (worker.usages.length > 0 ? "enabled" : "disabled");
    badge.textContent = worker.usages.length + " usage" + (worker.usages.length === 1 ? "" : "s");
    actions.appendChild(badge);

    row.appendChild(actions);
    return row;
  }

  function buildEditRow(worker) {
    const row = document.createElement("div");
    row.className = "list-row list-row-editing";

    const main = document.createElement("div");
    main.className = "list-row-main";
    const name = document.createElement("div");
    name.className = "list-row-name";
    name.textContent = worker.name;
    main.appendChild(name);

    const editForm = document.createElement("div");
    editForm.className = "field-stack";
    editForm.innerHTML = `
      <div class="field">
        <label>Name</label>
        <input type="text" class="edit-name">
      </div>
      <div class="field">
        <label>Harness</label>
        <select class="edit-harness"><option value="">(loading...)</option></select>
      </div>
      <div class="field">
        <label>Model</label>
        <input type="text" class="edit-model">
      </div>
      <div class="field">
        <label>Effort</label>
        <input type="text" class="edit-effort">
      </div>
    `;
    main.appendChild(editForm);
    row.appendChild(main);

    const nameInput = editForm.querySelector(".edit-name");
    const harnessInput = editForm.querySelector(".edit-harness");
    const modelInput = editForm.querySelector(".edit-model");
    const effortInput = editForm.querySelector(".edit-effort");
    nameInput.value = worker.name;
    // worker.harness may be a value KnownHarnesses no longer lists (an
    // older row, or one the DB happens to hold outside the current set) —
    // populateHarnessSelect still needs *an* option to select, so add it
    // explicitly rather than silently falling back to the list's first
    // entry and misrepresenting what's actually saved.
    harnessesReady.then(() => {
      let options = harnesses;
      if (!options.includes(worker.harness)) options = [worker.harness, ...options];
      populateHarnessSelect(harnessInput, options, worker.harness);
    });
    modelInput.value = worker.model;
    effortInput.value = (worker.params && worker.params.effort) || "";

    const actions = document.createElement("div");
    actions.className = "list-row-actions";

    const saveBtn = document.createElement("button");
    saveBtn.className = "primary";
    saveBtn.textContent = "Save";
    saveBtn.addEventListener("click", async () => {
      clearError();
      saveBtn.disabled = true;
      const effort = effortInput.value.trim();
      try {
        await apiRequest("/api/workers/update", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            id: worker.id,
            name: nameInput.value.trim(),
            harness: harnessInput.value.trim(),
            model: modelInput.value.trim(),
            params: effort ? { effort: effort } : {},
          }),
        });
        await refresh();
      } catch (err) {
        showError(err);
        saveBtn.disabled = false;
      }
    });
    actions.appendChild(saveBtn);

    const cancelBtn = document.createElement("button");
    cancelBtn.className = "link";
    cancelBtn.textContent = "Cancel";
    cancelBtn.addEventListener("click", () => {
      row.replaceWith(buildViewRow(worker));
    });
    actions.appendChild(cancelBtn);

    row.appendChild(actions);
    return row;
  }

  document.getElementById("wf-submit").addEventListener("click", async () => {
    clearError();
    const name = document.getElementById("wf-name").value.trim();
    const harness = document.getElementById("wf-harness").value.trim();
    const model = document.getElementById("wf-model").value.trim();
    const effort = document.getElementById("wf-effort").value.trim();
    if (!name || !harness || !model) {
      showError(new Error("Name, harness, and model are required."));
      return;
    }
    const submit = document.getElementById("wf-submit");
    submit.disabled = true;
    try {
      await apiRequest("/api/workers", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: name,
          harness: harness,
          model: model,
          params: effort ? { effort: effort } : {},
        }),
      });
      document.getElementById("wf-name").value = "";
      // Harness deliberately left as-is (not reset) — a select with no
      // "(none)" option, and the common case is adding several Workers
      // in a row with the same harness, different models/effort.
      document.getElementById("wf-model").value = "";
      document.getElementById("wf-effort").value = "";
      await refresh();
    } catch (err) {
      showError(err);
    } finally {
      submit.disabled = false;
    }
  });

  refresh();
}

// ---- tasks view ----

function renderTasks(container) {
  const wrap = document.createElement("div");

  const errorBanner = document.createElement("div");
  errorBanner.className = "error-banner";
  errorBanner.style.display = "none";
  wrap.appendChild(errorBanner);

  function showError(err) {
    errorBanner.textContent = String(err.message || err);
    errorBanner.style.display = "block";
  }
  function clearError() {
    errorBanner.style.display = "none";
  }

  const formCard = document.createElement("div");
  formCard.className = "card";
  formCard.innerHTML = `
    <div class="card-header">
      <h2>Delegate a task</h2>
    </div>
    <div class="field-stack">
      <div class="field">
        <label for="tf-repo">Repository</label>
        <select id="tf-repo"><option value="">(loading...)</option></select>
      </div>
      <div class="field">
        <label for="tf-description">Description</label>
        <input id="tf-description" type="text" placeholder="free text — what should the agent do?">
      </div>
      <div class="field">
        <label for="tf-issue">... or GitHub issue number</label>
        <input id="tf-issue" type="number" min="1" placeholder="e.g. 3 — fetches the issue's title/body instead">
      </div>
      <div class="field">
        <label for="tf-workflow">Workflow</label>
        <select id="tf-workflow"><option value="">(repository default)</option></select>
      </div>
      <button class="primary" id="tf-submit">Delegate task</button>
    </div>
    <p class="hint">Provide exactly one of Description or GitHub issue number. This starts a real Run —
      real harness calls, real API cost — the moment you submit.</p>
  `;
  makeFormCardCollapsible(formCard, "cp-collapse-task-form");
  wrap.appendChild(formCard);

  let repos = [];
  const reposReady = apiRequest("/api/repositories")
    .then((all) => {
      repos = (all || []).filter((r) => r.enabled);
      const select = document.getElementById("tf-repo");
      select.innerHTML = "";
      if (repos.length === 0) {
        const opt = document.createElement("option");
        opt.value = "";
        opt.textContent = "(no enabled repositories — add one under Repositories)";
        select.appendChild(opt);
        return;
      }
      for (const repo of repos) {
        const opt = document.createElement("option");
        opt.value = repo.name;
        opt.textContent = repo.name;
        select.appendChild(opt);
      }
    })
    .catch(showError);

  let workflows = [];
  const workflowsReady = apiRequest("/api/workflows")
    .then((infos) => {
      workflows = infos || [];
      populateWorkflowSelect(document.getElementById("tf-workflow"), workflows, "");
    })
    .catch(showError);

  const listCard = document.createElement("div");
  listCard.className = "card";
  const listHeader = document.createElement("div");
  listHeader.className = "card-header";
  listHeader.innerHTML = `
    <h2 id="task-count">Tasks</h2>
    <div class="header-actions">
      <label class="sr-only" for="task-status-filter">Filter tasks by status</label>
      <select id="task-status-filter" class="compact-select">
        <option value="">All statuses</option>
        <option value="RUNNING">Running</option>
        <option value="REVIEW_PENDING">Review pending</option>
        <option value="FAILED">Failed</option>
        <option value="COMPLETED">Completed</option>
        <option value="CANCELLED">Cancelled</option>
      </select>
      <button class="link" id="task-refresh">Refresh</button>
    </div>`;
  listCard.appendChild(listHeader);
  const list = document.createElement("div");
  list.className = "list";
  listCard.appendChild(list);
  wrap.appendChild(listCard);

  // container.appendChild(wrap) must happen before any getElementById
  // lookup below — until wrap is attached, its contents aren't part of
  // the live document and getElementById returns null.
  container.appendChild(wrap);

  document.getElementById("task-status-filter").addEventListener("change", () => refresh());
  document.getElementById("task-refresh").addEventListener("click", () => refresh());

  // When the chosen repository has a default_workflow, preselect it (still
  // overridable) so the human sees what will actually run before hitting
  // submit, rather than discovering it after the fact.
  document.getElementById("tf-repo").addEventListener("change", async (ev) => {
    await reposReady;
    await workflowsReady;
    const repo = repos.find((r) => r.name === ev.target.value);
    populateWorkflowSelect(document.getElementById("tf-workflow"), workflows, repo ? repo.default_workflow : "");
  });

  async function refresh() {
    clearError();
    let tasks;
    try {
      tasks = await apiRequest("/api/tasks");
    } catch (err) {
      showError(err);
      return;
    }
    renderList(tasks || []);
  }

  function renderList(tasks) {
    const filter = document.getElementById("task-status-filter").value;
    const visible = filter ? tasks.filter((task) => task.status === filter) : tasks;
    document.getElementById("task-count").textContent = filter
      ? `Tasks — ${visible.length} ${filter.toLowerCase().replaceAll("_", " ")}`
      : `Tasks — ${tasks.length} total`;

    list.innerHTML = "";
    if (visible.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      empty.textContent = filter ? "No tasks match this status." : "No tasks yet.";
      list.appendChild(empty);
      return;
    }

    for (const task of visible) {
      list.appendChild(buildTaskRow(task));
    }
  }

  function buildTaskRow(task) {
    const row = document.createElement("div");
    row.className = "list-row";

    const main = document.createElement("div");
    main.className = "list-row-main";

    const name = document.createElement("div");
    name.className = "list-row-name";
    const description = task.description || "(no description)";
    name.textContent = description.length > 100 ? description.slice(0, 100) + "…" : description;
    main.appendChild(name);

    const meta = document.createElement("div");
    meta.className = "list-row-meta";
    const bits = [task.source];
    if (task.target_repo) bits.push(task.target_repo);
    if (task.workflow) bits.push(task.workflow);
    if (task.run_id) bits.push("run: " + task.run_id);
    meta.textContent = bits.join("  ·  ");
    main.appendChild(meta);

    if (task.status === "FAILED") {
      const reason = document.createElement("div");
      reason.className = "list-row-meta text-negative";
      reason.textContent = task.failure_reason || "Failed (no reason recorded — this Run predates failure tracking)";
      main.appendChild(reason);
    }

    // What the latest step actually produced (a Planner's verdict and
    // scope_contract, a Reviewer's findings, a diff summary) — same
    // content doc08's tracker mirror posts externally, shown here too so
    // "why is this Task stuck" doesn't require reading Temporal's raw
    // history (empty for most COMPLETED/RUNNING tasks, since a plain
    // pass/fail tool-step outcome carries none of these fields).
    if (task.summary) {
      const summary = document.createElement("div");
      summary.className = "list-row-summary";
      summary.textContent = task.summary;
      main.appendChild(summary);
    }

    row.appendChild(main);

    const actions = document.createElement("div");
    actions.className = "list-row-actions";
    const badge = document.createElement("span");
    badge.className = "badge " + (task.status === "FAILED" ? "failed" : task.status === "QUEUED" ? "disabled" : "enabled");
    badge.textContent = task.status;
    actions.appendChild(badge);
    if (task.run_id) {
      const details = document.createElement("button");
      details.className = "link";
      details.textContent = "Details";
      details.addEventListener("click", () => showRunDetails(task));
      actions.appendChild(details);
    }
    if (task.status === "FAILED" && task.source === "human") {
      const retry = document.createElement("button");
      retry.className = "link";
      retry.textContent = "Retry";
      retry.addEventListener("click", async () => {
        if (!confirm("Start a fresh Run for this failed Task? The previous Run will remain available in the history.")) return;
        retry.disabled = true;
        try {
          const created = await apiRequest("/api/tasks/retry", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ task_id: task.task_id }),
          });
          alert(`Retry started as Run ${created.run_id}.`);
          await refresh();
        } catch (err) {
          showError(err);
          retry.disabled = false;
        }
      });
      actions.appendChild(retry);
    }
    row.appendChild(actions);

    return row;
  }

  document.getElementById("tf-submit").addEventListener("click", async () => {
    clearError();
    const repoName = document.getElementById("tf-repo").value;
    const description = document.getElementById("tf-description").value.trim();
    const issueStr = document.getElementById("tf-issue").value.trim();
    const workflowFile = document.getElementById("tf-workflow").value;

    if (!repoName) {
      showError(new Error("Choose a repository."));
      return;
    }
    if (!description && !issueStr) {
      showError(new Error("Provide a description or a GitHub issue number."));
      return;
    }
    if (description && issueStr) {
      showError(new Error("Provide exactly one of description or GitHub issue number, not both."));
      return;
    }

    const repo = repos.find((r) => r.name === repoName);
    const target = issueStr ? `issue #${issueStr}` : "a manual description";
    if (!confirm(`This starts a real Run against ${repoName} (${target}) using ` +
        `${workflowFile || (repo && repo.default_workflow) || "the repository's default workflow"}. ` +
        `It will make real, billed harness calls. Continue?`)) {
      return;
    }

    const submit = document.getElementById("tf-submit");
    submit.disabled = true;
    try {
      const body = { repo_name: repoName, workflow_file: workflowFile, description: description };
      if (issueStr) body.github_issue = parseInt(issueStr, 10);
      const created = await apiRequest("/api/tasks", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (created.attach_run_warning) {
        showError(new Error("Task started (run " + created.run_id + ") but: " + created.attach_run_warning));
      }
      document.getElementById("tf-description").value = "";
      document.getElementById("tf-issue").value = "";
      await refresh();
    } catch (err) {
      showError(err);
    } finally {
      submit.disabled = false;
    }
  });

  refresh();
  // Auto-refresh: a Task's status only ever changes from background Run
  // activity (a worker process, not this browser tab), so without this a
  // human has to remember to hit refresh to see it move past QUEUED/
  // RUNNING. Skipped while any row is mid-edit so a poll tick never wipes
  // out unsaved input — same guard on every polled view.
  startPolling(() => {
    if (!list.querySelector(".list-row-editing")) refresh();
  }, 5000);
}

// ---- inbox view ----

function humanizeAge(isoTimestamp) {
  const ms = Date.now() - new Date(isoTimestamp).getTime();
  const minutes = Math.floor(ms / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return minutes + "m ago";
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return hours + "h ago";
  return Math.floor(hours / 24) + "d ago";
}

function renderInbox(container) {
  const wrap = document.createElement("div");

  const errorBanner = document.createElement("div");
  errorBanner.className = "error-banner";
  errorBanner.style.display = "none";
  wrap.appendChild(errorBanner);

  function showError(err) {
    errorBanner.textContent = String(err.message || err);
    errorBanner.style.display = "block";
  }
  function clearError() {
    errorBanner.style.display = "none";
  }

  const listCard = document.createElement("div");
  listCard.className = "card";
  const header = document.createElement("div");
  header.className = "card-header";
  header.innerHTML = `<h2 id="inbox-count">Inbox</h2>`;
  listCard.appendChild(header);
  const list = document.createElement("div");
  list.className = "list";
  listCard.appendChild(list);
  wrap.appendChild(listCard);

  const hint = document.createElement("p");
  hint.className = "hint";
  hint.textContent = "Runs currently parked at REVIEW_PENDING, oldest first, with the escalating step's verdict/findings shown inline. " +
    "This is not a full transcript (that's still only in the Temporal UI) — just the last step's outcome.";
  wrap.appendChild(hint);

  container.appendChild(wrap);

  // Step-id options for the resume combobox come from /api/workflows,
  // keyed by workflow name (the same name run_events/PendingRun.workflow
  // carries) — one fetch, reused per row.
  let stepIDsByWorkflow = {};
  const workflowsReady = apiRequest("/api/workflows")
    .then((infos) => {
      for (const info of infos || []) {
        stepIDsByWorkflow[info.workflow] = info.step_ids || [];
      }
    })
    .catch(showError);

  async function refresh() {
    clearError();
    let pending;
    try {
      pending = await apiRequest("/api/inbox");
    } catch (err) {
      showError(err);
      return;
    }
    renderList(pending || []);
  }

  function renderList(pending) {
    document.getElementById("inbox-count").textContent = `Inbox — ${pending.length} pending`;

    list.innerHTML = "";
    if (pending.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      empty.textContent = "Nothing waiting on a human right now.";
      list.appendChild(empty);
      return;
    }

    for (const item of pending) {
      list.appendChild(buildInboxRow(item));
    }
  }

  function buildInboxRow(item) {
    const row = document.createElement("div");
    row.className = "list-row";

    const main = document.createElement("div");
    main.className = "list-row-main";
    const name = document.createElement("div");
    name.className = "list-row-name";
    name.textContent = item.run_id;
    main.appendChild(name);
    const meta = document.createElement("div");
    meta.className = "list-row-meta";
    const bits = [item.workflow, "escalated from " + item.from_step + (item.outcome ? " (" + item.outcome + ")" : "")];
    if (item.attempt_number) bits.push("attempt " + item.attempt_number);
    bits.push(humanizeAge(item.occurred_at));
    meta.textContent = bits.join("  ·  ");
    main.appendChild(meta);

    // What the escalating step actually produced (a Planner's verdict
    // and scope_contract, a Reviewer's findings, a diff summary) — the
    // same content doc08's tracker mirror posts externally, shown here
    // too so a human can decide resume-vs-cancel without reading
    // Temporal's raw history to find out why this escalated.
    if (item.summary) {
      const summary = document.createElement("div");
      summary.className = "list-row-summary";
      summary.textContent = item.summary;
      main.appendChild(summary);
    }

    row.appendChild(main);

    const actions = document.createElement("div");
    actions.className = "list-row-actions";

    const detailsBtn = document.createElement("button");
    detailsBtn.className = "link";
    detailsBtn.textContent = "Details";
    detailsBtn.addEventListener("click", () => showRunDetails({
      run_id: item.run_id,
      status: "REVIEW_PENDING",
      workflow: item.workflow,
    }));
    actions.appendChild(detailsBtn);

    const resumeBtn = document.createElement("button");
    resumeBtn.className = "link";
    resumeBtn.textContent = "Resume";
    resumeBtn.addEventListener("click", () => {
      row.replaceWith(buildResumeRow(item));
    });
    actions.appendChild(resumeBtn);

    const cancelBtn = document.createElement("button");
    cancelBtn.className = "link";
    cancelBtn.textContent = "Cancel";
    cancelBtn.addEventListener("click", async () => {
      if (!confirm(`Cancel run ${item.run_id}? This can't be undone.`)) return;
      clearError();
      cancelBtn.disabled = true;
      try {
        await apiRequest("/api/inbox/cancel", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ run_id: item.run_id }),
        });
        await refresh();
        refreshInboxBadge();
      } catch (err) {
        showError(err);
        cancelBtn.disabled = false;
      }
    });
    actions.appendChild(cancelBtn);

    row.appendChild(actions);
    return row;
  }

  function buildResumeRow(item) {
    const row = document.createElement("div");
    row.className = "list-row list-row-editing";

    const main = document.createElement("div");
    main.className = "list-row-main";
    const name = document.createElement("div");
    name.className = "list-row-name";
    name.textContent = item.run_id;
    main.appendChild(name);

    if (item.summary) {
      const summary = document.createElement("div");
      summary.className = "list-row-summary";
      summary.textContent = item.summary;
      main.appendChild(summary);
    }

    const form = document.createElement("div");
    form.className = "field-stack";
    form.innerHTML = `
      <div class="field">
        <label>Resume at step <span class="field-help">(recommended step is preselected)</span></label>
        <select class="resume-step"><option value="">(loading...)</option></select>
      </div>
      <div class="field">
        <label>Hint (free text, passed to the resumed step's context)</label>
        <textarea class="resume-hint" rows="3" placeholder="e.g. what changed, or why to proceed"></textarea>
      </div>
      <div class="resume-advice"></div>
      <label class="confirmation-check"><input type="checkbox" class="resume-confirm"> I reviewed the latest summary and understand this resume will reset the Run's budgets.</label>
    `;
    main.appendChild(form);
    row.appendChild(main);

    const stepSelect = form.querySelector(".resume-step");
    const hintInput = form.querySelector(".resume-hint");
    const advice = form.querySelector(".resume-advice");
    const confirmCheck = form.querySelector(".resume-confirm");
    const updateAdvice = () => {
      const selected = stepSelect.value;
      const recommended = recommendedResumeStep(item);
      advice.textContent = selected === recommended
        ? `Recommended: resume at ${recommended}. This returns to the step that raised the escalation.`
        : `You selected ${selected || "no step"} instead of the recommended ${recommended}. Confirm that this will not skip required work or repeat edits.`;
      advice.classList.toggle("warning", selected !== recommended);
    };
    stepSelect.addEventListener("change", updateAdvice);
    workflowsReady.then(() => {
      const stepIDs = stepIDsByWorkflow[item.workflow] || [];
      stepSelect.innerHTML = "";
      for (const id of stepIDs) {
        const opt = document.createElement("option");
        opt.value = id;
        opt.textContent = id;
        stepSelect.appendChild(opt);
      }
      const recommended = recommendedResumeStep(item);
      if (stepIDs.includes(recommended)) stepSelect.value = recommended;
      updateAdvice();
    });

    const actions = document.createElement("div");
    actions.className = "list-row-actions";

    const confirmBtn = document.createElement("button");
    confirmBtn.className = "primary";
    confirmBtn.textContent = "Confirm resume";
    confirmBtn.addEventListener("click", async () => {
      clearError();
      if (!stepSelect.value) {
        showError(new Error("Choose a step to resume at."));
        return;
      }
      if (!confirmCheck.checked) {
        showError(new Error("Review the summary and confirm that you understand the resume consequences."));
        return;
      }
      confirmBtn.disabled = true;
      try {
        await apiRequest("/api/inbox/resume", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            run_id: item.run_id,
            resume_step_id: stepSelect.value,
            hint: hintInput.value.trim(),
          }),
        });
        await refresh();
        refreshInboxBadge();
      } catch (err) {
        showError(err);
        confirmBtn.disabled = false;
      }
    });
    actions.appendChild(confirmBtn);

    const cancelEditBtn = document.createElement("button");
    cancelEditBtn.className = "link";
    cancelEditBtn.textContent = "Dismiss";
    cancelEditBtn.addEventListener("click", () => {
      row.replaceWith(buildInboxRow(item));
    });
    actions.appendChild(cancelEditBtn);

    row.appendChild(actions);
    return row;
  }

  refresh();
  // Auto-refresh: a Run parked here got there from background Activity —
  // an escalation appearing, or one a teammate already resumed/cancelled
  // from elsewhere — that this browser tab has no way to know about
  // otherwise. Skipped while a row is mid-edit (resume form open) so a
  // poll tick never wipes out an unsaved hint.
  startPolling(() => {
    if (!list.querySelector(".list-row-editing")) refresh();
  }, 5000);
}

// ---- pending approvals view ----
//
// docs/01's mandatory plan-approval gate: every drafted plan pauses here,
// unconditionally, for every Workflow — the routine, expected-volume
// counterpart to Inbox's exceptions-only queue (internal/inbox.
// ListPendingApprovals' doc comment). Two actions, both requiring a
// non-empty note — approval isn't a bare click (doc01) — and a redraft is
// shown diffed against the immediately preceding draft rather than
// re-rendered whole, so re-reading round N doesn't mean re-reading every
// round before it.

// lineDiff computes a minimal line-level diff between two texts via a
// classic LCS dynamic program — deterministic, no vendored library, well
// within reason for the short plan documents this renders (a handful of
// KB at most).
function lineDiff(oldText, newText) {
  const a = oldText.split("\n");
  const b = newText.split("\n");
  const m = a.length;
  const n = b.length;
  const dp = Array.from({ length: m + 1 }, () => new Array(n + 1).fill(0));
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const result = [];
  let i = 0;
  let j = 0;
  while (i < m && j < n) {
    if (a[i] === b[j]) {
      result.push({ type: "same", text: a[i] });
      i++; j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      result.push({ type: "remove", text: a[i] });
      i++;
    } else {
      result.push({ type: "add", text: b[j] });
      j++;
    }
  }
  while (i < m) { result.push({ type: "remove", text: a[i] }); i++; }
  while (j < n) { result.push({ type: "add", text: b[j] }); j++; }
  return result;
}

function renderPendingApprovals(container) {
  const wrap = document.createElement("div");

  const errorBanner = document.createElement("div");
  errorBanner.className = "error-banner";
  errorBanner.style.display = "none";
  wrap.appendChild(errorBanner);

  function showError(err) {
    errorBanner.textContent = String(err.message || err);
    errorBanner.style.display = "block";
  }
  function clearError() {
    errorBanner.style.display = "none";
  }

  const listCard = document.createElement("div");
  listCard.className = "card";
  const header = document.createElement("div");
  header.className = "card-header";
  header.innerHTML = `<h2 id="pending-approvals-count">Pending approvals</h2>`;
  listCard.appendChild(header);
  const list = document.createElement("div");
  list.className = "list";
  listCard.appendChild(list);
  wrap.appendChild(listCard);

  const hint = document.createElement("p");
  hint.className = "hint";
  hint.textContent = "Every drafted plan pauses here before a Coder touches any code — not an exception queue " +
    "like Inbox, the routine path for every Task. Approving or requesting changes both require a note: the point " +
    "is a human is on record for why, not just that a click happened.";
  wrap.appendChild(hint);

  container.appendChild(wrap);

  async function refresh() {
    clearError();
    let pending;
    try {
      pending = await apiRequest("/api/pending-approvals");
    } catch (err) {
      showError(err);
      return;
    }
    renderList(pending || []);
  }

  function renderList(pending) {
    document.getElementById("pending-approvals-count").textContent =
      `Pending approvals — ${pending.length} awaiting review`;

    list.innerHTML = "";
    if (pending.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      empty.textContent = "No plans waiting on approval right now.";
      list.appendChild(empty);
      return;
    }

    for (const item of pending) {
      list.appendChild(buildApprovalRow(item));
    }
  }

  function buildApprovalRow(item) {
    const row = document.createElement("div");
    row.className = "list-row";

    const main = document.createElement("div");
    main.className = "list-row-main";
    const name = document.createElement("div");
    name.className = "list-row-name";
    name.textContent = item.run_id;
    main.appendChild(name);
    const meta = document.createElement("div");
    meta.className = "list-row-meta";
    meta.textContent = [item.workflow, humanizeAge(item.occurred_at)].join("  ·  ");
    main.appendChild(meta);

    if (item.summary) {
      const summary = document.createElement("div");
      summary.className = "list-row-summary";
      summary.textContent = item.summary;
      main.appendChild(summary);
    }

    row.appendChild(main);

    const actions = document.createElement("div");
    actions.className = "list-row-actions";

    const detailsBtn = document.createElement("button");
    detailsBtn.className = "link";
    detailsBtn.textContent = "Details";
    detailsBtn.addEventListener("click", () => showRunDetails({
      run_id: item.run_id,
      status: "REVIEW_PENDING",
      workflow: item.workflow,
    }));
    actions.appendChild(detailsBtn);

    const reviewBtn = document.createElement("button");
    reviewBtn.className = "primary";
    reviewBtn.textContent = "Review";
    reviewBtn.addEventListener("click", () => {
      row.replaceWith(buildReviewRow(item));
    });
    actions.appendChild(reviewBtn);

    row.appendChild(actions);
    return row;
  }

  function buildReviewRow(item) {
    const row = document.createElement("div");
    row.className = "list-row list-row-editing";

    const main = document.createElement("div");
    main.className = "list-row-main";
    const name = document.createElement("div");
    name.className = "list-row-name";
    name.textContent = item.run_id;
    main.appendChild(name);

    const diffStatus = document.createElement("div");
    diffStatus.className = "list-row-meta";
    diffStatus.textContent = "Loading round history…";
    main.appendChild(diffStatus);
    loadPlanDiff(item, diffStatus, main);

    const form = document.createElement("div");
    form.className = "field-stack";
    form.innerHTML = `
      <div class="field">
        <label>Your note <span class="field-help">(required either way — why you're approving, or what needs to change)</span></label>
        <textarea class="approval-note" rows="3" placeholder="e.g. scope looks right, ship it — or: this misses the auth edge case"></textarea>
      </div>
    `;
    main.appendChild(form);
    row.appendChild(main);

    const noteInput = form.querySelector(".approval-note");

    const actions = document.createElement("div");
    actions.className = "list-row-actions";

    const approveBtn = document.createElement("button");
    approveBtn.className = "primary";
    approveBtn.textContent = "Approve";
    approveBtn.disabled = true;
    actions.appendChild(approveBtn);

    const requestChangesBtn = document.createElement("button");
    requestChangesBtn.className = "link";
    requestChangesBtn.textContent = "Request changes";
    requestChangesBtn.disabled = true;
    actions.appendChild(requestChangesBtn);

    noteInput.addEventListener("input", () => {
      const hasNote = noteInput.value.trim() !== "";
      approveBtn.disabled = !hasNote;
      requestChangesBtn.disabled = !hasNote;
    });

    approveBtn.addEventListener("click", async () => {
      clearError();
      approveBtn.disabled = true;
      requestChangesBtn.disabled = true;
      try {
        await apiRequest("/api/pending-approvals/approve", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ run_id: item.run_id, justification: noteInput.value.trim() }),
        });
        await refresh();
        refreshPendingApprovalsBadge();
        refreshInboxBadge();
      } catch (err) {
        showError(err);
        approveBtn.disabled = false;
        requestChangesBtn.disabled = false;
      }
    });

    requestChangesBtn.addEventListener("click", async () => {
      clearError();
      approveBtn.disabled = true;
      requestChangesBtn.disabled = true;
      try {
        await apiRequest("/api/pending-approvals/request-changes", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ run_id: item.run_id, hint: noteInput.value.trim() }),
        });
        await refresh();
        refreshPendingApprovalsBadge();
        refreshInboxBadge();
      } catch (err) {
        showError(err);
        approveBtn.disabled = false;
        requestChangesBtn.disabled = false;
      }
    });

    const dismissBtn = document.createElement("button");
    dismissBtn.className = "link";
    dismissBtn.textContent = "Dismiss";
    dismissBtn.addEventListener("click", () => {
      row.replaceWith(buildApprovalRow(item));
    });
    actions.appendChild(dismissBtn);

    row.appendChild(actions);
    return row;
  }

  // loadPlanDiff fetches this Run's full event history (already-existing
  // GET /api/runs/{run_id}, no new endpoint needed) and, if the planning
  // step ran more than once (a prior request-changes round), renders a
  // line diff between the last two drafts — round 1 has nothing to diff
  // against, so it just confirms there's no prior round. The row's own
  // current-plan text (item.summary) already shows the latest draft in
  // full; this is supplementary "what changed since the last note," not
  // a replacement for it.
  async function loadPlanDiff(item, statusEl, container) {
    let events;
    try {
      const res = await apiRequest("/api/runs/" + encodeURIComponent(item.run_id));
      events = res.events || [];
    } catch (err) {
      statusEl.textContent = "Could not load round history: " + (err.message || err);
      return;
    }

    const rounds = events.filter((ev) => ev.from_step === item.from_step && ev.to_step === "REVIEW_PENDING" && ev.summary);
    if (rounds.length < 2) {
      statusEl.textContent = "Round 1 — no earlier draft to diff against.";
      return;
    }

    const prev = rounds[rounds.length - 2];
    const curr = rounds[rounds.length - 1];
    statusEl.textContent = `Round ${rounds.length} — changed since the previous draft:`;

    const diffBox = document.createElement("div");
    diffBox.className = "plan-diff";
    for (const line of lineDiff(prev.summary, curr.summary)) {
      const lineEl = document.createElement("div");
      lineEl.className = "plan-diff-line plan-diff-" + line.type;
      lineEl.textContent = (line.type === "add" ? "+ " : line.type === "remove" ? "- " : "  ") + line.text;
      diffBox.appendChild(lineEl);
    }
    container.appendChild(diffBox);
  }

  refresh();
  // Auto-refresh: a plan appearing here, or one a teammate already
  // approved/sent back from elsewhere, both happen from background Run
  // activity this browser tab has no other way to learn about. Skipped
  // while a row is mid-review (note being typed) so a poll tick never
  // wipes out unsaved input.
  startPolling(() => {
    if (!list.querySelector(".list-row-editing")) refresh();
  }, 5000);
}

// ---- settings view ----

// renderSettings edits internal/settings' global key-value store. Only
// one real field today (factory_root) — the backend is generic
// (GET/POST /api/settings takes any key), but the UI doesn't need to be
// generic just because the backend is; add the next field by hand
// alongside factory_root when the next setting actually gets migrated
// off an env var (docs/04's "Accumulating env-config surfaces" list has
// the candidates).
function renderSettings(container) {
  const wrap = document.createElement("div");

  const errorBanner = document.createElement("div");
  errorBanner.className = "error-banner";
  errorBanner.style.display = "none";
  wrap.appendChild(errorBanner);

  function showError(err) {
    errorBanner.textContent = String(err.message || err);
    errorBanner.style.display = "block";
  }
  function clearError() {
    errorBanner.style.display = "none";
  }

  const card = document.createElement("div");
  card.className = "card";
  card.innerHTML = `
    <div class="card-header">
      <h2>Settings</h2>
    </div>
    <div class="field-stack">
      <div class="field">
        <label for="settings-factory-root">Default worktree root</label>
        <input id="settings-factory-root" type="text" placeholder="e.g. /var/lib/factory">
      </div>
      <button class="primary" id="settings-submit">Save</button>
    </div>
    <p class="hint" id="settings-status"></p>
  `;
  wrap.appendChild(card);

  const hint = document.createElement("p");
  hint.className = "hint";
  hint.textContent = "Where a Run's git clone/worktree live on disk, unless a Repository overrides it " +
    "(see that view's \"Worktree root override\" field). Used by every Run — an unconfigured value fails " +
    "a Run at its first step (provision) rather than guessing a path that might not be writable.";
  wrap.appendChild(hint);

  container.appendChild(wrap);

  const input = document.getElementById("settings-factory-root");
  const status = document.getElementById("settings-status");
  const submit = document.getElementById("settings-submit");

  async function refresh() {
    clearError();
    let all;
    try {
      all = await apiRequest("/api/settings");
    } catch (err) {
      showError(err);
      return;
    }
    const factoryRoot = (all || []).find((s) => s.key === "factory_root");
    status.classList.remove("text-negative");
    if (factoryRoot) {
      input.value = factoryRoot.value;
      status.textContent = `Currently used by every Run. Last updated ${new Date(factoryRoot.updated_at).toLocaleString()}.`;
    } else {
      input.value = "";
      status.textContent = "Not configured yet — every Run without a per-repository override will fail at its first step until this is set.";
      status.classList.add("text-negative");
    }
  }

  submit.addEventListener("click", async () => {
    clearError();
    const value = input.value.trim();
    if (!value) {
      showError(new Error("Default worktree root cannot be empty."));
      return;
    }
    submit.disabled = true;
    try {
      await apiRequest("/api/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ key: "factory_root", value: value }),
      });
      await refresh();
    } catch (err) {
      showError(err);
    } finally {
      submit.disabled = false;
    }
  });

  refresh();
}
