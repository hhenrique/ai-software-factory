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
  work: { label: "Work", render: renderWork },
  inbox: { label: "Inbox", render: renderInbox },
  workflow_v1: { label: "Workflow (v1: vanilla SVG)", render: renderWorkflowV1 },
  workflow_v2: { label: "Workflow (v2: D3 + dagre)", render: renderWorkflowV2 },
  workflow_v3: { label: "Workflow (v3: Cytoscape.js)", render: renderWorkflowV3 },
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
    a.textContent = view.label;
    li.appendChild(a);
    list.appendChild(li);
  }
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

window.addEventListener("hashchange", renderView);
window.addEventListener("DOMContentLoaded", () => {
  setupSidebarToggle();
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

  const card = document.createElement("div");
  card.className = "card graph-card";
  card.innerHTML = `
    <div class="card-header">
      <h2>${opts.title}</h2>
      <select class="graph-workflow-select"><option value="">(loading...)</option></select>
    </div>
    <div class="graph-canvas-wrap"><div class="graph-canvas"></div></div>
    <div class="graph-legend">
      <span class="graph-legend-item"><span class="graph-legend-swatch" style="color:var(--color-node-tool)"></span>tool step</span>
      <span class="graph-legend-item"><span class="graph-legend-swatch" style="color:var(--color-node-agent)"></span>agent step</span>
      <span class="graph-legend-item"><span class="graph-legend-swatch" style="color:var(--color-node-terminal)"></span>terminal state</span>
      <span class="graph-legend-item">${opts.interactionHint}</span>
    </div>
  `;
  wrap.appendChild(card);
  container.appendChild(wrap);

  const select = card.querySelector(".graph-workflow-select");
  const canvas = card.querySelector(".graph-canvas");

  apiRequest("/api/workflows")
    .then((infos) => {
      infos = infos || [];
      select.innerHTML = "";
      for (const info of infos) {
        const option = document.createElement("option");
        option.value = info.path;
        option.textContent = info.path;
        select.appendChild(option);
      }
      if (infos.length > 0) {
        loadAndRender(infos[0].path);
      } else {
        showError(new Error("No workflow definitions found."));
      }
    })
    .catch(showError);

  select.addEventListener("change", () => loadAndRender(select.value));

  function loadAndRender(path) {
    if (!path) return;
    canvas.innerHTML = "";
    apiRequest("/api/workflow-graph?path=" + encodeURIComponent(path))
      .then((graph) => opts.render(canvas, graph))
      .catch(showError);
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
      (repo.test_command ? "  ·  " + repo.test_command : "");
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
    `;
    main.appendChild(editForm);
    row.appendChild(main);

    const testCommandInput = editForm.querySelector(".edit-test-command");
    testCommandInput.value = repo.test_command || "";
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
        }),
      });
      document.getElementById("rf-identity").value = "";
      document.getElementById("rf-test-command").value = "";
      document.getElementById("rf-workflow").value = "";
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
  hint.textContent = "Read-only: scanned from workflows/ on disk. Edited as YAML, checked into git.";
  wrap.appendChild(hint);

  container.appendChild(wrap);

  apiRequest("/api/workflows")
    .then((infos) => renderList(infos || []))
    .catch(showError);

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
    const roleSummary = (info.roles || []).map((r) => `${r.name}: ${r.harness}/${r.model}`).join(", ");
    const bits = [info.path];
    if (info.step_count) bits.push(info.step_count + " steps");
    if (roleSummary) bits.push(roleSummary);
    if (info.has_trigger) bits.push("triggered");
    meta.textContent = bits.join("  ·  ");
    main.appendChild(meta);

    if (!info.valid && info.errors && info.errors.length > 0) {
      const errList = document.createElement("div");
      errList.className = "list-row-meta text-negative";
      errList.textContent = info.errors.join("; ");
      main.appendChild(errList);
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
  hint.textContent = "Derived from every Workflow Definition's roles: block, grouped by " +
    "(harness, model) — changing one here means changing all of its usages below at once.";
  wrap.appendChild(hint);

  container.appendChild(wrap);

  apiRequest("/api/workers")
    .then((workers) => renderList(workers || []))
    .catch(showError);

  function renderList(workers) {
    const usageCount = workers.reduce((sum, w) => sum + w.usages.length, 0);
    document.getElementById("worker-count").textContent =
      `Workers — ${workers.length} distinct, ${usageCount} role usages`;

    list.innerHTML = "";
    if (workers.length === 0) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      empty.textContent = "No roles found in any Workflow Definition.";
      list.appendChild(empty);
      return;
    }

    for (const worker of workers) {
      list.appendChild(buildWorkerRow(worker));
    }
  }

  function buildWorkerRow(worker) {
    const row = document.createElement("div");
    row.className = "list-row";

    const main = document.createElement("div");
    main.className = "list-row-main";

    const name = document.createElement("div");
    name.className = "list-row-name";
    name.textContent = `${worker.harness} / ${worker.model}`;
    main.appendChild(name);

    const usageText = worker.usages
      .map((u) => `${u.workflow}: ${u.role}` + (u.effort ? ` (effort: ${u.effort})` : ""))
      .join(", ");
    const meta = document.createElement("div");
    meta.className = "list-row-meta";
    meta.textContent = usageText;
    main.appendChild(meta);

    row.appendChild(main);

    const actions = document.createElement("div");
    actions.className = "list-row-actions";
    const badge = document.createElement("span");
    badge.className = "badge enabled";
    badge.textContent = worker.usages.length + " usage" + (worker.usages.length === 1 ? "" : "s");
    actions.appendChild(badge);
    row.appendChild(actions);

    return row;
  }
}

// ---- work (task) view ----

function renderWork(container) {
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
  header.innerHTML = `<h2 id="inbox-count">Pending review</h2>`;
  listCard.appendChild(header);
  const list = document.createElement("div");
  list.className = "list";
  listCard.appendChild(list);
  wrap.appendChild(listCard);

  const hint = document.createElement("p");
  hint.className = "hint";
  hint.textContent = "Runs currently parked at REVIEW_PENDING, oldest first. This is not a general Run browser — " +
    "see the Temporal UI for full trace/replay.";
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
    document.getElementById("inbox-count").textContent = `Pending review — ${pending.length}`;

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
    const bits = [item.workflow, "escalated from " + item.from_step];
    if (item.attempt_number) bits.push("attempt " + item.attempt_number);
    bits.push(humanizeAge(item.occurred_at));
    meta.textContent = bits.join("  ·  ");
    main.appendChild(meta);
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
