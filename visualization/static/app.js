"use strict";

// ===== State =====
let nfaData = null;          // VizNFA from /api/nfa
let activeNFANodes = new Set(); // cumulative set of NFA node ids materialized into DFA
let simulation = null;

// ===== D3 setup =====
const svg = d3.select("#graph svg");
const g = svg.append("g");

// Zoom / pan
const zoom = d3.zoom()
  .scaleExtent([0.1, 4])
  .on("zoom", e => g.attr("transform", e.transform));
svg.call(zoom);

// Arrow markers for directed edges
const defs = svg.append("defs");
function addMarker(id, color) {
  defs.append("marker")
    .attr("id", id)
    .attr("viewBox", "0 -5 10 10")
    .attr("refX", 18)
    .attr("refY", 0)
    .attr("markerWidth", 6)
    .attr("markerHeight", 6)
    .attr("orient", "auto")
    .append("path")
    .attr("d", "M0,-5L10,0L0,5")
    .attr("fill", color);
}
addMarker("arrow-byte",    "#991b1b");
addMarker("arrow-epsilon", "#475569");
addMarker("arrow-active",  "#d97706");

// ===== Init =====
async function init() {
  // Load NFA
  const nfaRes = await fetch("/api/nfa");
  nfaData = await nfaRes.json();

  // Load word chips
  const wordsRes = await fetch("/api/words");
  const words = await wordsRes.json();
  renderChips(words);

  renderGraph();
}

// ===== Graph rendering =====
function renderGraph() {
  g.selectAll("*").remove();

  if (!nfaData) return;

  const nodes = nfaData.nodes.map(n => ({ ...n }));
  const edges = nfaData.edges.map(e => ({ ...e }));

  // Build node-index lookup
  const nodeById = {};
  nodes.forEach(n => nodeById[n.id] = n);

  // Simulation
  const width  = document.getElementById("graph").clientWidth  || 800;
  const height = document.getElementById("graph").clientHeight || 600;

  simulation = d3.forceSimulation(nodes)
    .force("link", d3.forceLink(edges)
      .id(d => d.id)
      .distance(d => d.kind === "epsilon" ? 45 : 70)
      .strength(0.5))
    .force("charge", d3.forceManyBody().strength(-220))
    .force("center", d3.forceCenter(width / 2, height / 2))
    .force("collision", d3.forceCollide(18));

  // Edges
  const link = g.append("g").attr("class", "links")
    .selectAll("line")
    .data(edges)
    .join("line")
    .attr("class", d => `link ${d.kind}`)
    .attr("marker-end", d => `url(#arrow-${d.kind})`);

  // Edge labels
  const edgeLabel = g.append("g").attr("class", "edge-labels")
    .selectAll("text")
    .data(edges)
    .join("text")
    .attr("class", d => `edge-label ${d.kind}`)
    .text(d => d.label);

  // Nodes
  const node = g.append("g").attr("class", "nodes")
    .selectAll("g")
    .data(nodes)
    .join("g")
    .attr("class", d => {
      const classes = ["node"];
      if (d.accept) classes.push("accept");
      else if (d.epsilonOnly) classes.push("epsilon-only");
      else classes.push("normal");
      if (activeNFANodes.has(d.id)) classes.push("active");
      return classes.join(" ");
    })
    .call(d3.drag()
      .on("start", (event, d) => {
        if (!event.active) simulation.alphaTarget(0.3).restart();
        d.fx = d.x; d.fy = d.y;
      })
      .on("drag", (event, d) => { d.fx = event.x; d.fy = event.y; })
      .on("end", (event, d) => {
        if (!event.active) simulation.alphaTarget(0);
        d.fx = null; d.fy = null;
      }));

  // Accept nodes get a double ring; others get a single circle
  node.each(function(d) {
    const sel = d3.select(this);
    const r = d.epsilonOnly ? 7 : 10;
    if (d.accept) {
      sel.append("circle").attr("class", "outer").attr("r", r + 4);
      sel.append("circle").attr("class", "inner").attr("r", r);
    } else {
      sel.append("circle").attr("r", r);
    }
  });

  node.append("text")
    .attr("class", "node-label")
    .attr("dy", "0.35em")
    .attr("text-anchor", "middle")
    .text(d => d.id);

  // Simulation tick
  simulation.on("tick", () => {
    link
      .attr("x1", d => d.source.x)
      .attr("y1", d => d.source.y)
      .attr("x2", d => d.target.x)
      .attr("y2", d => d.target.y);

    edgeLabel
      .attr("x", d => (d.source.x + d.target.x) / 2)
      .attr("y", d => (d.source.y + d.target.y) / 2);

    node.attr("transform", d => `translate(${d.x},${d.y})`);
  });
}

// Update visual state after a feed (re-apply classes without rebuilding sim)
function updateActiveClasses() {
  if (!nfaData) return;

  g.selectAll(".node").attr("class", d => {
    const classes = ["node"];
    if (d.accept) classes.push("accept");
    else if (d.epsilonOnly) classes.push("epsilon-only");
    else classes.push("normal");
    if (activeNFANodes.has(d.id)) classes.push("active");
    return classes.join(" ");
  });

  // Highlight byte-edges between two highlighted nodes
  g.selectAll(".link").attr("class", d => {
    const srcId = (typeof d.source === "object") ? d.source.id : d.source;
    const tgtId = (typeof d.target === "object") ? d.target.id : d.target;
    if (d.kind === "byte" && activeNFANodes.has(srcId) && activeNFANodes.has(tgtId)) {
      return "link active";
    }
    return `link ${d.kind}`;
  }).attr("marker-end", d => {
    const srcId = (typeof d.source === "object") ? d.source.id : d.source;
    const tgtId = (typeof d.target === "object") ? d.target.id : d.target;
    if (d.kind === "byte" && activeNFANodes.has(srcId) && activeNFANodes.has(tgtId)) {
      return "url(#arrow-active)";
    }
    return `url(#arrow-${d.kind})`;
  });

  g.selectAll(".edge-label").attr("class", d => {
    const srcId = (typeof d.source === "object") ? d.source.id : d.source;
    const tgtId = (typeof d.target === "object") ? d.target.id : d.target;
    if (d.kind === "byte" && activeNFANodes.has(srcId) && activeNFANodes.has(tgtId)) {
      return "edge-label active";
    }
    return `edge-label ${d.kind}`;
  });
}

// ===== Feed =====
async function feedWord(word) {
  if (!word.trim()) return;

  const res = await fetch("/api/feed", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ word: word.trim() }),
  });

  if (!res.ok) {
    console.error("feed failed:", await res.text());
    return;
  }

  const feed = await res.json();

  // Accumulate highlighted NFA nodes
  (feed.dfaStates || []).forEach(ds => {
    (ds.nfaNodes || []).forEach(id => activeNFANodes.add(id));
  });
  updateActiveClasses();

  // Update panels
  renderMatches(feed.matches || [], word);
  renderStats(feed.stats || {});
  renderDFAStates(feed.dfaStates || []);
  markChipMatched(word, feed.matches || []);
}

// ===== Chip rendering =====
function renderChips(words) {
  const container = document.getElementById("chips");
  words.forEach(w => {
    const chip = document.createElement("span");
    chip.className = "chip";
    chip.textContent = w;
    chip.dataset.word = w;
    chip.addEventListener("click", () => {
      document.getElementById("word-input").value = w;
      feedWord(w);
    });
    container.appendChild(chip);
  });
}

function markChipMatched(word, matches) {
  if (matches.length === 0) return;
  const chips = document.querySelectorAll(".chip");
  chips.forEach(c => {
    if (c.dataset.word === word) c.classList.add("matched");
  });
}

// ===== Matches panel =====
function renderMatches(matches, word) {
  const el = document.getElementById("matches");
  el.innerHTML = "";
  if (matches.length === 0) {
    el.innerHTML = `<p class="no-match">No matches for "${word}"</p>`;
    return;
  }
  const ul = document.createElement("ul");
  ul.className = "match-list";
  matches.forEach(m => {
    const li = document.createElement("li");
    li.textContent = m;
    ul.appendChild(li);
  });
  el.appendChild(ul);
}

// ===== Stats panel =====
function renderStats(stats) {
  const fields = [
    ["states",    "DFA States"],
    ["creates",   "Creates"],
    ["hits",      "Cache Hits"],
    ["misses",    "Cache Misses"],
    ["cacheBytes","Cache Bytes"],
  ];
  const grid = document.getElementById("stats-grid");
  grid.innerHTML = "";
  fields.forEach(([key, label]) => {
    const box = document.createElement("div");
    box.className = "stat-box";
    box.innerHTML = `<div class="stat-label">${label}</div>
                     <div class="stat-value">${stats[key] ?? 0}</div>`;
    grid.appendChild(box);
  });
}

// ===== DFA States panel =====
function renderDFAStates(dfaStates) {
  const el = document.getElementById("dfa-states");
  el.innerHTML = "";
  if (dfaStates.length === 0) {
    el.innerHTML = `<p class="no-match">No DFA states yet.</p>`;
    return;
  }
  const list = document.createElement("div");
  list.className = "dfa-list";
  dfaStates.forEach(ds => {
    const item = document.createElement("div");
    item.className = "dfa-item" + (ds.start ? " start" : "");
    const trans = (ds.trans || []).map(t => `${t.label}→${t.to}`).join(", ");
    item.innerHTML =
      `<strong>S${ds.id}${ds.start ? " ★" : ""}</strong>` +
      (trans ? `  <span style="color:#475569">${trans}</span>` : "") +
      `<div class="nfa-nodes">NFA: [${(ds.nfaNodes || []).join(", ")}]</div>`;
    list.appendChild(item);
  });
  el.appendChild(list);
}

// ===== Event wiring =====
document.getElementById("feed-btn").addEventListener("click", () => {
  feedWord(document.getElementById("word-input").value);
});
document.getElementById("word-input").addEventListener("keydown", e => {
  if (e.key === "Enter") feedWord(e.target.value);
});

window.addEventListener("resize", () => {
  if (simulation) {
    const width  = document.getElementById("graph").clientWidth;
    const height = document.getElementById("graph").clientHeight;
    simulation.force("center", d3.forceCenter(width / 2, height / 2));
    simulation.alpha(0.3).restart();
  }
});

init();
