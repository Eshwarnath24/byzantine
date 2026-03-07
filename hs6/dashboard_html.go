package main

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>HotStuff BFT — Node Dashboard</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@300;400;500;600;700&family=Syne:wght@400;500;600;700;800&display=swap" rel="stylesheet">
<style>
  :root {
    --bg:        #080c10;
    --surface:   #0d1117;
    --surface2:  #111820;
    --surface3:  #161e28;
    --border:    #1e2d3d;
    --border2:   #243345;
    --accent:    #00d4ff;
    --accent2:   #0090cc;
    --gold:      #f5a623;
    --gold2:     #c47d0e;
    --green:     #00e676;
    --green2:    #00b85c;
    --red:       #ff4444;
    --red2:      #cc2222;
    --purple:    #b16fff;
    --purple2:   #7c3aed;
    --dim:       #3d5068;
    --text:      #c8d8e8;
    --text2:     #7a94ae;
    --text3:     #3d5068;
    --font-mono: 'JetBrains Mono', monospace;
    --font-ui:   'Syne', sans-serif;
    --radius:    6px;
    --radius2:   10px;
  }

  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

  body {
    background: var(--bg);
    color: var(--text);
    font-family: var(--font-mono);
    font-size: 13px;
    min-height: 100vh;
    overflow-x: hidden;
  }

  /* ── scanline overlay ── */
  body::before {
    content: '';
    position: fixed; inset: 0;
    background: repeating-linear-gradient(
      0deg,
      transparent,
      transparent 2px,
      rgba(0,0,0,.15) 2px,
      rgba(0,0,0,.15) 4px
    );
    pointer-events: none;
    z-index: 9999;
  }

  /* ── HEADER ── */
  .header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 18px 28px;
    border-bottom: 1px solid var(--border);
    background: linear-gradient(90deg, rgba(0,212,255,.04) 0%, transparent 60%);
    position: sticky; top: 0; z-index: 100;
    backdrop-filter: blur(12px);
  }
  .header-left { display: flex; align-items: center; gap: 16px; }
  .logo { font-family: var(--font-ui); font-size: 20px; font-weight: 800; letter-spacing: -.5px; color: #fff; }
  .logo span { color: var(--accent); }
  .node-badge {
    background: var(--surface2);
    border: 1px solid var(--border);
    border-radius: 20px;
    padding: 4px 12px;
    font-size: 11px;
    color: var(--text2);
    display: flex; align-items: center; gap: 6px;
  }
  .node-badge .nid { color: var(--gold); font-weight: 700; }
  .header-right { display: flex; align-items: center; gap: 12px; }
  .conn-dot {
    width: 8px; height: 8px; border-radius: 50%;
    background: var(--dim);
    transition: background .3s;
    box-shadow: 0 0 0 3px rgba(0,0,0,.4);
  }
  .conn-dot.live { background: var(--green); box-shadow: 0 0 10px var(--green2); animation: pulse 2s infinite; }
  .conn-dot.dead { background: var(--red); }
  .conn-label { font-size: 11px; color: var(--text2); }
  @keyframes pulse { 0%,100%{opacity:1} 50%{opacity:.5} }

  /* ── LAYOUT ── */
  .main { padding: 20px 28px; display: grid; grid-template-columns: 1fr 1fr; gap: 16px; max-width: 1400px; margin: 0 auto; }
  .full-width { grid-column: 1 / -1; }

  /* ── CARDS ── */
  .card {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius2);
    overflow: hidden;
    transition: border-color .2s;
  }
  .card:hover { border-color: var(--border2); }
  .card-header {
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
    display: flex; align-items: center; justify-content: space-between;
    background: var(--surface2);
  }
  .card-title { font-family: var(--font-ui); font-size: 10px; font-weight: 600; letter-spacing: 1.5px; text-transform: uppercase; color: var(--text3); }
  .card-body { padding: 16px; }

  /* ── STATUS BAR ── */
  .status-bar {
    display: flex; align-items: center; gap: 0;
    background: var(--surface2);
    border: 1px solid var(--border);
    border-radius: var(--radius2);
    overflow: hidden;
  }
  .stat {
    flex: 1; padding: 12px 16px;
    border-right: 1px solid var(--border);
    text-align: center;
  }
  .stat:last-child { border-right: none; }
  .stat-label { font-size: 9px; letter-spacing: 1.2px; text-transform: uppercase; color: var(--text3); margin-bottom: 4px; }
  .stat-value { font-family: var(--font-ui); font-size: 22px; font-weight: 700; color: #fff; }
  .stat-value.accent { color: var(--accent); }
  .stat-value.gold   { color: var(--gold); }
  .stat-value.green  { color: var(--green); }
  .stat-value.purple { color: var(--purple); }

  /* ── ROLE BADGE ── */
  .role-badge {
    display: inline-flex; align-items: center; gap: 6px;
    padding: 5px 14px; border-radius: 20px;
    font-size: 10px; font-weight: 700; letter-spacing: 1px;
    text-transform: uppercase;
  }
  .role-badge.leader { background: rgba(245,166,35,.12); border: 1px solid rgba(245,166,35,.3); color: var(--gold); }
  .role-badge.replica { background: rgba(0,212,255,.08); border: 1px solid rgba(0,212,255,.2); color: var(--accent); }
  .role-badge.malicious { background: rgba(255,68,68,.12); border: 1px solid rgba(255,68,68,.3); color: var(--red); }

  /* ── CLUSTER NODES ── */
  .cluster-grid { display: flex; flex-wrap: wrap; gap: 10px; padding: 16px; }
  .node-card {
    flex: 1; min-width: 130px;
    background: var(--surface2);
    border: 1px solid var(--border);
    border-radius: var(--radius2);
    padding: 14px;
    text-align: center;
    transition: all .25s;
    position: relative;
    overflow: hidden;
  }
  .node-card::before {
    content: '';
    position: absolute; top: 0; left: 0; right: 0; height: 2px;
    background: var(--border); transition: background .25s;
  }
  .node-card.is-leader { border-color: rgba(245,166,35,.4); background: rgba(245,166,35,.05); }
  .node-card.is-leader::before { background: var(--gold); }
  .node-card.is-you { border-color: rgba(0,212,255,.3); }
  .node-card.is-you::before { background: var(--accent); }
  .node-card.malicious { border-color: rgba(255,68,68,.4); background: rgba(255,68,68,.04); }
  .node-card.malicious::before { background: var(--red); }
  .node-star { color: var(--gold); font-size: 16px; margin-bottom: 4px; display: block; opacity: 0; transition: opacity .2s; }
  .node-card.is-leader .node-star { opacity: 1; }
  .node-name { font-family: var(--font-ui); font-size: 16px; font-weight: 700; color: #fff; }
  .node-name .you-tag {
    font-size: 9px; font-weight: 600; letter-spacing: 1px; vertical-align: middle;
    background: rgba(0,212,255,.15); border: 1px solid rgba(0,212,255,.3);
    color: var(--accent); padding: 1px 5px; border-radius: 3px; margin-left: 5px;
  }
  .node-sub { font-size: 10px; color: var(--text3); margin-top: 3px; }
  .node-role { font-size: 10px; color: var(--text2); margin-top: 6px; font-weight: 500; }
  .node-card.is-leader .node-role { color: var(--gold); }

  /* ── BLOCK TREE ── */
  .tree-area {
    font-family: var(--font-mono);
    font-size: 12px;
    min-height: 90px;
    padding: 16px;
    overflow-x: auto;
  }
  .tree-empty { color: var(--text3); font-style: italic; }
  .chain {
    display: flex; flex-wrap: wrap; align-items: center; gap: 0;
  }
  .chain-arrow { color: var(--text3); padding: 0 6px; font-size: 16px; }
  .block-chip {
    display: inline-flex; flex-direction: column; align-items: center;
    background: var(--surface2); border: 1px solid var(--border);
    border-radius: var(--radius); padding: 8px 12px; cursor: default;
    transition: all .2s; position: relative; min-width: 72px;
  }
  .block-chip:hover { border-color: var(--border2); transform: translateY(-2px); }
  .block-chip.committed { border-color: rgba(177,111,255,.4); background: rgba(177,111,255,.07); }
  .block-chip.committed::after {
    content: '✓'; position: absolute; top: -7px; right: -7px;
    background: var(--purple2); color: #fff; font-size: 9px;
    width: 15px; height: 15px; border-radius: 50%;
    display: flex; align-items: center; justify-content: center;
    font-family: var(--font-mono);
  }
  .block-chip.qcd { border-color: rgba(0,212,255,.35); background: rgba(0,212,255,.06); }
  .block-chip.pending { border-color: rgba(245,166,35,.35); background: rgba(245,166,35,.05); animation: glow-gold 1.5s infinite; }
  @keyframes glow-gold { 0%,100%{box-shadow:0 0 0 rgba(245,166,35,0)} 50%{box-shadow:0 0 12px rgba(245,166,35,.25)} }
  .block-chip.proposal { border-color: rgba(0,230,118,.35); background: rgba(0,230,118,.05); animation: glow-green 1.5s infinite; }
  @keyframes glow-green { 0%,100%{box-shadow:0 0 0 rgba(0,230,118,0)} 50%{box-shadow:0 0 12px rgba(0,230,118,.25)} }
  .block-id { font-size: 13px; font-weight: 700; color: #fff; }
  .block-id.committed-color { color: var(--purple); }
  .block-id.qcd-color { color: var(--accent); }
  .block-id.pending-color { color: var(--gold); }
  .block-id.proposal-color { color: var(--green); }
  .block-view { font-size: 9px; color: var(--text3); margin-top: 2px; }
  .block-data { font-size: 9px; color: var(--text2); margin-top: 2px; max-width: 80px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .lock-tag, .hqc-tag {
    font-size: 8px; padding: 1px 4px; border-radius: 3px; margin-top: 3px;
    font-weight: 600; letter-spacing: .5px;
  }
  .lock-tag { background: rgba(245,166,35,.15); color: var(--gold); border: 1px solid rgba(245,166,35,.25); }
  .hqc-tag  { background: rgba(0,212,255,.12);  color: var(--accent); border: 1px solid rgba(0,212,255,.2); }
  .legend { display: flex; gap: 16px; padding: 10px 16px; border-top: 1px solid var(--border); flex-wrap: wrap; }
  .legend-item { display: flex; align-items: center; gap: 5px; font-size: 10px; color: var(--text3); }
  .legend-dot { width: 8px; height: 8px; border-radius: 2px; }
  .legend-dot.committed { background: var(--purple); }
  .legend-dot.qcd       { background: var(--accent); }
  .legend-dot.pending   { background: var(--gold); }
  .legend-dot.proposal  { background: var(--green); }

  /* ── VOTE PROMPT ── */
  .vote-panel {
    display: none;
    background: var(--surface);
    border: 1px solid rgba(0,230,118,.3);
    border-radius: var(--radius2);
    overflow: hidden;
    animation: slide-in .3s ease;
  }
  .vote-panel.show { display: block; }
  @keyframes slide-in { from{opacity:0;transform:translateY(-8px)} to{opacity:1;transform:translateY(0)} }
  .vote-header {
    background: rgba(0,230,118,.07);
    border-bottom: 1px solid rgba(0,230,118,.2);
    padding: 10px 16px;
    display: flex; align-items: center; gap: 8px;
  }
  .vote-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--green); animation: pulse 1s infinite; }
  .vote-title { font-family: var(--font-ui); font-size: 11px; font-weight: 700; color: var(--green); letter-spacing: 1px; text-transform: uppercase; }
  .vote-body { padding: 16px; }
  .vote-block-info { display: grid; grid-template-columns: repeat(2,1fr); gap: 8px; margin-bottom: 14px; }
  .vbi { background: var(--surface2); border: 1px solid var(--border); border-radius: var(--radius); padding: 8px 10px; }
  .vbi-label { font-size: 9px; color: var(--text3); letter-spacing: 1px; text-transform: uppercase; margin-bottom: 3px; }
  .vbi-value { font-size: 13px; font-weight: 600; color: var(--text); }
  .vote-data { background: var(--surface2); border: 1px solid var(--border); border-radius: var(--radius); padding: 10px 12px; margin-bottom: 14px; }
  .vote-data-label { font-size: 9px; color: var(--text3); letter-spacing: 1px; text-transform: uppercase; margin-bottom: 4px; }
  .vote-data-value { font-size: 13px; color: var(--green); font-weight: 600; word-break: break-all; }
  .vote-btns { display: grid; grid-template-columns: 1fr 1fr; gap: 10px; }
  .btn {
    padding: 11px; border: none; border-radius: var(--radius); cursor: pointer;
    font-family: var(--font-mono); font-size: 13px; font-weight: 700;
    letter-spacing: .5px; transition: all .15s; text-transform: uppercase;
  }
  .btn-yes { background: rgba(0,230,118,.12); border: 1px solid rgba(0,230,118,.35); color: var(--green); }
  .btn-yes:hover { background: rgba(0,230,118,.22); box-shadow: 0 0 15px rgba(0,230,118,.2); }
  .btn-no  { background: rgba(255,68,68,.1);  border: 1px solid rgba(255,68,68,.3);   color: var(--red); }
  .btn-no:hover  { background: rgba(255,68,68,.2);  box-shadow: 0 0 15px rgba(255,68,68,.2); }
  .btn:disabled { opacity: .4; cursor: not-allowed; }
  .btn:active:not(:disabled) { transform: scale(.97); }

  /* ── PROPOSE PANEL ── */
  .propose-panel {
    display: none;
    background: var(--surface);
    border: 1px solid rgba(245,166,35,.3);
    border-radius: var(--radius2);
    overflow: hidden;
    animation: slide-in .3s ease;
  }
  .propose-panel.show { display: block; }
  .propose-header {
    background: rgba(245,166,35,.07);
    border-bottom: 1px solid rgba(245,166,35,.2);
    padding: 10px 16px;
    display: flex; align-items: center; gap: 8px;
  }
  .propose-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--gold); animation: pulse 1s infinite; }
  .propose-title { font-family: var(--font-ui); font-size: 11px; font-weight: 700; color: var(--gold); letter-spacing: 1px; text-transform: uppercase; }
  .propose-body { padding: 16px; }
  .propose-input-wrap { display: flex; gap: 8px; }
  .propose-input {
    flex: 1; background: var(--surface2); border: 1px solid var(--border);
    border-radius: var(--radius); color: var(--text);
    font-family: var(--font-mono); font-size: 13px;
    padding: 10px 12px; outline: none; transition: border-color .2s;
  }
  .propose-input:focus { border-color: var(--gold); }
  .propose-input::placeholder { color: var(--text3); }
  .btn-propose { background: rgba(245,166,35,.12); border: 1px solid rgba(245,166,35,.35); color: var(--gold); min-width: 90px; }
  .btn-propose:hover { background: rgba(245,166,35,.22); box-shadow: 0 0 15px rgba(245,166,35,.2); }
  .propose-hint { font-size: 10px; color: var(--text3); margin-top: 8px; }

  /* ── EVENT LOG ── */
  .log-header { display: flex; align-items: center; justify-content: space-between; }
  .log-clear { background: none; border: 1px solid var(--border); border-radius: var(--radius); color: var(--text3); font-family: var(--font-mono); font-size: 10px; padding: 3px 8px; cursor: pointer; transition: all .15s; }
  .log-clear:hover { border-color: var(--border2); color: var(--text2); }
  .log-area { height: 220px; overflow-y: auto; padding: 10px 16px; scroll-behavior: smooth; }
  .log-area::-webkit-scrollbar { width: 4px; }
  .log-area::-webkit-scrollbar-track { background: transparent; }
  .log-area::-webkit-scrollbar-thumb { background: var(--border); border-radius: 2px; }
  .log-entry {
    display: grid; grid-template-columns: 70px 52px 1fr; gap: 8px;
    padding: 4px 0; border-bottom: 1px solid rgba(255,255,255,.025);
    animation: faderow .25s ease;
  }
  @keyframes faderow { from{opacity:0;transform:translateX(-6px)} to{opacity:1;transform:none} }
  .log-time { color: var(--text3); font-size: 11px; }
  .log-level {
    font-size: 10px; font-weight: 700; letter-spacing: .5px;
    padding: 1px 5px; border-radius: 3px; text-align: center; align-self: start;
  }
  .ll-SYS    { background: rgba(122,148,174,.12); color: var(--text2); }
  .ll-INFO   { background: rgba(0,212,255,.1);    color: var(--accent); }
  .ll-WARN   { background: rgba(245,166,35,.12);  color: var(--gold); }
  .ll-VOTE   { background: rgba(0,230,118,.1);    color: var(--green); }
  .ll-QC     { background: rgba(0,212,255,.12);   color: var(--accent); }
  .ll-COMMIT { background: rgba(177,111,255,.12); color: var(--purple); }
  .ll-NET    { background: rgba(0,212,255,.08);   color: var(--accent2); }
  .ll-ERR    { background: rgba(255,68,68,.12);   color: var(--red); }
  .ll-LEADER { background: rgba(245,166,35,.15);  color: var(--gold); }
  .log-msg { color: var(--text); font-size: 11px; line-height: 1.5; word-break: break-all; }

  /* ── TOAST ── */
  .toast-wrap { position: fixed; bottom: 24px; right: 24px; display: flex; flex-direction: column-reverse; gap: 8px; z-index: 9998; }
  .toast {
    background: var(--surface2); border: 1px solid var(--border2);
    border-radius: var(--radius); padding: 10px 16px; font-size: 12px;
    max-width: 320px; animation: toast-in .3s ease;
    display: flex; align-items: center; gap: 8px;
  }
  @keyframes toast-in { from{opacity:0;transform:translateY(10px)} to{opacity:1;transform:none} }
  .toast.ok     { border-left: 3px solid var(--green); }
  .toast.warn   { border-left: 3px solid var(--gold); }
  .toast.commit { border-left: 3px solid var(--purple); }
  .toast.err    { border-left: 3px solid var(--red); }
</style>
</head>
<body>

<!-- HEADER -->
<header class="header">
  <div class="header-left">
    <div class="logo">Hot<span>Stuff</span> BFT</div>
    <div class="node-badge">Node <span class="nid" id="hdr-node">—</span> &nbsp;·&nbsp; Port <span id="hdr-port">—</span></div>
  </div>
  <div class="header-right">
    <span id="role-badge" class="role-badge replica">REPLICA</span>
    <div class="conn-dot" id="conn-dot"></div>
    <span class="conn-label" id="conn-label">Connecting…</span>
  </div>
</header>

<div class="main">

  <!-- STATUS BAR -->
  <div class="full-width">
    <div class="status-bar">
      <div class="stat"><div class="stat-label">View</div><div class="stat-value accent" id="s-view">—</div></div>
      <div class="stat"><div class="stat-label">Leader</div><div class="stat-value gold" id="s-leader">—</div></div>
      <div class="stat"><div class="stat-label">Quorum</div><div class="stat-value" id="s-quorum">—</div></div>
      <div class="stat"><div class="stat-label">Committed</div><div class="stat-value green" id="s-committed">0</div></div>
      <div class="stat"><div class="stat-label">Accepted</div><div class="stat-value purple" id="s-accepted">0</div></div>
    </div>
  </div>

  <!-- CLUSTER -->
  <div class="card">
    <div class="card-header"><span class="card-title">Cluster Nodes</span></div>
    <div class="cluster-grid" id="cluster-grid">
      <div style="color:var(--text3);font-size:11px;padding:8px">Waiting for peers…</div>
    </div>
  </div>

  <!-- ACTION PANEL (vote / propose) -->
  <div>
    <!-- Vote prompt -->
    <div class="vote-panel" id="vote-panel">
      <div class="vote-header">
        <div class="vote-dot"></div>
        <span class="vote-title">⚡ Proposal Received — Vote Required</span>
      </div>
      <div class="vote-body">
        <div class="vote-block-info">
          <div class="vbi"><div class="vbi-label">Block ID</div><div class="vbi-value" id="vp-id">—</div></div>
          <div class="vbi"><div class="vbi-label">View</div><div class="vbi-value" id="vp-view">—</div></div>
          <div class="vbi"><div class="vbi-label">Parent Block</div><div class="vbi-value" id="vp-parent">—</div></div>
          <div class="vbi"><div class="vbi-label">Leader</div><div class="vbi-value" id="vp-leader">—</div></div>
        </div>
        <div class="vote-data">
          <div class="vote-data-label">Block Data</div>
          <div class="vote-data-value" id="vp-data">—</div>
        </div>
        <div class="vote-btns">
          <button class="btn btn-yes" id="btn-yes" onclick="castVote('y')">✔ Accept</button>
          <button class="btn btn-no"  id="btn-no"  onclick="castVote('n')">✖ Reject</button>
        </div>
      </div>
    </div>

    <!-- Propose panel (leader) -->
    <div class="propose-panel" id="propose-panel">
      <div class="propose-header">
        <div class="propose-dot"></div>
        <span class="propose-title">★ Leader — Propose Block</span>
      </div>
      <div class="propose-body">
        <div class="propose-input-wrap">
          <input class="propose-input" id="propose-input" placeholder="Enter block data (leave empty to auto-generate)" type="text"
            onkeydown="if(event.key==='Enter') submitPropose()">
          <button class="btn btn-propose" onclick="submitPropose()">Propose</button>
        </div>
        <div class="propose-hint">This data will be proposed to all replicas for voting.</div>
      </div>
    </div>

    <!-- Idle state -->
    <div class="card" id="idle-card">
      <div class="card-header"><span class="card-title">Actions</span></div>
      <div class="card-body" style="color:var(--text3);font-size:11px;">
        Waiting for your turn to propose or vote…
      </div>
    </div>
  </div>

  <!-- BLOCK TREE -->
  <div class="card full-width">
    <div class="card-header"><span class="card-title">Block Chain</span></div>
    <div class="tree-area" id="tree-area">
      <span class="tree-empty">Genesis only — no blocks proposed yet</span>
    </div>
    <div class="legend">
      <div class="legend-item"><div class="legend-dot committed"></div> committed</div>
      <div class="legend-item"><div class="legend-dot qcd"></div> QC'd (accepted)</div>
      <div class="legend-item"><div class="legend-dot pending"></div> pending votes (you are leader)</div>
      <div class="legend-item"><div class="legend-dot proposal"></div> proposal (awaiting your vote)</div>
      <span style="margin-left:auto;color:var(--text3);font-size:10px;">[L] = locked &nbsp; [H] = highQC</span>
    </div>
  </div>

  <!-- EVENT LOG -->
  <div class="card full-width">
    <div class="card-header">
      <span class="card-title">Event Log</span>
      <button class="log-clear" onclick="clearLog()">clear</button>
    </div>
    <div class="log-area" id="log-area"></div>
  </div>

</div><!-- /main -->

<!-- TOASTS -->
<div class="toast-wrap" id="toasts"></div>

<script>
// ── State ────────────────────────────────────────────────────────────────────
const port = parseInt(location.port) || 8001;
const wsURL = 'ws://' + location.hostname + ':' + port + '/ws';
let ws, state = null, pendingVote = null, pendingPropose = false, reconnTimer = null;

// ── WebSocket ────────────────────────────────────────────────────────────────
function connect() {
  ws = new WebSocket(wsURL);
  ws.onopen = () => {
    setConn(true);
    if (reconnTimer) { clearInterval(reconnTimer); reconnTimer = null; }
  };
  ws.onclose = ws.onerror = () => {
    setConn(false);
    if (!reconnTimer) reconnTimer = setInterval(connect, 3000);
  };
  ws.onmessage = e => {
    try { handleMsg(JSON.parse(e.data)); } catch {}
  };
}

function setConn(live) {
  document.getElementById('conn-dot').className = 'conn-dot ' + (live ? 'live' : 'dead');
  document.getElementById('conn-label').textContent = live ? 'Connected' : 'Reconnecting…';
}

// ── Message handler ───────────────────────────────────────────────────────────
function handleMsg(msg) {
  if (msg.type === 'state')       return applyState(msg);
  if (msg.type === 'event')       return appendLog(msg);
  if (msg.type === 'vote_prompt') return showVotePrompt(msg);
}

// ── Apply full state snapshot ────────────────────────────────────────────────
function applyState(s) {
  state = s;

  // Header
  document.getElementById('hdr-node').textContent = s.node_id;
  document.getElementById('hdr-port').textContent = 7000 + s.node_id;

  // Role badge
  const rb = document.getElementById('role-badge');
  if (s.malicious) {
    rb.className = 'role-badge malicious'; rb.textContent = '☠ BYZANTINE';
  } else if (s.is_leader) {
    rb.className = 'role-badge leader'; rb.textContent = '★ LEADER';
  } else {
    rb.className = 'role-badge replica'; rb.textContent = 'REPLICA';
  }

  // Stats
  setText('s-view', s.view);
  setText('s-leader', 'Node ' + s.leader);
  setText('s-quorum', s.quorum + '/' + s.total_nodes);
  setText('s-committed', s.committed_count);
  setText('s-accepted', s.accepted_count);

  // Cluster
  renderCluster(s);

  // Block tree
  renderTree(s);

  // Action panels
  renderActions(s);
}

// ── Cluster nodes ─────────────────────────────────────────────────────────────
function renderCluster(s) {
  const g = document.getElementById('cluster-grid');
  if (!s.peers || !s.peers.length) { g.innerHTML = '<div style="color:var(--text3);font-size:11px;padding:8px">No peers yet…</div>'; return; }
  g.innerHTML = s.peers.map(p => {
    let cls = 'node-card';
    if (p.is_leader) cls += ' is-leader';
    if (p.is_you)   cls += ' is-you';
    if (s.malicious && p.is_you) cls += ' malicious';
    const you = p.is_you ? '<span class="you-tag">YOU</span>' : '';
    const role = p.is_leader ? 'Leader' : 'Replica';
    return '<div class="' + cls + '">' +
      '<span class="node-star">★</span>' +
      '<div class="node-name">Node ' + p.id + you + '</div>' +
      '<div class="node-sub">:' + p.port + '</div>' +
      '<div class="node-role">' + role + '</div>' +
    '</div>';
  }).join('');
}

// ── Block tree ────────────────────────────────────────────────────────────────
function renderTree(s) {
  const ta = document.getElementById('tree-area');
  const blocks = s.blocks || [];

  // Collect all block ids to show
  let chips = [];

  // Genesis
  chips.push({ id: 0, parentId: -1, view: 0, data: 'genesis', committed: true, qcd: true, special: 'genesis' });

  // Accepted blocks
  for (const b of blocks) chips.push(b);

  // Pending block (leader waiting for votes)
  if (s.pending_block) {
    const pb = s.pending_block;
    if (!chips.find(c => c.id === pb.id)) chips.push({ ...pb, special: 'pending' });
    else chips.find(c => c.id === pb.id).special = 'pending';
  }

  // Proposal (replica waiting to vote)
  if (s.proposal) {
    const pr = s.proposal;
    if (!chips.find(c => c.id === pr.id)) chips.push({ ...pr, special: 'proposal' });
    else chips.find(c => c.id === pr.id).special = 'proposal';
  }

  if (chips.length <= 1 && !s.pending_block && !s.proposal) {
    ta.innerHTML = '<span class="tree-empty">Genesis only — no blocks proposed yet</span>';
    return;
  }

  // Sort by id
  chips.sort((a, b) => a.id - b.id);

  const html = chips.map((b, i) => {
    let cls = 'block-chip', idCls = 'block-id';
    if (b.special === 'pending')  { cls += ' pending';  idCls += ' pending-color'; }
    else if (b.special === 'proposal') { cls += ' proposal'; idCls += ' proposal-color'; }
    else if (b.committed)         { cls += ' committed'; idCls += ' committed-color'; }
    else if (b.qcd)               { cls += ' qcd';       idCls += ' qcd-color'; }

    let tags = '';
    if (b.id === s.locked_block && b.id !== 0) tags += '<div class="lock-tag">[L]</div>';
    if (b.id === s.high_qc_block && b.id !== 0) tags += '<div class="hqc-tag">[H]</div>';

    const label = b.id === 0 ? 'B0' : 'B' + b.id;
    const dataStr = b.data && b.data !== 'genesis' ? b.data.substring(0,12) + (b.data.length > 12 ? '…' : '') : '';
    const arrow = i > 0 ? '<span class="chain-arrow">→</span>' : '';

    return arrow + '<div class="' + cls + '" title="Block ' + b.id + '&#10;View: ' + b.view + '&#10;Data: ' + b.data + '&#10;Parent: ' + (b.parentId !== undefined ? b.parentId : b.parent_id) + '">' +
      '<div class="' + idCls + '">' + label + '</div>' +
      '<div class="block-view">v' + b.view + '</div>' +
      (dataStr ? '<div class="block-data">' + escHtml(dataStr) + '</div>' : '') +
      tags +
    '</div>';
  }).join('');

  ta.innerHTML = '<div class="chain">' + html + '</div>';
}

// ── Action panels ─────────────────────────────────────────────────────────────
function renderActions(s) {
  const vp  = document.getElementById('vote-panel');
  const pp  = document.getElementById('propose-panel');
  const ic  = document.getElementById('idle-card');

  // If there's a pending vote prompt still showing, keep it
  if (pendingVote) {
    vp.classList.add('show');
    pp.classList.remove('show');
    ic.style.display = 'none';
    return;
  }

  // Leader waiting to propose
  if (s.is_leader && !s.pending_block) {
    pendingPropose = true;
    pp.classList.add('show');
    vp.classList.remove('show');
    ic.style.display = 'none';
  } else if (s.proposal && !s.is_leader) {
    // Replica has a proposal — but if vote_prompt already came via WS, keep that
    // (handled in showVotePrompt)
  } else {
    pp.classList.remove('show');
    vp.classList.remove('show');
    ic.style.display = '';
    pendingPropose = false;
  }
}

// ── Vote prompt (from WS push) ────────────────────────────────────────────────
function showVotePrompt(msg) {
  pendingVote = msg;
  const b = msg.block;
  setText('vp-id', 'B' + b.id);
  setText('vp-view', b.view);
  setText('vp-parent', 'B' + b.parent_id);
  setText('vp-leader', 'Node ' + msg.leader_id);
  document.getElementById('vp-data').textContent = b.data || '(empty)';
  document.getElementById('btn-yes').disabled = false;
  document.getElementById('btn-no').disabled  = false;
  document.getElementById('vote-panel').classList.add('show');
  document.getElementById('propose-panel').classList.remove('show');
  document.getElementById('idle-card').style.display = 'none';
  toast('Proposal received from Node ' + msg.leader_id + ' — vote required', 'warn');
}

// ── Cast vote ─────────────────────────────────────────────────────────────────
async function castVote(answer) {
  document.getElementById('btn-yes').disabled = true;
  document.getElementById('btn-no').disabled  = true;
  try {
    const r = await fetch('/vote', {
      method: 'POST',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ answer })
    });
    const label = answer === 'y' ? '✔ Accepted' : '✖ Rejected';
    const kind  = answer === 'y' ? 'ok' : 'warn';
    toast('Vote sent: ' + label, kind);
    appendLog({ type:'event', level:'VOTE', message: 'You voted ' + label + ' for Block ' + (pendingVote?.block?.id ?? '?'), time: nowStr() });
  } catch(e) {
    toast('Failed to send vote', 'err');
  }
  pendingVote = null;
  document.getElementById('vote-panel').classList.remove('show');
  document.getElementById('idle-card').style.display = '';
}

// ── Submit propose ────────────────────────────────────────────────────────────
async function submitPropose() {
  const input = document.getElementById('propose-input');
  const data  = input.value.trim();
  try {
    const r = await fetch('/propose', {
      method: 'POST',
      headers: {'Content-Type':'application/json'},
      body: JSON.stringify({ data })
    });
    toast('Block data submitted: ' + (data || '(auto-generated)'), 'ok');
    appendLog({ type:'event', level:'LEADER', message: 'Proposed block data: ' + (data || '(auto-generated)'), time: nowStr() });
    input.value = '';
    pendingPropose = false;
    document.getElementById('propose-panel').classList.remove('show');
    document.getElementById('idle-card').style.display = '';
  } catch(e) {
    toast('Failed to submit proposal', 'err');
  }
}

// ── Event log ─────────────────────────────────────────────────────────────────
const MAX_LOG = 200;
function appendLog(ev) {
  const area = document.getElementById('log-area');
  const atBottom = area.scrollHeight - area.scrollTop - area.clientHeight < 40;
  const row = document.createElement('div');
  row.className = 'log-entry';
  row.innerHTML =
    '<span class="log-time">' + escHtml(ev.time||'') + '</span>' +
    '<span class="log-level ll-' + (ev.level||'SYS') + '">' + escHtml(ev.level||'SYS') + '</span>' +
    '<span class="log-msg">' + escHtml(ev.message||'') + '</span>';
  area.appendChild(row);
  while (area.children.length > MAX_LOG) area.removeChild(area.firstChild);
  if (atBottom) area.scrollTop = area.scrollHeight;

  // Special toast for commits
  if (ev.level === 'COMMIT') toast(ev.message, 'commit');
}

function clearLog() { document.getElementById('log-area').innerHTML = ''; }

// ── Toast ─────────────────────────────────────────────────────────────────────
function toast(msg, kind='ok') {
  const wrap = document.getElementById('toasts');
  const el = document.createElement('div');
  el.className = 'toast ' + kind;
  el.textContent = msg;
  wrap.appendChild(el);
  setTimeout(() => { el.style.opacity='0'; el.style.transition='opacity .4s'; setTimeout(()=>el.remove(), 400); }, 3500);
}

// ── Utilities ─────────────────────────────────────────────────────────────────
function setText(id, v) { const el = document.getElementById(id); if(el) el.textContent = v; }
function escHtml(s) { return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;'); }
function nowStr() { return new Date().toTimeString().slice(0,8); }

// ── Boot ──────────────────────────────────────────────────────────────────────
connect();
</script>
</body>
</html>`
