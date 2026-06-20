// app.js — Three.js static 3D point-cloud NFA visualizer
// ES module; import map in index.html resolves bare specifiers.

import * as THREE from "three";
import { OrbitControls } from "three/addons/controls/OrbitControls.js";
import {
  forceSimulation,
  forceManyBody,
  forceLink,
  forceCollide,
} from "d3-force-3d";

// ===== State =====
let nfaData = null;
let activeNFANodes = new Set(); // cumulative set of NFA node ids materialized into DFA

// Three.js objects (set after buildScene)
let renderer, camera, controls, scene;
let pointsObj = null;       // THREE.Points (nodes)
let byteLines = null;       // THREE.LineSegments (byte edges)
let epsLines = null;        // THREE.LineSegments (epsilon edges)
let nodePositions = [];     // [{x,y,z}] indexed by position in nfaData.nodes

// Node-id to array-index map
let nodeIndexById = {};

// Colors (as THREE.Color components)
const C_NORMAL  = new THREE.Color("#64748b"); // slate
const C_ACCEPT  = new THREE.Color("#2dd4bf"); // teal
const C_EPS     = new THREE.Color("#334155"); // dim
const C_ACTIVE  = new THREE.Color("#d97706"); // gold

// ===== Init =====
async function init() {
  const [nfaRes, wordsRes] = await Promise.all([
    fetch("/api/nfa"),
    fetch("/api/words"),
  ]);
  nfaData = await nfaRes.json();
  const words = await wordsRes.json();

  renderChips(words);
  buildScene();
}

// ===== 3D scene =====
function buildScene() {
  const container = document.getElementById("graph");
  const w = container.clientWidth  || 800;
  const h = container.clientHeight || 600;

  // Renderer
  renderer = new THREE.WebGLRenderer({ antialias: true });
  renderer.setPixelRatio(window.devicePixelRatio);
  renderer.setSize(w, h);
  renderer.setClearColor(0x0f1117);
  container.appendChild(renderer.domElement);

  // Scene + camera
  scene = new THREE.Scene();
  camera = new THREE.PerspectiveCamera(60, w / h, 0.1, 10000);
  camera.position.set(0, 0, 600);

  // OrbitControls — render on demand only; damping disabled (needs rAF loop)
  controls = new OrbitControls(camera, renderer.domElement);
  controls.enableDamping = false;
  controls.addEventListener("change", renderFrame);

  // Compute layout once, freeze it
  computeLayout();

  // Build geometry from frozen positions
  buildPointCloud();
  buildEdgeLines();

  // Frame the camera on the cloud so it fills the view.
  fitCamera();

  // One initial render
  renderFrame();

  // Resize handling
  window.addEventListener("resize", onResize);

  // ε-edge toggle
  document.getElementById("show-eps").addEventListener("change", e => {
    if (epsLines) epsLines.visible = e.target.checked;
    renderFrame();
  });
}

// ===== Layout: compute once, never animate =====
function computeLayout() {
  if (!nfaData || !nfaData.nodes || !nfaData.nodes.length) return;

  const N = nfaData.nodes.length;
  // Seed every node at a DISTINCT, finite position on a Fibonacci sphere so the
  // force sim never starts from coincident points (a common NaN source).
  const R0 = 220;
  const golden = Math.PI * (1 + Math.sqrt(5));
  const nodes = nfaData.nodes.map((n, i) => {
    const phi = Math.acos(1 - 2 * (i + 0.5) / N);
    const theta = golden * i;
    return {
      id: n.id,
      x: R0 * Math.sin(phi) * Math.cos(theta),
      y: R0 * Math.sin(phi) * Math.sin(theta),
      z: R0 * Math.cos(phi),
    };
  });

  // Lay out using BYTE edges only. The epsilon edges (splice/spinner plumbing,
  // hidden by default) are a dense interlinked mesh that collapses the layout
  // into a clump; the byte-transition graph is trie-like and spreads cleanly.
  const links = (nfaData.edges || [])
    .filter(e => e.kind === "byte")
    .map(e => ({ source: e.from, target: e.to }));

  // Pass dimensions to the constructor so forceManyBody/forceLink initialize in
  // 3D (chaining .numDimensions() after construction does not reliably reach
  // the forces, which collapses the layout to a plane). Strong charge inflates
  // the dense, near-planar link graph into a real 3D cloud; distanceMax bounds
  // it so positions can't blow up to Infinity/NaN over many ticks.
  const sim = forceSimulation(nodes, 3)
    .force("charge", forceManyBody().strength(-110).distanceMax(1600))
    .force("link", forceLink(links).id(d => d.id).distance(70).strength(0.12))
    .force("collide", forceCollide(8))
    .stop();
  for (let i = 0; i < 160; i++) sim.tick();

  // Sanitize any non-finite coordinate, then recenter on the finite centroid.
  let cx = 0, cy = 0, cz = 0, m = 0;
  for (const n of nodes) {
    if (Number.isFinite(n.x) && Number.isFinite(n.y) && Number.isFinite(n.z)) {
      cx += n.x; cy += n.y; cz += n.z; m++;
    }
  }
  if (m > 0) { cx /= m; cy /= m; cz /= m; }

  nodeIndexById = {};
  nodePositions = nodes.map((n, idx) => {
    nodeIndexById[n.id] = idx;
    const x = Number.isFinite(n.x) ? n.x - cx : 0;
    const y = Number.isFinite(n.y) ? n.y - cy : 0;
    const z = Number.isFinite(n.z) ? n.z - cz : 0;
    return { x, y, z };
  });
}

// ===== Point cloud geometry =====
function buildPointCloud() {
  const n = nfaData.nodes.length;
  const positions = new Float32Array(n * 3);
  const colors    = new Float32Array(n * 3);
  const sizes     = new Float32Array(n);

  nfaData.nodes.forEach((node, i) => {
    const p = nodePositions[i];
    positions[i * 3]     = p.x;
    positions[i * 3 + 1] = p.y;
    positions[i * 3 + 2] = p.z;

    let c;
    if (node.accept) {
      c = C_ACCEPT;
      sizes[i] = 10;
    } else if (node.epsilonOnly) {
      c = C_EPS;
      sizes[i] = 5;
    } else {
      c = C_NORMAL;
      sizes[i] = 8;
    }
    colors[i * 3]     = c.r;
    colors[i * 3 + 1] = c.g;
    colors[i * 3 + 2] = c.b;
  });

  const geo = new THREE.BufferGeometry();
  geo.setAttribute("position", new THREE.BufferAttribute(positions, 3));
  geo.setAttribute("color",    new THREE.BufferAttribute(colors,    3));
  geo.setAttribute("size",     new THREE.BufferAttribute(sizes,     1));

  const mat = new THREE.PointsMaterial({
    vertexColors: true,
    sizeAttenuation: true,
    size: 8,
  });

  pointsObj = new THREE.Points(geo, mat);
  scene.add(pointsObj);
}

// ===== Edge line geometry =====
function buildEdgeLines() {
  if (!nfaData.edges || !nfaData.edges.length) return;

  const byteEdges    = nfaData.edges.filter(e => e.kind === "byte");
  const epsilonEdges = nfaData.edges.filter(e => e.kind === "epsilon");

  byteLines = makeLineSegments(byteEdges, "#991b1b", 0.7);
  epsLines  = makeLineSegments(epsilonEdges, "#475569", 0.3);
  epsLines.visible = false; // hidden by default; toggle with checkbox

  scene.add(byteLines);
  scene.add(epsLines);
}

function makeLineSegments(edges, colorHex, opacity) {
  const positions = new Float32Array(edges.length * 6); // 2 verts × 3 floats each
  const colors    = new Float32Array(edges.length * 6);

  const c = new THREE.Color(colorHex);
  edges.forEach((e, i) => {
    const fi = nodeIndexById[e.from];
    const ti = nodeIndexById[e.to];
    if (fi === undefined || ti === undefined) return;

    const fp = nodePositions[fi];
    const tp = nodePositions[ti];

    positions[i * 6]     = fp.x;
    positions[i * 6 + 1] = fp.y;
    positions[i * 6 + 2] = fp.z;
    positions[i * 6 + 3] = tp.x;
    positions[i * 6 + 4] = tp.y;
    positions[i * 6 + 5] = tp.z;

    for (let v = 0; v < 2; v++) {
      colors[(i * 2 + v) * 3]     = c.r;
      colors[(i * 2 + v) * 3 + 1] = c.g;
      colors[(i * 2 + v) * 3 + 2] = c.b;
    }
  });

  const geo = new THREE.BufferGeometry();
  geo.setAttribute("position", new THREE.BufferAttribute(positions, 3));
  geo.setAttribute("color",    new THREE.BufferAttribute(colors,    3));

  const mat = new THREE.LineBasicMaterial({
    vertexColors: true,
    transparent: opacity < 1,
    opacity,
  });

  return new THREE.LineSegments(geo, mat);
}

// Frame the camera to fit the whole point cloud.
function fitCamera() {
  if (!pointsObj) return;
  pointsObj.geometry.computeBoundingSphere();
  const s = pointsObj.geometry.boundingSphere;
  if (!s || !Number.isFinite(s.radius) || s.radius === 0) return;
  controls.target.copy(s.center);
  const fov = (camera.fov * Math.PI) / 180;
  const dist = (s.radius / Math.sin(fov / 2)) * 1.25;
  // Oblique angle so the 3D depth reads at a glance (not a face-on plane).
  const dir = new THREE.Vector3(0.8, 0.5, 1).normalize();
  camera.position.copy(s.center).addScaledVector(dir, dist);
  camera.near = Math.max(0.1, dist / 1000);
  camera.far = dist * 1000;
  camera.updateProjectionMatrix();
  controls.update();
}

// ===== On-demand render (no animation loop) =====
function renderFrame() {
  renderer.render(scene, camera);
}

// ===== Update colors after feed (no relayout) =====
function updatePointColors() {
  if (!pointsObj) return;

  const colorAttr = pointsObj.geometry.getAttribute("color");

  nfaData.nodes.forEach((node, i) => {
    let c;
    if (activeNFANodes.has(node.id)) {
      c = C_ACTIVE;
    } else if (node.accept) {
      c = C_ACCEPT;
    } else if (node.epsilonOnly) {
      c = C_EPS;
    } else {
      c = C_NORMAL;
    }
    colorAttr.setXYZ(i, c.r, c.g, c.b);
  });
  colorAttr.needsUpdate = true;
}

function updateByteEdgeColors() {
  if (!byteLines) return;

  const colorAttr = byteLines.geometry.getAttribute("color");
  const byteEdges = nfaData.edges.filter(e => e.kind === "byte");

  byteEdges.forEach((e, i) => {
    const bothActive = activeNFANodes.has(e.from) && activeNFANodes.has(e.to);
    const c = bothActive ? C_ACTIVE : new THREE.Color("#991b1b");
    colorAttr.setXYZ(i * 2,     c.r, c.g, c.b);
    colorAttr.setXYZ(i * 2 + 1, c.r, c.g, c.b);
  });
  colorAttr.needsUpdate = true;
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

  // Recolor point cloud and byte edges, then re-render
  updatePointColors();
  updateByteEdgeColors();
  renderFrame();

  // Update panels
  renderMatches(feed.matches || [], word);
  renderStats(feed.stats || {});
  renderDFAStates(feed.dfaStates || []);
  markChipMatched(word, feed.matches || []);
}

// ===== Resize =====
function onResize() {
  const container = document.getElementById("graph");
  const w = container.clientWidth;
  const h = container.clientHeight;
  camera.aspect = w / h;
  camera.updateProjectionMatrix();
  renderer.setSize(w, h);
  renderFrame();
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
  document.querySelectorAll(".chip").forEach(c => {
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
    ["states",     "DFA States"],
    ["creates",    "Creates"],
    ["hits",       "Cache Hits"],
    ["misses",     "Cache Misses"],
    ["cacheBytes", "Cache Bytes"],
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

init();
