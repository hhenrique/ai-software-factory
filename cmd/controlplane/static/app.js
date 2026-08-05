// Control plane SPA shell. Vanilla JS, no framework/build step — kept
// simple on purpose (docs/04: thin MVP), but structured so a new section
// is one entry in VIEWS plus one render function, not a rewrite of the
// shell. Routing is just location.hash -> VIEWS lookup; there's exactly
// one view today (repositories), but the registry/render contract is
// already the shape a second view would follow.

const VIEWS = {
  repositories: { label: "Repositories", render: renderRepositories },
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
    <div class="form-row">
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
        <input id="rf-workflow" type="text" placeholder="workflows/issue-to-pr-claude-only.yaml">
      </div>
      <button class="primary" id="rf-submit">+ Add repository</button>
    </div>
    <p class="hint">GitHub is the only managed provider in this release.</p>
  `;
  wrap.appendChild(formCard);

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
      list.appendChild(row);
    }
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
