(() => {
  const state = {
    meta: null,
    tools: [],
    workflows: [],
    currentName: null,
    dirty: false,
    selectedId: null,
    selectedEdge: null,
    view: { x: 0, y: 0, scale: 1 },
    draft: emptyWorkflow(),
    layout: { nodes: {} },
    drag: null,
    link: null,
    pan: null,
  };

  const el = {
    list: document.getElementById("workflow-list"),
    metaFoot: document.getElementById("meta-foot"),
    canvas: document.getElementById("canvas"),
    edges: document.getElementById("edges"),
    wrap: document.getElementById("canvas-wrap"),
    status: document.getElementById("status"),
    name: document.getElementById("wf-name"),
    desc: document.getElementById("wf-desc"),
    mode: document.getElementById("wf-mode"),
    scope: document.getElementById("wf-scope"),
    form: document.getElementById("inspector-form"),
    empty: document.getElementById("inspector-empty"),
    nodeId: document.getElementById("node-id"),
    nodeDesc: document.getElementById("node-desc"),
    nodeDiff: document.getElementById("node-diff"),
    toolsToggle: document.getElementById("node-tools-toggle"),
    toolsPanel: document.getElementById("node-tools-panel"),
    depsToggle: document.getElementById("node-deps-toggle"),
    depsPanel: document.getElementById("node-deps-panel"),
    nodePrompt: document.getElementById("node-prompt"),
    // settings elements
    settingsStatus: document.getElementById("settings-status"),
    settingsDirty: document.getElementById("settings-dirty"),
    providerCount: document.getElementById("provider-count"),
    modelCount: document.getElementById("model-count"),
    setProvider: document.getElementById("set-provider"),
    setModel: document.getElementById("set-model"),
    setEffort: document.getElementById("set-effort"),
    setMaxTurns: document.getElementById("set-max-turns"),
    setMaxContext: document.getElementById("set-max-context"),
    providerList: document.getElementById("provider-list"),
    modelList: document.getElementById("model-list"),
    mcpList: document.getElementById("mcp-list"),
    skillList: document.getElementById("skill-list"),
    newProvName: document.getElementById("new-prov-name"),
    newProvKey: document.getElementById("new-prov-key"),
    newProvUrl: document.getElementById("new-prov-url"),
    newProvFormat: document.getElementById("new-prov-format"),
    newModelProvider: document.getElementById("new-model-provider"),
    newModelId: document.getElementById("new-model-id"),
    newMcpName: document.getElementById("new-mcp-name"),
    newMcpScope: document.getElementById("new-mcp-scope"),
    newMcpTransport: document.getElementById("new-mcp-transport"),
    newMcpCommand: document.getElementById("new-mcp-command"),
    newMcpArgs: document.getElementById("new-mcp-args"),
    newMcpUrl: document.getElementById("new-mcp-url"),
    newMcpEnv: document.getElementById("new-mcp-env"),
    newMcpHeaders: document.getElementById("new-mcp-headers"),
    // features (computer use)
    setComputerUse: document.getElementById("set-computer-use"),
    // jev
    jevState: document.getElementById("jev-state"),
    setJevEnabled: document.getElementById("set-jev-enabled"),
    setJevBaseUrl: document.getElementById("set-jev-base-url"),
    setJevModel: document.getElementById("set-jev-model"),
    setJevApiKey: document.getElementById("set-jev-api-key"),
    setJevApiKeyEnv: document.getElementById("set-jev-api-key-env"),
    setJevTimeout: document.getElementById("set-jev-timeout"),
    setJevConfidence: document.getElementById("set-jev-confidence"),
    setJevRouting: document.getElementById("set-jev-routing"),
    setJevMemoryJudge: document.getElementById("set-jev-memory-judge"),
    setJevGuardrail: document.getElementById("set-jev-guardrail"),
  };

  function emptyWorkflow() {
    return {
      name: "new-workflow",
      description: "Describe this workflow",
      execution_mode: "auto",
      tasks: [
        {
          id: "step-1",
          description: "First step",
          prompt: "Do the first step. Args: {{args}}",
          difficulty: "easy",
          allowed_tools: [],
          depends_on: [],
        },
      ],
    };
  }

  function setStatus(msg, kind = "") {
    el.status.textContent = msg || "";
    el.status.className = "status" + (kind ? " " + kind : "");
  }

  async function api(path, opts) {
    const res = await fetch(path, { cache: "no-store", ...(opts || {}) });
    if (!res.ok) {
      const text = await res.text();
      throw new Error(text || res.statusText);
    }
    if (res.status === 204) return null;
    return res.json();
  }

  function markDirty() {
    state.dirty = true;
  }

  function taskById(id) {
    return state.draft.tasks.find((t) => t.id === id);
  }

  function ensureLayout() {
    if (!state.layout.nodes) state.layout.nodes = {};
    state.draft.tasks.forEach((t, i) => {
      if (!state.layout.nodes[t.id]) {
        state.layout.nodes[t.id] = { x: 80 + (i % 3) * 280, y: 80 + Math.floor(i / 3) * 140 };
      }
    });
  }

  function applyView() {
    const t = `translate(${state.view.x}px, ${state.view.y}px) scale(${state.view.scale})`;
    el.canvas.style.transform = t;
    el.edges.style.transform = t;
    el.edges.style.transformOrigin = "0 0";
  }

  function renderList() {
    el.list.innerHTML = "";
    if (!state.workflows.length) {
      el.list.innerHTML = `<div class="muted" style="padding:8px 12px">No workflows loaded.</div>`;
      return;
    }
    state.workflows.forEach((wf) => {
      const item = document.createElement("div");
      item.className = "list-item" + (wf.name === state.currentName ? " active" : "");
      item.innerHTML = `<div class="name">${escapeHtml(wf.name)}</div><div class="desc">${escapeHtml(wf.description || `${(wf.tasks || []).length} tasks`)}</div>`;
      item.onclick = () => loadWorkflow(wf.name);
      el.list.appendChild(item);
    });
  }

  function renderNodes() {
    ensureLayout();
    el.canvas.innerHTML = "";
    state.draft.tasks.forEach((task) => {
      const pos = state.layout.nodes[task.id] || { x: 80, y: 80 };
      const node = document.createElement("div");
      node.className = "node" + (state.selectedId === task.id ? " selected" : "");
      node.dataset.id = task.id;
      node.style.left = pos.x + "px";
      node.style.top = pos.y + "px";
      const diff = task.difficulty || "default";
      node.innerHTML = `
        <div class="node-header"><span>${escapeHtml(task.id)}</span><span class="node-badge">${escapeHtml(diff)}</span></div>
        <div class="node-body">${escapeHtml(task.description || "")}</div>
        <div class="node-ports">
          <div class="port in" data-port="in" data-id="${escapeAttr(task.id)}" title="input"></div>
          <div class="port out" data-port="out" data-id="${escapeAttr(task.id)}" title="output"></div>
        </div>`;
      node.addEventListener("mousedown", onNodeMouseDown);
      node.querySelectorAll(".port").forEach((p) => p.addEventListener("mousedown", onPortMouseDown));
      el.canvas.appendChild(node);
    });
    renderEdges();
    renderInspector();
  }

  function nodeCenter(id, side) {
    const pos = state.layout.nodes[id] || { x: 0, y: 0 };
    const w = 220, h = 88;
    return {
      x: side === "out" ? pos.x + w : pos.x,
      y: pos.y + h / 2,
    };
  }

  function bezier(a, b) {
    const dx = Math.max(40, Math.abs(b.x - a.x) * 0.5);
    return `M ${a.x} ${a.y} C ${a.x + dx} ${a.y}, ${b.x - dx} ${b.y}, ${b.x} ${b.y}`;
  }

  function renderEdges() {
    const parts = [];
    state.draft.tasks.forEach((task) => {
      (task.depends_on || []).forEach((dep) => {
        if (!taskById(dep)) return;
        const a = nodeCenter(dep, "out");
        const b = nodeCenter(task.id, "in");
        const d = bezier(a, b);
        const key = dep + "->" + task.id;
        const active = state.selectedEdge === key ? " active" : "";
        parts.push(`<path class="edge-hit" data-edge="${escapeAttr(key)}" d="${d}"></path>`);
        parts.push(`<path class="edge-path${active}" data-edge="${escapeAttr(key)}" d="${d}"></path>`);
      });
    });
    if (state.link) {
      const a = nodeCenter(state.link.from, "out");
      const b = state.link.cursor;
      parts.push(`<path class="edge-path active" d="${bezier(a, b)}"></path>`);
    }
    el.edges.innerHTML = parts.join("");
    el.edges.querySelectorAll(".edge-hit").forEach((p) => {
      p.addEventListener("mousedown", (e) => {
        e.stopPropagation();
        state.selectedEdge = p.dataset.edge;
        state.selectedId = null;
        renderNodes();
      });
    });
  }

  function renderInspector() {
    const task = state.selectedId ? taskById(state.selectedId) : null;
    if (!task) {
      el.form.classList.add("hidden");
      el.empty.classList.remove("hidden");
      closeMultiSelects();
      return;
    }
    el.empty.classList.add("hidden");
    el.form.classList.remove("hidden");
    el.nodeId.value = task.id || "";
    el.nodeDesc.value = task.description || "";
    el.nodeDiff.value = task.difficulty || "";
    el.nodePrompt.value = task.prompt || "";
    renderToolOptions(task.allowed_tools || []);
    renderDepOptions(task.depends_on || []);
  }

  // Built-in tools that only inspect state / files (IsReadOnly=true).
  // Skill is intentionally excluded from presets (meta tool).
  const READ_ONLY_TOOLS = new Set([
    "Diff",
    "Fetch",
    "Glob",
    "Grep",
    "LS",
    "LSP",
    "ReadMemory",
    "View",
    "ViewImage",
    "WebSearch",
  ]);

  // Meta / orchestration tools excluded from 读写 preset.
  const PRESET_EXCLUDE_TOOLS = new Set(["AskUser", "Skill", "Task"]);

  function isMCPTool(name) {
    return String(name || "").startsWith("mcp__");
  }

  function isPresetExcluded(name) {
    return PRESET_EXCLUDE_TOOLS.has(name) || isMCPTool(name);
  }

  function toolOptionsList(selected) {
    return uniqueList([...(state.tools || []), ...(selected || [])]);
  }

  function presetReadOnly(options) {
    return options.filter((name) => READ_ONLY_TOOLS.has(name) && !isPresetExcluded(name));
  }

  // 读写 = all available coding tools, minus MCP / Skill / AskUser / Task.
  function presetReadWrite(options) {
    return options.filter((name) => !isPresetExcluded(name));
  }

  function applyToolSelection(names) {
    const task = taskById(state.selectedId);
    if (!task) return;
    const selected = uniqueList(names || []);
    task.allowed_tools = selected;
    renderToolOptions(selected);
    markDirty();
    renderNodesKeepInspector();
  }

  function renderToolOptions(selected) {
    const selectedSet = new Set(selected || []);
    // Keep unknown tools that are already on the task.
    const options = toolOptionsList(selected);
    const listHTML = options.length
      ? options.map((name) => multiOptionHTML("tool", name, selectedSet.has(name))).join("")
      : `<div class="multi-select-empty">No tools available</div>`;
    el.toolsPanel.innerHTML = `
      <div class="multi-select-actions">
        <button type="button" class="chip" data-preset="readonly" title="Select read-only tools">只读</button>
        <button type="button" class="chip" data-preset="readwrite" title="Select read/write tools (exclude MCP, Skill, AskUser, Task)">读写</button>
        <button type="button" class="chip" data-preset="clear" title="Clear selection">清空</button>
      </div>
      <div class="multi-select-list">${listHTML}</div>`;
    el.toolsToggle.textContent = summarizeSelection(selected, "Select tools…", "tools");

    el.toolsPanel.querySelectorAll("[data-preset]").forEach((btn) => {
      btn.addEventListener("click", (e) => {
        e.preventDefault();
        e.stopPropagation();
        const kind = btn.getAttribute("data-preset");
        if (kind === "readonly") {
          applyToolSelection(presetReadOnly(options));
          return;
        }
        if (kind === "readwrite") {
          applyToolSelection(presetReadWrite(options));
          return;
        }
        if (kind === "clear") {
          applyToolSelection([]);
        }
      });
    });

    el.toolsPanel.querySelectorAll("input[type=checkbox]").forEach((input) => {
      input.addEventListener("change", () => {
        const task = taskById(state.selectedId);
        if (!task) return;
        task.allowed_tools = readChecked(el.toolsPanel);
        el.toolsToggle.textContent = summarizeSelection(task.allowed_tools, "Select tools…", "tools");
        markDirty();
        renderNodesKeepInspector();
      });
    });
  }

  function renderDepOptions(selected) {
    const selectedSet = new Set(selected || []);
    const options = state.draft.tasks
      .map((t) => t.id)
      .filter((id) => id && id !== state.selectedId);
    el.depsPanel.innerHTML = options.length
      ? options.map((name) => multiOptionHTML("dep", name, selectedSet.has(name))).join("")
      : `<div class="multi-select-empty">No other nodes</div>`;
    el.depsToggle.textContent = summarizeSelection(selected, "Select dependencies…", "deps");
    el.depsPanel.querySelectorAll("input[type=checkbox]").forEach((input) => {
      input.addEventListener("change", () => {
        const task = taskById(state.selectedId);
        if (!task) return;
        task.depends_on = readChecked(el.depsPanel);
        el.depsToggle.textContent = summarizeSelection(task.depends_on, "Select dependencies…", "deps");
        markDirty();
        renderNodesKeepInspector();
      });
    });
  }

  function multiOptionHTML(kind, name, checked) {
    return `<label class="multi-select-item">
      <input type="checkbox" data-kind="${kind}" value="${escapeAttr(name)}" ${checked ? "checked" : ""} />
      <span>${escapeHtml(name)}</span>
    </label>`;
  }

  function readChecked(panel) {
    return Array.from(panel.querySelectorAll("input[type=checkbox]:checked")).map((el) => el.value);
  }

  function summarizeSelection(values, emptyLabel, unit) {
    const list = values || [];
    if (!list.length) return emptyLabel;
    if (list.length <= 2) return list.join(", ");
    return `${list.length} ${unit} selected`;
  }

  function uniqueList(values) {
    const out = [];
    const seen = new Set();
    values.forEach((v) => {
      const s = String(v || "").trim();
      if (!s || seen.has(s)) return;
      seen.add(s);
      out.push(s);
    });
    out.sort((a, b) => a.localeCompare(b));
    return out;
  }

  function closeMultiSelects() {
    el.toolsPanel.classList.add("hidden");
    el.depsPanel.classList.add("hidden");
  }

  function togglePanel(panel) {
    const opening = panel.classList.contains("hidden");
    closeMultiSelects();
    if (opening) panel.classList.remove("hidden");
  }

  function renderNodesKeepInspector() {
    // lightweight node redraw used when only tools/deps change
    ensureLayout();
    const selected = state.selectedId;
    const edge = state.selectedEdge;
    // preserve open dropdown state
    const toolsOpen = !el.toolsPanel.classList.contains("hidden");
    const depsOpen = !el.depsPanel.classList.contains("hidden");
    el.canvas.innerHTML = "";
    state.draft.tasks.forEach((task) => {
      const pos = state.layout.nodes[task.id] || { x: 80, y: 80 };
      const node = document.createElement("div");
      node.className = "node" + (selected === task.id ? " selected" : "");
      node.dataset.id = task.id;
      node.style.left = pos.x + "px";
      node.style.top = pos.y + "px";
      const diff = task.difficulty || "default";
      node.innerHTML = `
        <div class="node-header"><span>${escapeHtml(task.id)}</span><span class="node-badge">${escapeHtml(diff)}</span></div>
        <div class="node-body">${escapeHtml(task.description || "")}</div>
        <div class="node-ports">
          <div class="port in" data-port="in" data-id="${escapeAttr(task.id)}" title="input"></div>
          <div class="port out" data-port="out" data-id="${escapeAttr(task.id)}" title="output"></div>
        </div>`;
      node.addEventListener("mousedown", onNodeMouseDown);
      node.querySelectorAll(".port").forEach((p) => p.addEventListener("mousedown", onPortMouseDown));
      el.canvas.appendChild(node);
    });
    state.selectedId = selected;
    state.selectedEdge = edge;
    renderEdges();
    if (toolsOpen) el.toolsPanel.classList.remove("hidden");
    if (depsOpen) el.depsPanel.classList.remove("hidden");
  }

  function syncTopbarFromDraft() {
    el.name.value = state.draft.name || "";
    el.desc.value = state.draft.description || "";
    el.mode.value = state.draft.execution_mode || "auto";
  }

  function syncDraftFromTopbar() {
    state.draft.name = (el.name.value || "").trim().toLowerCase().replace(/\s+/g, "-");
    state.draft.description = el.desc.value.trim();
    state.draft.execution_mode = el.mode.value;
  }

  function selectNode(id) {
    state.selectedId = id;
    state.selectedEdge = null;
    renderNodes();
  }

  function onNodeMouseDown(e) {
    if (e.button !== 0) return;
    if (e.target.classList.contains("port")) return;
    const id = e.currentTarget.dataset.id;
    selectNode(id);
    const pos = state.layout.nodes[id];
    state.drag = {
      id,
      startX: e.clientX,
      startY: e.clientY,
      origX: pos.x,
      origY: pos.y,
    };
    e.preventDefault();
  }

  function onPortMouseDown(e) {
    e.stopPropagation();
    e.preventDefault();
    const port = e.currentTarget.dataset.port;
    const id = e.currentTarget.dataset.id;
    if (port !== "out") return;
    const cursor = clientToWorld(e.clientX, e.clientY);
    state.link = { from: id, cursor };
    state.selectedEdge = null;
    renderEdges();
  }

  function clientToWorld(clientX, clientY) {
    const rect = el.wrap.getBoundingClientRect();
    return {
      x: (clientX - rect.left - state.view.x) / state.view.scale,
      y: (clientY - rect.top - state.view.y) / state.view.scale,
    };
  }

  function onMouseMove(e) {
    if (state.drag) {
      const dx = (e.clientX - state.drag.startX) / state.view.scale;
      const dy = (e.clientY - state.drag.startY) / state.view.scale;
      state.layout.nodes[state.drag.id] = {
        x: state.drag.origX + dx,
        y: state.drag.origY + dy,
      };
      markDirty();
      const node = el.canvas.querySelector(`.node[data-id="${cssEscape(state.drag.id)}"]`);
      if (node) {
        node.style.left = state.layout.nodes[state.drag.id].x + "px";
        node.style.top = state.layout.nodes[state.drag.id].y + "px";
      }
      renderEdges();
      return;
    }
    if (state.link) {
      state.link.cursor = clientToWorld(e.clientX, e.clientY);
      renderEdges();
      return;
    }
    if (state.pan) {
      state.view.x = state.pan.origX + (e.clientX - state.pan.startX);
      state.view.y = state.pan.origY + (e.clientY - state.pan.startY);
      applyView();
    }
  }

  function onMouseUp(e) {
    if (state.link) {
      const target = document.elementFromPoint(e.clientX, e.clientY);
      const port = target && target.classList && target.classList.contains("port") ? target : null;
      if (port && port.dataset.port === "in") {
        const to = port.dataset.id;
        const from = state.link.from;
        if (to && from && to !== from) {
          const task = taskById(to);
          if (task) {
            task.depends_on = task.depends_on || [];
            if (!task.depends_on.includes(from)) {
              task.depends_on.push(from);
              markDirty();
              setStatus(`Linked ${from} → ${to}`, "ok");
            }
          }
        }
      }
      state.link = null;
      renderNodes();
    }
    state.drag = null;
    state.pan = null;
  }

  function addNode() {
    let n = 1;
    const used = new Set(state.draft.tasks.map((t) => t.id));
    while (used.has("step-" + n)) n++;
    const id = "step-" + n;
    state.draft.tasks.push({
      id,
      description: "New step",
      prompt: "Describe what this step should do. Args: {{args}}",
      difficulty: "easy",
      allowed_tools: [],
      depends_on: [],
    });
    state.layout.nodes[id] = {
      x: 120 + (state.draft.tasks.length % 4) * 40,
      y: 120 + (state.draft.tasks.length % 4) * 40,
    };
    markDirty();
    selectNode(id);
    setStatus("Node added", "ok");
  }

  function deleteSelected() {
    if (state.selectedEdge) {
      const [from, to] = state.selectedEdge.split("->");
      const task = taskById(to);
      if (task) {
        task.depends_on = (task.depends_on || []).filter((d) => d !== from);
        markDirty();
      }
      state.selectedEdge = null;
      renderNodes();
      setStatus("Edge removed", "ok");
      return;
    }
    if (!state.selectedId) return;
    if (state.draft.tasks.length <= 1) {
      setStatus("Keep at least one node", "err");
      return;
    }
    const removed = state.selectedId;
    state.draft.tasks = state.draft.tasks.filter((t) => t.id !== removed);
    state.draft.tasks.forEach((t) => {
      t.depends_on = (t.depends_on || []).filter((d) => d !== removed);
    });
    delete state.layout.nodes[removed];
    state.selectedId = state.draft.tasks[0]?.id || null;
    markDirty();
    renderNodes();
    setStatus("Node deleted", "ok");
  }

  function autoLayout() {
    // simple level layout based on depends_on
    const ids = state.draft.tasks.map((t) => t.id);
    const levelOf = {};
    const byId = Object.fromEntries(state.draft.tasks.map((t) => [t.id, t]));
    let guard = 0;
    while (Object.keys(levelOf).length < ids.length && guard++ < 100) {
      ids.forEach((id) => {
        if (levelOf[id] != null) return;
        const deps = (byId[id].depends_on || []).filter((d) => byId[d]);
        if (deps.every((d) => levelOf[d] != null)) {
          levelOf[id] = deps.length ? Math.max(...deps.map((d) => levelOf[d])) + 1 : 0;
        }
      });
    }
    const buckets = {};
    ids.forEach((id) => {
      const lvl = levelOf[id] ?? 0;
      buckets[lvl] = buckets[lvl] || [];
      buckets[lvl].push(id);
    });
    Object.keys(buckets).forEach((lvl) => {
      buckets[lvl].forEach((id, i) => {
        state.layout.nodes[id] = { x: 80 + Number(lvl) * 280, y: 80 + i * 140 };
      });
    });
    markDirty();
    renderNodes();
    setStatus("Auto layout applied", "ok");
  }

  async function refreshList() {
    state.workflows = await api("/api/workflows");
    renderList();
  }

  async function loadWorkflow(name) {
    const wf = await api("/api/workflows/" + encodeURIComponent(name));
    state.currentName = wf.name;
    state.draft = {
      name: wf.name,
      description: wf.description || "",
      execution_mode: wf.execution_mode || "auto",
      tasks: (wf.tasks || []).map(normalizeTask),
    };
    state.layout = wf.layout || { nodes: {} };
    state.selectedId = state.draft.tasks[0]?.id || null;
    state.selectedEdge = null;
    state.dirty = false;
    syncTopbarFromDraft();
    renderList();
    renderNodes();
    setStatus("Loaded " + wf.name, "ok");
  }

  function normalizeTask(t, i) {
    return {
      id: t.id || "task-" + (i + 1),
      description: t.description || "",
      prompt: t.prompt || "",
      difficulty: t.difficulty || "",
      model: t.model || "",
      allowed_tools: t.allowed_tools || [],
      depends_on: t.depends_on || [],
    };
  }

  function newWorkflow() {
    state.currentName = null;
    state.draft = emptyWorkflow();
    state.layout = { nodes: { "step-1": { x: 120, y: 120 } } };
    state.selectedId = "step-1";
    state.selectedEdge = null;
    state.dirty = true;
    syncTopbarFromDraft();
    renderList();
    renderNodes();
    setStatus("New workflow draft", "ok");
  }

  async function saveWorkflow() {
    syncDraftFromTopbar();
    // flush inspector into selected task first
    flushInspector();
    const payload = {
      name: state.draft.name,
      description: state.draft.description,
      execution_mode: state.draft.execution_mode,
      tasks: state.draft.tasks,
      scope: el.scope.value || "project",
      layout: state.layout,
    };
    try {
      const res = await api("/api/save", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      state.dirty = false;
      state.currentName = res.name;
      await refreshList();
      setStatus("Saved to " + res.path, "ok");
    } catch (err) {
      setStatus(String(err.message || err), "err");
    }
  }

  async function deleteWorkflow() {
    const name = state.currentName || (el.name.value || "").trim().toLowerCase().replace(/\s+/g, "-");
    if (!name) {
      setStatus("No workflow selected to delete", "err");
      return;
    }
    const ok = window.confirm(`Delete workflow "${name}" from disk?\nThis cannot be undone.`);
    if (!ok) return;
    try {
      await api("/api/workflows/" + encodeURIComponent(name), { method: "DELETE" });
      state.currentName = null;
      state.dirty = false;
      await refreshList();
      if (state.workflows.length) {
        await loadWorkflow(state.workflows[0].name);
      } else {
        newWorkflow();
      }
      setStatus("Deleted workflow " + name, "ok");
    } catch (err) {
      setStatus(String(err.message || err), "err");
    }
  }

  function flushInspector() {
    if (!state.selectedId) return;
    const task = taskById(state.selectedId);
    if (!task) return;
    const oldId = task.id;
    let newId = (el.nodeId.value || "").trim().toLowerCase().replace(/\s+/g, "-");
    if (!newId) newId = oldId;
    if (newId !== oldId) {
      if (taskById(newId)) {
        setStatus("Duplicate task id: " + newId, "err");
        el.nodeId.value = oldId;
        return;
      }
      task.id = newId;
      if (state.layout.nodes[oldId]) {
        state.layout.nodes[newId] = state.layout.nodes[oldId];
        delete state.layout.nodes[oldId];
      }
      state.draft.tasks.forEach((t) => {
        t.depends_on = (t.depends_on || []).map((d) => (d === oldId ? newId : d));
      });
      state.selectedId = newId;
    }
    task.description = el.nodeDesc.value.trim();
    task.difficulty = el.nodeDiff.value;
    task.prompt = el.nodePrompt.value;
    task.allowed_tools = readChecked(el.toolsPanel);
    task.depends_on = readChecked(el.depsPanel);
    markDirty();
  }

  // ---- Settings ----

  const settings = {
    data: null,
    draft: null,
    dirty: false,
  };

  function setSettingsStatus(msg, kind = "") {
    el.settingsStatus.textContent = msg || "";
    el.settingsStatus.className = "status" + (kind ? " " + kind : "");
  }

  function switchTab(tab) {
    document.querySelectorAll(".tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === tab));
    document.getElementById("tab-workflow").classList.toggle("hidden", tab !== "workflow");
    document.getElementById("tab-settings").classList.toggle("hidden", tab !== "settings");
    if (tab === "settings" && !settings.draft) {
      loadSettingsV2();
    }
  }

  // ---- Settings rewrite: one local draft, one explicit save ----

  function copySettings(value) {
    return JSON.parse(JSON.stringify(value || {}));
  }

  function markSettingsDirty() {
    settings.dirty = true;
    el.settingsDirty.textContent = "Unsaved changes";
    el.settingsDirty.classList.add("dirty");
  }

  function clearSettingsDirty() {
    settings.dirty = false;
    el.settingsDirty.textContent = "All changes are saved together.";
    el.settingsDirty.classList.remove("dirty");
  }

  async function loadSettingsV2() {
    try {
      const data = await api("/api/settings");
      settings.data = data;
      settings.draft = copySettings(data);
      clearSettingsDirty();
      renderSettingsV2();
    } catch (err) {
      setSettingsStatus(String(err.message || err), "err");
    }
  }

  function syncRuntimeSettings() {
    const d = settings.draft;
    if (!d) return;
    d.provider = el.setProvider.value;
    d.model = el.setModel.value;
    d.effort = el.setEffort.value;
    d.max_turns = numberInput(el.setMaxTurns.value, d.max_turns);
    d.max_context_tokens = numberInput(el.setMaxContext.value, d.max_context_tokens);
    syncFeatureSettings(d);
  }

  function syncFeatureSettings(d) {
    d.computer_use = { enabled: Boolean(el.setComputerUse.checked) };
    const jev = d.jev || (d.jev = {});
    jev.enabled = Boolean(el.setJevEnabled.checked);
    jev.base_url = el.setJevBaseUrl.value.trim();
    jev.model = el.setJevModel.value.trim();
    jev.api_key_env = el.setJevApiKeyEnv.value.trim();
    jev.timeout_sec = numberInput(el.setJevTimeout.value, jev.timeout_sec);
    jev.route_min_confidence = floatInput(el.setJevConfidence.value, jev.route_min_confidence);
    jev.routing = Boolean(el.setJevRouting.checked);
    jev.memory_judge = Boolean(el.setJevMemoryJudge.checked);
    jev.guardrail = Boolean(el.setJevGuardrail.checked);
    // The key is write-only: an empty field means "keep what is stored", so it
    // is only included in the payload when the user actually typed something.
    const typedKey = el.setJevApiKey.value.trim();
    if (typedKey) {
      jev.api_key = typedKey;
      // Reflect it in the badge immediately so the state label is not stale
      // until the next load.
      jev.api_key_set = true;
    } else {
      delete jev.api_key;
    }
  }

  function floatInput(value, fallback) {
    const raw = String(value).trim();
    if (raw === "") return fallback ?? 0;
    const parsed = Number.parseFloat(raw);
    return Number.isFinite(parsed) ? parsed : fallback ?? 0;
  }

  function numberInput(value, fallback) {
    if (String(value).trim() === "") return fallback || 0;
    const parsed = Number.parseInt(value, 10);
    return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback || 0;
  }

  function providerByName(name) {
    return (settings.draft?.providers || []).find((p) => p.name === name);
  }

  function renderRuntimeModelOptions() {
    const d = settings.draft;
    const provider = providerByName(d.provider);
    const models = provider?.models || [];
    el.setModel.innerHTML = models.length
      ? models.map((name) => `<option value="${escapeAttr(name)}">${escapeHtml(name)}</option>`).join("")
      : `<option value="">No models configured</option>`;
    if (models.includes(d.model)) {
      el.setModel.value = d.model;
    } else {
      d.model = models[0] || "";
      el.setModel.value = d.model;
    }
  }

  function renderSettingsV2() {
    const d = settings.draft;
    if (!d) return;
    const providers = d.providers || [];
    const allModels = providers.flatMap((p) => (p.models || []).map((name) => ({ provider: p.name, name })));

    el.setProvider.innerHTML = providers.length
      ? providers.map((p) => `<option value="${escapeAttr(p.name)}">${escapeHtml(p.name)}</option>`).join("")
      : `<option value="">No providers configured</option>`;
    if (!providers.some((p) => p.name === d.provider)) {
      d.provider = providers[0]?.name || "";
    }
    el.setProvider.value = d.provider;
    renderRuntimeModelOptions();
    el.setEffort.value = d.effort || "high";
    el.setMaxTurns.value = d.max_turns ?? 0;
    el.setMaxContext.value = d.max_context_tokens ?? 0;

    el.providerCount.textContent = `${providers.length}`;
    el.modelCount.textContent = `${allModels.length}`;
    el.providerList.innerHTML = providers.length
      ? providers.map((p) => `<div class="settings-row">
          <div class="settings-row-main">
            <strong>${escapeHtml(p.name)}</strong>
            <span class="badge">${escapeHtml(p.api_format || "anthropic")}</span>
            <span class="badge ${p.api_key_set ? "ok" : ""}">${p.api_key_set ? "key set" : "no key"}</span>
            <div class="settings-row-detail">${escapeHtml(p.base_url || "No base URL")}</div>
          </div>
          <button class="icon-btn danger" data-del-provider="${escapeAttr(p.name)}" title="Delete provider">Delete</button>
        </div>`).join("")
      : `<div class="empty-state">No providers configured.</div>`;

    el.modelList.innerHTML = allModels.length
      ? allModels.map((m) => `<div class="settings-row">
          <div class="settings-row-main"><strong>${escapeHtml(m.name)}</strong><span class="badge">${escapeHtml(m.provider)}</span></div>
          <button class="icon-btn danger" data-del-model-provider="${escapeAttr(m.provider)}" data-del-model="${escapeAttr(m.name)}" title="Delete model">Delete</button>
        </div>`).join("")
      : `<div class="empty-state">No models configured.</div>`;

    el.newModelProvider.innerHTML = providers.length
      ? providers.map((p) => `<option value="${escapeAttr(p.name)}">${escapeHtml(p.name)}</option>`).join("")
      : `<option value="">Add a provider first</option>`;

    el.mcpList.innerHTML = (d.mcp_servers || []).length
      ? d.mcp_servers.map((server) => `<label class="settings-row settings-toggle-row">
          <div class="settings-row-main">
            <strong>${escapeHtml(server.name)}</strong>
            <span class="badge">${escapeHtml(server.transport || server.type || "stdio")}</span>
            <div class="settings-row-detail">${escapeHtml(server.command || server.url || "")}</div>
          </div>
          <input type="checkbox" data-mcp-toggle="${escapeAttr(server.name)}" ${server.disabled ? "" : "checked"} />
        </label>`).join("")
      : `<div class="empty-state">No MCP servers configured.</div>`;

    el.skillList.innerHTML = (d.skills || []).length
      ? d.skills.map((skill) => `<label class="settings-row settings-toggle-row">
          <div class="settings-row-main">
            <strong>${escapeHtml(skill.name)}</strong>
            <div class="settings-row-detail">${escapeHtml(skill.description || "")}</div>
          </div>
          <input type="checkbox" data-skill-toggle="${escapeAttr(skill.name)}" ${skill.enabled ? "checked" : ""} />
        </label>`).join("")
      : `<div class="empty-state">No skills available.</div>`;

    renderFeatureSettings(d);
  }

  function renderFeatureSettings(d) {
    el.setComputerUse.checked = Boolean(d.computer_use?.enabled);

    const jev = d.jev || {};
    el.setJevEnabled.checked = Boolean(jev.enabled);
    el.setJevBaseUrl.value = jev.base_url || "";
    el.setJevModel.value = jev.model || "";
    el.setJevApiKeyEnv.value = jev.api_key_env || "";
    el.setJevTimeout.value = jev.timeout_sec ?? 20;
    el.setJevConfidence.value = jev.route_min_confidence ?? 0.6;
    el.setJevRouting.checked = Boolean(jev.routing);
    el.setJevMemoryJudge.checked = Boolean(jev.memory_judge);
    el.setJevGuardrail.checked = Boolean(jev.guardrail);
    // Never echo a stored key back into the page.
    el.setJevApiKey.value = "";
    el.setJevApiKey.placeholder = jev.api_key_set
      ? "A key is stored — leave blank to keep it"
      : "Leave blank to use the env var below";

    el.jevState.textContent = jevStateLabel(jev);
    el.jevState.className = "badge" + (jevStateLabel(jev) === "active" ? " ok" : "");
  }

  // jevStateLabel mirrors the backend rule: enabled without a resolvable key is
  // not active, because every call would fail and fall back.
  function jevStateLabel(jev) {
    if (!jev.enabled) return "off";
    if (!jev.api_key_set && !(jev.api_key_env || "").trim()) return "needs a key";
    return "active";
  }

  // refreshJevBadge updates the badge without re-rendering the whole form, so
  // typing in a text field does not lose focus.
  function refreshJevBadge() {
    const jev = settings.draft?.jev || {};
    const label = jevStateLabel(jev);
    el.jevState.textContent = label;
    el.jevState.className = "badge" + (label === "active" ? " ok" : "");
  }

  async function saveSettingsV2() {
    if (!settings.draft) return;
    syncRuntimeSettings();
    const d = settings.draft;
    const payload = {
      provider: d.provider,
      model: d.model,
      effort: d.effort,
      max_turns: d.max_turns,
      max_context_tokens: d.max_context_tokens,
      mcp_disabled: Object.fromEntries((d.mcp_servers || []).map((s) => [s.name, Boolean(s.disabled)])),
      skills_enabled: Object.fromEntries((d.skills || []).filter((s) => s.enabled).map((s) => [s.name, true])),
      skills_disabled: Object.fromEntries((d.skills || []).filter((s) => !s.enabled).map((s) => [s.name, true])),
      computer_use_enabled: Boolean(d.computer_use?.enabled),
      jev_enabled: Boolean(d.jev?.enabled),
      jev_base_url: d.jev?.base_url || "",
      jev_api_key_env: d.jev?.api_key_env || "",
      jev_model: d.jev?.model || "",
      jev_timeout_sec: d.jev?.timeout_sec ?? 20,
      jev_route_min_confidence: d.jev?.route_min_confidence ?? 0.6,
      jev_routing: Boolean(d.jev?.routing),
      jev_memory_judge: Boolean(d.jev?.memory_judge),
      jev_guardrail: Boolean(d.jev?.guardrail),
    };
    // Only send the key when the user typed one; otherwise the stored key stays.
    if (d.jev?.api_key) {
      payload.jev_api_key = d.jev.api_key;
    }
    try {
      await api("/api/settings", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      await loadSettingsV2();
      setSettingsStatus("Settings saved and verified. Applying runtime changes in the background.", "ok");
    } catch (err) {
      setSettingsStatus(String(err.message || err), "err");
    }
  }

  async function persistDraftBeforeStructureChange() {
    if (!settings.dirty) return true;
    await saveSettingsV2();
    return !settings.dirty;
  }

  async function addProviderV2() {
    const payload = {
      name: el.newProvName.value.trim(),
      api_key: el.newProvKey.value.trim(),
      base_url: el.newProvUrl.value.trim(),
      api_format: el.newProvFormat.value,
    };
    if (!payload.name || !payload.base_url) {
      setSettingsStatus("Provider name and base URL are required.", "err");
      return;
    }
    if (!(await persistDraftBeforeStructureChange())) return;
    try {
      await api("/api/providers", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
      el.newProvName.value = "";
      el.newProvKey.value = "";
      el.newProvUrl.value = "";
      await loadSettingsV2();
      setSettingsStatus(`Provider "${payload.name}" added.`, "ok");
    } catch (err) {
      setSettingsStatus(String(err.message || err), "err");
    }
  }

  async function deleteProviderV2(name) {
    if (!confirm(`Delete provider "${name}" and its models?`)) return;
    if (!(await persistDraftBeforeStructureChange())) return;
    try {
      await api("/api/providers/" + encodeURIComponent(name), { method: "DELETE" });
      await loadSettingsV2();
      setSettingsStatus(`Provider "${name}" deleted.`, "ok");
    } catch (err) {
      setSettingsStatus(String(err.message || err), "err");
    }
  }

  async function addModelV2() {
    const payload = { provider: el.newModelProvider.value, model: el.newModelId.value.trim() };
    if (!payload.provider || !payload.model) {
      setSettingsStatus("Provider and model ID are required.", "err");
      return;
    }
    if (!(await persistDraftBeforeStructureChange())) return;
    try {
      await api("/api/models", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
      el.newModelId.value = "";
      await loadSettingsV2();
      setSettingsStatus(`Model "${payload.model}" added.`, "ok");
    } catch (err) {
      setSettingsStatus(String(err.message || err), "err");
    }
  }

  async function deleteModelV2(provider, model) {
    if (!confirm(`Delete model "${model}" from "${provider}"?`)) return;
    if (!(await persistDraftBeforeStructureChange())) return;
    try {
      await api(`/api/models/${encodeURIComponent(provider)}/${encodeURIComponent(model)}`, { method: "DELETE" });
      await loadSettingsV2();
      setSettingsStatus(`Model "${model}" deleted.`, "ok");
    } catch (err) {
      setSettingsStatus(String(err.message || err), "err");
    }
  }

  async function addMCPServerV2() {
    const transport = el.newMcpTransport.value;
    const payload = {
      name: el.newMcpName.value.trim(),
      scope: el.newMcpScope.value,
      transport,
      command: el.newMcpCommand.value.trim(),
      args: parseDelimitedList(el.newMcpArgs.value),
      url: el.newMcpUrl.value.trim(),
      env: parseKeyValueLines(el.newMcpEnv.value, "="),
      headers: parseKeyValueLines(el.newMcpHeaders.value, ":"),
    };
    if (!payload.name) {
      setSettingsStatus("MCP server name is required.", "err");
      return;
    }
    if (transport === "stdio" && !payload.command) {
      setSettingsStatus("A command is required for stdio MCP servers.", "err");
      return;
    }
    if ((transport === "http" || transport === "sse") && !payload.url) {
      setSettingsStatus(`A URL is required for ${transport} MCP servers.`, "err");
      return;
    }
    if (!(await persistDraftBeforeStructureChange())) return;
    try {
      await api("/api/mcp-servers", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
      [el.newMcpName, el.newMcpCommand, el.newMcpArgs, el.newMcpUrl, el.newMcpEnv, el.newMcpHeaders].forEach((input) => { input.value = ""; });
      await loadSettingsV2();
      setSettingsStatus(`MCP server "${payload.name}" added to ${payload.scope} settings.`, "ok");
    } catch (err) {
      setSettingsStatus(String(err.message || err), "err");
    }
  }

  function parseDelimitedList(value) {
    return String(value || "").split(/[\n,]/).map((item) => item.trim()).filter(Boolean);
  }

  function parseKeyValueLines(value, separator) {
    const entries = String(value || "").split("\n").map((line) => line.trim()).filter(Boolean);
    const output = {};
    entries.forEach((line) => {
      const index = line.indexOf(separator);
      if (index <= 0) return;
      const key = line.slice(0, index).trim();
      if (key) output[key] = line.slice(index + separator.length).trim();
    });
    return output;
  }

  function escapeHtml(s) {
    return String(s)
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;");
  }
  function escapeAttr(s) {
    return escapeHtml(s).replaceAll("'", "&#39;");
  }
  function cssEscape(s) {
    if (window.CSS && CSS.escape) return CSS.escape(s);
    return String(s).replace(/"/g, '\\"');
  }

  function bind() {
    document.getElementById("btn-new").onclick = newWorkflow;
    document.getElementById("btn-refresh").onclick = async () => {
      await refreshList();
      setStatus("List refreshed", "ok");
    };
    document.getElementById("btn-add-node").onclick = addNode;
    document.getElementById("btn-auto-layout").onclick = autoLayout;
    document.getElementById("btn-delete-wf").onclick = deleteWorkflow;
    document.getElementById("btn-save").onclick = saveWorkflow;
    document.getElementById("btn-del-node").onclick = deleteSelected;

    // Settings bindings: the settings page owns one local draft.
    document.querySelectorAll(".tab").forEach((b) => {
      b.addEventListener("click", () => switchTab(b.dataset.tab));
    });
    document.getElementById("btn-save-settings").onclick = saveSettingsV2;
    document.getElementById("btn-reload-settings").onclick = loadSettingsV2;
    document.getElementById("btn-add-provider").onclick = addProviderV2;
    document.getElementById("btn-add-model").onclick = addModelV2;
    document.getElementById("btn-add-mcp").onclick = addMCPServerV2;

    el.setProvider.addEventListener("change", () => {
      if (!settings.draft) return;
      settings.draft.provider = el.setProvider.value;
      settings.draft.model = "";
      renderRuntimeModelOptions();
      markSettingsDirty();
    });
    [el.setModel, el.setEffort, el.setMaxTurns, el.setMaxContext].forEach((input) => {
      input.addEventListener("change", () => {
        syncRuntimeSettings();
        markSettingsDirty();
      });
      input.addEventListener("input", () => {
        if (input.type === "number") syncRuntimeSettings();
        markSettingsDirty();
      });
    });

    el.providerList.addEventListener("click", (e) => {
      const btn = e.target.closest("[data-del-provider]");
      if (btn) deleteProviderV2(btn.dataset.delProvider);
    });
    el.modelList.addEventListener("click", (e) => {
      const btn = e.target.closest("[data-del-model]");
      if (btn) deleteModelV2(btn.dataset.delModelProvider, btn.dataset.delModel);
    });
    el.mcpList.addEventListener("change", (e) => {
      const input = e.target.closest("[data-mcp-toggle]");
      if (!input || !settings.draft) return;
      const server = (settings.draft.mcp_servers || []).find((s) => s.name === input.dataset.mcpToggle);
      if (!server) return;
      server.disabled = !input.checked;
      markSettingsDirty();
    });
    el.skillList.addEventListener("change", (e) => {
      const input = e.target.closest("[data-skill-toggle]");
      if (!input || !settings.draft) return;
      const skill = (settings.draft.skills || []).find((s) => s.name === input.dataset.skillToggle);
      if (!skill) return;
      skill.enabled = input.checked;
      markSettingsDirty();
    });

    // Features + Jev: sync the draft on every change so the save payload is
    // always built from what the user currently sees.
    [
      el.setComputerUse,
      el.setJevEnabled,
      el.setJevApiKey,
      el.setJevRouting,
      el.setJevMemoryJudge,
      el.setJevGuardrail,
    ].forEach((input) => {
      input.addEventListener("change", () => {
        syncFeatureSettings(settings.draft || {});
        refreshJevBadge();
        markSettingsDirty();
      });
    });
    [
      el.setJevBaseUrl,
      el.setJevModel,
      el.setJevApiKeyEnv,
      el.setJevTimeout,
      el.setJevConfidence,
    ].forEach((input) => {
      input.addEventListener("input", () => {
        syncFeatureSettings(settings.draft || {});
        refreshJevBadge();
        markSettingsDirty();
      });
    });

    el.toolsToggle.addEventListener("click", (e) => {
      e.stopPropagation();
      togglePanel(el.toolsPanel);
    });
    el.depsToggle.addEventListener("click", (e) => {
      e.stopPropagation();
      togglePanel(el.depsPanel);
    });
    el.toolsPanel.addEventListener("click", (e) => e.stopPropagation());
    el.depsPanel.addEventListener("click", (e) => e.stopPropagation());
    document.addEventListener("click", () => closeMultiSelects());

    ["wf-name", "wf-desc", "wf-mode", "wf-scope"].forEach((id) => {
      document.getElementById(id).addEventListener("input", () => {
        syncDraftFromTopbar();
        markDirty();
      });
      document.getElementById(id).addEventListener("change", () => {
        syncDraftFromTopbar();
        markDirty();
      });
    });

    ["node-id", "node-desc", "node-diff", "node-prompt"].forEach((id) => {
      const node = document.getElementById(id);
      node.addEventListener("change", () => {
        flushInspector();
        renderNodes();
      });
      if (node.tagName === "TEXTAREA" || node.type === "text") {
        node.addEventListener("blur", () => {
          flushInspector();
          renderNodes();
        });
      }
    });

    window.addEventListener("mousemove", onMouseMove);
    window.addEventListener("mouseup", onMouseUp);
    window.addEventListener("keydown", (e) => {
      if (e.key === "Delete" || e.key === "Backspace") {
        const tag = (e.target && e.target.tagName) || "";
        if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
        e.preventDefault();
        deleteSelected();
      }
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s") {
        e.preventDefault();
        saveWorkflow();
      }
    });

    el.wrap.addEventListener("mousedown", (e) => {
      if (e.button === 1 || (e.button === 0 && e.target === el.wrap)) {
        state.pan = {
          startX: e.clientX,
          startY: e.clientY,
          origX: state.view.x,
          origY: state.view.y,
        };
        state.selectedId = null;
        state.selectedEdge = null;
        renderNodes();
        e.preventDefault();
      }
    });
    el.wrap.addEventListener("wheel", (e) => {
      e.preventDefault();
      const delta = e.deltaY > 0 ? 0.9 : 1.1;
      const next = Math.min(2.5, Math.max(0.35, state.view.scale * delta));
      const rect = el.wrap.getBoundingClientRect();
      const mx = e.clientX - rect.left;
      const my = e.clientY - rect.top;
      const wx = (mx - state.view.x) / state.view.scale;
      const wy = (my - state.view.y) / state.view.scale;
      state.view.scale = next;
      state.view.x = mx - wx * next;
      state.view.y = my - wy * next;
      applyView();
    }, { passive: false });
  }

  async function init() {
    bind();
    applyView();
    state.meta = await api("/api/meta");
    state.tools = state.meta.tools || [];
    el.metaFoot.textContent = `project: ${state.meta.project_dir || "-"}\nuser: ${state.meta.user_dir || "-"}`;
    await refreshList();
    if (state.workflows.length) {
      await loadWorkflow(state.workflows[0].name);
    } else {
      newWorkflow();
    }
  }

  init().catch((err) => setStatus(String(err.message || err), "err"));
})();
