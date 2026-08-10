// Control plane SPA shell. Vanilla JS, no framework/build step — kept
// simple on purpose (docs/04: thin MVP), but structured so a new section
// is one entry in VIEWS plus one render function, not a rewrite of the
// shell. Routing is just location.hash -> VIEWS lookup; there's exactly
// one view today (repositories), but the registry/render contract is
// already the shape a second view would follow.

const VIEWS = {
  repositories: { label: "Repositories", render: renderRepositories },
  workflows: { label: "Workflows", render: renderWorkflows },
  workers: { label: "Workers", render: renderWorkers },
  tasks: { label: "Tasks", render: renderTasks },
  inbox: { label: "Inbox", render: renderInbox },
  settings: { label: "Settings", render: renderSettings },
  workflow_v1: { label: "Workflow (v1: vanilla SVG)", render: renderWorkflowV1 },
  workflow_v2: { label: "Workflow (v2: D3 + dagre)", render: renderWorkflowV2 },
  workflow_v3: { label: "Workflow (v3: Cytoscape.js)", render: renderWorkflowV3 },
  workflow_v4: { label: "Workflow (v4: BPMN-styled)", render: renderWorkflowV4 },
  workflow_v5: { label: "Workflow (v5: bpmn-js + auto-layout)", render: renderWorkflowV5 },
};

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

    const label = document.createElement("span");
    label.textContent = view.label;
    a.appendChild(label);

    if (id === "inbox") {
      const badge = document.createElement("span");
      badge.className = "nav-badge";
      badge.id = "nav-inbox-badge";
      badge.style.display = "none";
      a.appendChild(badge);
    }

    li.appendChild(a);
    list.appendChild(li);
  }
  refreshInboxBadge();
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

function renderView() {
  const id = currentViewID();
  const view = VIEWS[id];
  document.getElementById("topbar-title").textContent = view.label;
  const content = document.getElementById("content");
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

// ---- shared graph-view scaffold (workflow_v1/v2/v3) ----

// buildGraphViewShell is the common chrome every visualization prototype
// shares: an error banner, a workflow picker, a canvas area sized for a
// real graph (not a chat-bubble-sized card), and a legend row. Each
// prototype supplies its own render(canvasEl, graph) — everything about
// *how* the graph is drawn and panned/zoomed is prototype-specific by
// design (that's the point of having three), but the surrounding page
// structure shouldn't be reinvented three times.
// buildGraphViewShell is the common chrome every visualization prototype
// shares. opts.layouts is a list of {id, label, render(canvas, graph)} —
// each prototype registers several layout algorithms rather than one, so
// they can be compared live via the second dropdown without leaving the
// page. The graph itself (including cluster membership — see
// cmd/controlplane's computeClusters) is fetched once per workflow
// selection and reused across every layout switch; only re-rendering is
// re-run, not a new API call.
function buildGraphViewShell(container, opts) {
  const wrap = document.createElement("div");

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
      <select class="graph-workflow-select"><option value="">(loading...)</option></select>
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
    row.className = "list-row";

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

    row.appendChild(main);

    const actions = document.createElement("div");
    actions.className = "list-row-actions";
    const badge = document.createElement("span");
    badge.className = "badge " + (info.valid ? "valid" : "invalid");
    badge.textContent = info.valid ? "Valid" : "Invalid";
    actions.appendChild(badge);
    row.appendChild(actions);

    return row;
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
        <input id="wf-harness" type="text" placeholder="e.g. claude-code">
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
        <input type="text" class="edit-harness">
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
    harnessInput.value = worker.harness;
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
      document.getElementById("wf-harness").value = "";
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
  listHeader.innerHTML = `<h2 id="task-count">Tasks</h2>`;
  listCard.appendChild(listHeader);
  const list = document.createElement("div");
  list.className = "list";
  listCard.appendChild(list);
  wrap.appendChild(listCard);

  // container.appendChild(wrap) must happen before any getElementById
  // lookup below — until wrap is attached, its contents aren't part of
  // the live document and getElementById returns null.
  container.appendChild(wrap);

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
    document.getElementById("task-count").textContent = `Tasks — ${tasks.length} total`;

    list.innerHTML = "";
    if (tasks.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      empty.textContent = "No tasks yet.";
      list.appendChild(empty);
      return;
    }

    for (const task of tasks) {
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
    badge.className = "badge " + (task.status === "QUEUED" ? "disabled" : "enabled");
    badge.textContent = task.status;
    actions.appendChild(badge);
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
        <label>Resume at step</label>
        <select class="resume-step"><option value="">(loading...)</option></select>
      </div>
      <div class="field">
        <label>Hint (free text, passed to the resumed step's context)</label>
        <input type="text" class="resume-hint" placeholder="e.g. what changed, or why to proceed">
      </div>
    `;
    main.appendChild(form);
    row.appendChild(main);

    const stepSelect = form.querySelector(".resume-step");
    const hintInput = form.querySelector(".resume-hint");
    workflowsReady.then(() => {
      const stepIDs = stepIDsByWorkflow[item.workflow] || [];
      stepSelect.innerHTML = "";
      for (const id of stepIDs) {
        const opt = document.createElement("option");
        opt.value = id;
        opt.textContent = id;
        stepSelect.appendChild(opt);
      }
      if (stepIDs.includes(item.from_step)) stepSelect.value = item.from_step;
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
