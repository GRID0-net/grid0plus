// SwitchNet dashboard.
//
// No framework and no build step, on purpose: this repository ships Go and
// generated protobuf, and adding an npm toolchain to it would mean a second
// build system for one page. The page is small enough not to need one.
//
// The live view comes from an SSE stream of presence events. A presence change
// updates the affected row immediately; everything else refreshes on a slow
// poll, because it changes on human timescales.

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

const TOKEN = document.documentElement.dataset.token || "";

/** Appends the shared token, when the page was opened with one. */
function url(path) {
  return TOKEN ? `${path}${path.includes("?") ? "&" : "?"}token=${encodeURIComponent(TOKEN)}` : path;
}

// ---- theme ------------------------------------------------------------

// A viewer-local preference: what looks right on the operator's own screen
// has nothing to do with what the server should remember, so this lives in
// localStorage rather than a setting. Falls back to the OS preference (the
// CSS media query) when nothing has been chosen yet -- "system" is a real
// third state, not just "light" wearing a different name.
const THEME_KEY = "switchnet-dashboard-theme";

function applyTheme(theme) {
  if (theme === "light" || theme === "dark") {
    document.documentElement.dataset.theme = theme;
  } else {
    delete document.documentElement.dataset.theme;
  }
}

function currentTheme() {
  try {
    return localStorage.getItem(THEME_KEY) || "system";
  } catch {
    return "system"; // A private window or blocked storage: fall back quietly.
  }
}

(function initTheme() {
  applyTheme(currentTheme());
  $("#theme-toggle").addEventListener("click", () => {
    // System -> the opposite of whatever just rendered -> the other one ->
    // back to system. Two explicit states plus "let the OS decide" covers
    // everything without a three-way menu for a header button.
    const now = currentTheme();
    const rendersLight =
      now === "light" || (now === "system" && matchMedia("(prefers-color-scheme: light)").matches);
    const next = now === "system" ? (rendersLight ? "dark" : "light") : now === "light" ? "dark" : "system";
    applyTheme(next);
    try {
      if (next === "system") localStorage.removeItem(THEME_KEY);
      else localStorage.setItem(THEME_KEY, next);
    } catch {
      // Best-effort: the theme still applies to this load, it just will not
      // survive a refresh.
    }
  });
})();

async function api(path, options) {
  const res = await fetch(url(path), {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  if (!res.ok) {
    // The server's message is the useful part -- slot alignment, resource case
    // -- so it is surfaced rather than replaced with a generic failure.
    throw new Error((await res.text()).trim() || `${res.status}`);
  }
  return res.status === 204 ? null : res.json();
}

// ---- tabs -----------------------------------------------------------------

$$(".tabs button").forEach((tab) => {
  tab.addEventListener("click", () => {
    $$(".tabs button").forEach((t) => t.setAttribute("aria-selected", String(t === tab)));
    $$(".view").forEach((v) => v.classList.toggle("is-active", v.id === `view-${tab.dataset.view}`));
  });
});

// ---- rendering ------------------------------------------------------------

const stateClass = (s) => (s === "PLAYING" ? "playing" : s === "ONLINE" ? "online" : "offline");
const stateWord = (s) => (s === "PLAYING" ? "in game" : s === "ONLINE" ? "online" : "offline");

/**
 * A stable colour per user id, so the same player is always the same colour
 * across a session rather than reshuffling on every render. Two hue bands are
 * skipped -- roughly pink and green -- because those already mean something
 * (in game, online) and an avatar in either would read as a status pip
 * instead of an identity.
 */
function hueForID(id) {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (Math.imul(h, 31) + id.charCodeAt(i)) >>> 0;
  let hue = h % 360;
  if (hue > 300 || hue < 20) hue = (hue + 60) % 360;
  if (hue > 95 && hue < 140) hue = (hue + 80) % 360;
  return hue;
}

/** "MarinaFan" -> "MF", "agent3" -> "AG" -- initials for an avatar with no image. */
function initials(nickname) {
  const clean = (nickname || "").trim();
  if (!clean) return "?";
  const parts = clean.split(/[\s_.-]+/).filter(Boolean);
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase();
  return clean.slice(0, 2).toUpperCase();
}

/** Renders one avatar. sizeClass is "" for the table/strip default or "avatar-sm". */
function avatarHTML(id, nickname, sizeClass = "") {
  return `<span class="avatar ${sizeClass}" style="background:hsl(${hueForID(id)}deg 62% 45%)">` +
    `${escape(initials(nickname))}</span>`;
}

/** An avatar with a small status dot in the corner, for a player's own state. */
function avatarWithStatusHTML(p, sizeClass = "") {
  return `<span class="avatar-wrap">${avatarHTML(p.id, p.nickname, sizeClass)}` +
    `<span class="avatar-status ${stateClass(p.state)}"></span></span>`;
}

/** Human "3 minutes ago", because an ISO timestamp is not what the eye wants. */
function ago(iso) {
  if (!iso) return "never";
  const secs = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 45) return "just now";
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  return `${Math.floor(secs / 86400)}d ago`;
}

let players = [];

function renderPlayers() {
  const filter = ($("#player-filter").value || "").toLowerCase();
  const shown = players.filter((p) => p.nickname.toLowerCase().includes(filter));

  const tbody = $("#players-table tbody");
  tbody.replaceChildren(
    ...shown.map((p) => {
      const tr = document.createElement("tr");
      tr.dataset.player = p.id;
      tr.innerHTML = `
        <td><span class="player-cell">${avatarWithStatusHTML(p, "avatar-sm")}<strong>${escape(p.nickname)}</strong></span></td>
        <td><span class="badge ${stateClass(p.state)}">${stateWord(p.state)}</span></td>
        <td>${p.activity ? escape(p.activity) : "<span style='color:var(--fg-faint)'>—</span>"}</td>
        <td style="color:var(--fg-dim)">${ago(p.lastSeenAt)}</td>`;
      return tr;
    })
  );
  $("#players-empty").hidden = players.length > 0;

  // The overview strip shows only people who are actually around: a wall of
  // offline names is what the table is for.
  const around = players.filter((p) => p.state !== "OFFLINE");
  $("#player-strip").replaceChildren(
    ...(around.length
      ? around.map((p) => {
          const li = document.createElement("li");
          li.innerHTML =
            avatarWithStatusHTML(p) +
            `<span>${escape(p.nickname)}</span>` +
            (p.activity ? `<span class="what">${escape(p.activity)}</span>` : "");
          return li;
        })
      : [Object.assign(document.createElement("p"), { className: "empty", textContent: "Nobody is online." })])
  );
}

function renderOverview(o) {
  $("#stat-online").textContent = o.online;
  $("#stat-playing").textContent = o.playing;
  $("#stat-players").textContent = o.players;

  const banner = $("#fest-banner");
  if (!o.fest) {
    banner.classList.add("is-empty");
    banner.innerHTML = `<p class="empty">No Splatfest running.</p>`;
    return;
  }
  banner.classList.remove("is-empty");
  banner.innerHTML = `
    <div class="fid">${escape(o.fest.id)}</div>
    <div class="meta">${escape(o.fest.resource)} · ${o.fest.regions.join(" ")} · ${escape(o.fest.phase.replace(/_/g, " "))}</div>
    ${phaseTrack(o.fest.phase)}`;
}

const PHASES = ["announced", "first_half", "second_half", "counting", "closed"];

function phaseTrack(current) {
  const at = PHASES.indexOf(current);
  const bars = PHASES.map((_, i) => `<span class="${i < at ? "done" : i === at ? "now" : ""}"></span>`).join("");
  const names = PHASES.map(
    (p, i) => `<span class="${i === at ? "now" : ""}">${p.replace(/_/g, " ")}</span>`
  ).join("");
  return `<div class="phase-track">${bars}</div><div class="phase-names">${names}</div>`;
}

function renderFests(fests) {
  $("#fests-empty").hidden = fests.length > 0;
  $("#fest-list").replaceChildren(
    ...fests.map((f) => {
      const live = f.enabled && f.phase !== "none" && f.phase !== "closed";
      const card = document.createElement("div");
      card.className = "fest-card" + (live ? " live" : "");
      card.innerHTML = `
        <div class="cols">
          <div class="fid">${escape(f.id)}
            <span class="pill ${f.enabled ? "on" : ""}">${f.enabled ? "announced" : "held"}</span>
            <span class="pill">${escape(f.phase.replace(/_/g, " "))}</span>
            ${f.hasKeys ? "" : `<span class="pill warn" title="GetFestDecryptionKey will return nothing, so the packs cannot be opened">no keys</span>`}
          </div>
          <div class="meta">${escape(f.resource)} · rev ${escape(f.revision.slice(0, 12))}… · ${f.regions.join(" ")}</div>
          <div class="meta">starts ${new Date(f.startAt).toLocaleString()}</div>
        </div>
        <div class="actions">
          <button class="btn btn-ghost" data-toggle="${escape(f.id)}" data-next="${!f.enabled}">
            ${f.enabled ? "Hold" : "Announce"}
          </button>
          <button class="btn btn-ghost btn-danger" data-delete="${escape(f.id)}">Delete</button>
        </div>`;
      return card;
    })
  );
}

function escape(s) {
  return String(s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])
  );
}

// ---- actions --------------------------------------------------------------

$("#player-filter").addEventListener("input", renderPlayers);

$("#fest-list").addEventListener("click", async (e) => {
  const toggle = e.target.closest("[data-toggle]");
  const del = e.target.closest("[data-delete]");
  try {
    if (toggle) {
      await api(`/api/fests/${encodeURIComponent(toggle.dataset.toggle)}/enabled`, {
        method: "POST",
        body: JSON.stringify({ enabled: toggle.dataset.next === "true" }),
      });
      await refresh();
    } else if (del) {
      // Announcing a fest is reversible with the Hold button; deleting one is
      // not, so it asks.
      if (!confirm(`Delete ${del.dataset.delete}? This cannot be undone.`)) return;
      await api(`/api/fests/${encodeURIComponent(del.dataset.delete)}`, { method: "DELETE" });
      await refresh();
    }
  } catch (err) {
    alert(err.message);
  }
});

$("#fest-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const err = $("#fest-error");
  err.hidden = true;

  const regions = $$("#region-chips input:checked").map((c) => c.value);
  const local = $("#fest-start").value;

  try {
    await api("/api/fests", {
      method: "POST",
      body: JSON.stringify({
        startAt: local ? new Date(local).toISOString() : "",
        regions,
        resource: $("#fest-resource").value.trim(),
        revision: $("#fest-revision").value.trim(),
        enabled: $("#fest-enabled").checked,
      }),
    });
    e.target.reset();
    $$("#region-chips input").forEach((c) => (c.checked = true));
    await refresh();
  } catch (e2) {
    err.textContent = e2.message;
    err.hidden = false;
  }
});

// ---- service health -------------------------------------------------------

const HEALTH_WORD = { ok: "up", degraded: "degraded", down: "down", unknown: "not probed" };

function renderHealth(services) {
  const down = services.filter((s) => s.status === "down").length;
  const degraded = services.filter((s) => s.status === "degraded").length;

  $("#health-summary").textContent =
    down || degraded
      ? `${down} down · ${degraded} degraded`
      : `all ${services.length} services up`;

  $("#health-list").replaceChildren(
    ...services.map((s) => {
      const card = document.createElement("div");
      card.className = "svc svc-" + s.status;

      const checks = Object.entries(s.checks || {})
        .map(([name, c]) =>
          `<span class="pill ${c.ok ? "on" : "warn"}" title="${escape(c.error || "")}">` +
          `${escape(name)}${c.ok ? "" : " failed"}</span>`)
        .join(" ");

      card.innerHTML = `
        <div class="cols">
          <div class="fid">
            <span class="pip ${s.status === "ok" ? "online" : s.status === "unknown" ? "offline" : "playing"}"></span>
            ${escape(s.name)}
            <span class="pill ${s.status === "ok" ? "on" : s.status === "unknown" ? "" : "warn"}">${HEALTH_WORD[s.status] || s.status}</span>
            ${checks}
          </div>
          <div class="meta">${escape(s.addr)}${
            s.uptimeSeconds ? " · up " + uptime(s.uptimeSeconds) : ""
          } · ${s.tookMs}ms</div>
          ${s.detail ? `<div class="meta svc-detail">${escape(s.detail)}</div>` : ""}
        </div>`;
      return card;
    })
  );
}

/** "3d 4h" / "2h 10m" / "45s" — an uptime that is always small is a crash loop. */
function uptime(seconds) {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d) return `${d}d ${h}h`;
  if (h) return `${h}h ${m}m`;
  if (m) return `${m}m`;
  return `${seconds}s`;
}

async function loadHealth() {
  try {
    renderHealth((await api("/api/health")).services || []);
  } catch (e) {
    $("#health-summary").textContent = "could not read health";
    $("#health-list").replaceChildren(
      Object.assign(document.createElement("p"), { className: "empty", textContent: e.message })
    );
  }
}

// ---- matchmaking ----------------------------------------------------------

let matchmaking = { configured: false };

/** "3m 20s" — a wait is read as a duration, not as a count of seconds. */
function waited(seconds) {
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  return `${m}m ${seconds % 60}s`;
}

function renderMatchmaking(mm) {
  matchmaking = mm;

  $("#mm-unconfigured").hidden = mm.configured;
  $("#mm-configured").hidden = !mm.configured;

  if (!mm.configured) {
    $("#mm-unconfigured-reason").textContent = mm.reason || "";
    $("#stat-queue").textContent = "—";
    $("#stat-queue-note").textContent = "not configured";
    return;
  }

  const modes = mm.modes || [];
  const waiting = mm.waiting || 0;

  // The overview tile, finally showing a real number rather than admitting it
  // is not wired up.
  $("#stat-queue").textContent = waiting;
  $("#stat-queue-note").textContent =
    modes.length === 0 ? "no modes queuing" : `across ${modes.length} mode${modes.length === 1 ? "" : "s"}`;

  $("#mm-summary").textContent =
    `${waiting} waiting · default ${mm.defaultSize} per match`;
  $("#mm-empty").hidden = modes.length > 0;

  $("#mm-modes").replaceChildren(
    ...modes.map((m) => {
      const short = m.waiting.length > 0 && m.waiting.length < m.size;
      const card = document.createElement("div");
      card.className = "mm-mode";
      card.innerHTML = `
        <div class="cols">
          <div class="fid">${escape(m.mode)}
            <span class="pill ${short ? "warn" : "on"}">${m.waiting.length} / ${m.size}</span>
            ${m.overridden ? `<span class="pill" title="Set here rather than taken from the default">custom size</span>` : ""}
          </div>
          <div class="meta">${
            m.waiting.length === 0
              ? "nobody waiting"
              : short
                ? `${m.size - m.waiting.length} short of a full match`
                : "enough players to start"
          }</div>
        </div>
        <div class="actions">
          <label class="mm-size">
            <span>players</span>
            <input type="number" min="1" max="16" value="${m.size}" data-size-for="${escape(m.mode)}">
          </label>
          <button class="btn btn-ghost" data-start="${escape(m.mode)}"
            ${m.waiting.length === 0 ? "disabled" : ""}
            title="Form a match right now from everyone waiting, however few that is">
            Start now
          </button>
        </div>`;

      if (m.waiting.length > 0) {
        const list = document.createElement("ul");
        list.className = "mm-tickets";
        list.replaceChildren(
          ...m.waiting.map((t) => {
            const li = document.createElement("li");
            li.innerHTML =
              `<span class="pip online"></span>` +
              `<span>${escape(playerName(t.userId))}</span>` +
              `<span class="what">waiting ${waited(t.waitingForSeconds)}</span>` +
              `<button class="btn btn-ghost btn-danger btn-tiny" data-drop="${escape(t.id)}" ` +
              `title="Take this ticket out of the queue">Drop</button>`;
            return li;
          })
        );
        card.appendChild(list);
      }
      return card;
    })
  );

  const sessions = mm.sessions || [];
  $("#mm-sessions-empty").hidden = sessions.length > 0;
  $("#mm-sessions").replaceChildren(
    ...sessions.map((s) => {
      const card = document.createElement("div");
      card.className = "mm-session";
      card.innerHTML = `
        <div class="cols">
          <div class="fid">${escape(s.mode)}
            ${s.forced ? `<span class="pill warn" title="Fewer players than this mode currently wants. Either it was force-started, or the mode was resized after it formed.">undersized</span>` : ""}
          </div>
          <div class="meta">${s.players.length} player${s.players.length === 1 ? "" : "s"} ·
            host ${escape(playerName(s.host))} ·
            ${new Date(s.createdAt).toLocaleTimeString()}</div>
          <div class="meta">${s.players.map((p) => escape(playerName(p))).join(", ")}</div>
        </div>`;
      return card;
    })
  );
}

/** A user id is not a name. Resolve it against the players list when we can. */
function playerName(userId) {
  const p = players.find((x) => x.id === userId);
  return p ? p.nickname : userId;
}

async function loadMatchmaking() {
  try {
    renderMatchmaking(await api("/api/matchmaking"));
  } catch (e) {
    // Reaching the dashboard but not npln is worth saying plainly, and is a
    // different problem from the dashboard being down.
    renderMatchmaking({ configured: false, reason: e.message });
  }
}

$("#mm-modes").addEventListener("click", async (e) => {
  const start = e.target.closest("[data-start]");
  const drop = e.target.closest("[data-drop]");
  if (!start && !drop) return;

  try {
    if (start) {
      const mode = start.dataset.start;
      const m = (matchmaking.modes || []).find((x) => x.mode === mode);
      // Only ask when it is actually short. Confirming something that was going
      // to happen anyway is the kind of dialog people learn to click through.
      if (m && m.waiting.length < m.size &&
          !confirm(`Start ${mode} with ${m.waiting.length} of ${m.size} players?\n\n` +
                   `Whether the game accepts an undersized match is not known — that is what this finds out.`)) {
        return;
      }
      renderMatchmaking({ ...(await api(`/api/matchmaking/modes/${encodeURIComponent(mode)}/start`,
        { method: "POST" })) });
    } else {
      renderMatchmaking(await api(`/api/matchmaking/tickets/${encodeURIComponent(drop.dataset.drop)}/cancel`,
        { method: "POST" }));
    }
  } catch (err) {
    alert(err.message);
  }
});

$("#mm-modes").addEventListener("change", async (e) => {
  const input = e.target.closest("[data-size-for]");
  if (!input) return;

  const players = Number(input.value);
  if (!Number.isInteger(players) || players < 1) {
    alert("A match needs at least one player.");
    await loadMatchmaking();
    return;
  }

  try {
    renderMatchmaking(await api(
      `/api/matchmaking/modes/${encodeURIComponent(input.dataset.sizeFor)}/size`,
      { method: "POST", body: JSON.stringify({ players }) }));
  } catch (err) {
    alert(err.message);
    await loadMatchmaking();
  }
});

// ---- match history ----------------------------------------------------------

function renderMatches(matches) {
  $("#matches-empty").hidden = matches.length > 0;
  $("#matches-summary").textContent = matches.length
    ? `${matches.length} recorded`
    : "";

  $("#matches-list").replaceChildren(
    ...matches.map((m) => {
      const card = document.createElement("div");
      card.className = "mm-session";
      card.innerHTML = `
        <div class="cols">
          <div class="fid">${escape(m.mode)}
            ${m.forced ? `<span class="pill warn" title="Started by an operator with fewer players than the mode wanted, rather than filling on its own">forced</span>` : ""}
          </div>
          <div class="meta">${m.playerIds.length} player${m.playerIds.length === 1 ? "" : "s"} ·
            host ${escape(playerName(m.hostUserId))} ·
            ${ago(m.createdAt)}</div>
          <div class="meta">${m.playerIds.map((p) => escape(playerName(p))).join(", ")}</div>
        </div>`;
      return card;
    })
  );
}

async function loadMatches() {
  try {
    renderMatches(await api("/api/matches"));
  } catch (e) {
    $("#matches-summary").textContent = "could not read match history";
  }
}

// ---- live stream ----------------------------------------------------------

function setLive(state, label) {
  const pill = $("#live-pill");
  pill.dataset.state = state;
  $(".label", pill).textContent = label;
}

function connect() {
  const es = new EventSource(url("/api/events"));

  es.addEventListener("open", () => setLive("live", "live"));

  es.addEventListener("nobus", () => {
    // The server said it has no bus. Reconnecting would not help, and a pill
    // that says "live" while nothing arrives is worse than one that admits it.
    setLive("down", "no event bus");
    es.close();
  });

  es.addEventListener("presence", (ev) => {
    let e;
    try {
      e = JSON.parse(ev.data);
    } catch {
      return;
    }
    const p = players.find((x) => x.id === e.userId);
    if (p) {
      p.state = e.state;
      p.appId = e.appId || "";
      p.lastSeenAt = e.at;
      renderPlayers();
      const row = $(`[data-player="${CSS.escape(e.userId)}"]`);
      if (row) {
        row.classList.remove("just-changed");
        void row.offsetWidth; // restart the animation
        row.classList.add("just-changed");
      }
    }
    // A player we have never seen means a new account: pull the full list.
    if (!p) refresh();
  });

  es.addEventListener("error", () => {
    setLive("down", "reconnecting");
    // EventSource retries on its own; nothing to do but say so.
  });
}

// ---- refresh --------------------------------------------------------------

async function refresh() {
  try {
    const [o, ps, fs] = await Promise.all([
      api("/api/overview"),
      api("/api/players"),
      api("/api/fests"),
    ]);
    players = ps;
    renderOverview(o);
    renderPlayers();
    renderFests(fs);
    // After players, so a ticket or a match's host/players can be shown by
    // nickname rather than by id.
    await Promise.all([loadMatchmaking(), loadMatches()]);
  } catch (e) {
    setLive("down", "server unreachable");
  }
}

// A default start time of the next Saturday, matching the server's own default,
// so the form opens on something sensible rather than empty.
(function seedStartTime() {
  const d = new Date();
  d.setUTCHours(0, 0, 0, 0);
  do {
    d.setUTCDate(d.getUTCDate() + 1);
  } while (d.getUTCDay() !== 6);
  const pad = (n) => String(n).padStart(2, "0");
  $("#fest-start").value =
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
})();

refresh();

// Health is loaded SEPARATELY from refresh(), and never inside its try.
//
// refresh() reads players and the overview, both of which fail when the
// database is down -- which is exactly when the health page is the thing being
// looked at. Chaining health onto the end of it meant the one view that could
// explain the outage was the one that rendered empty during it.
loadHealth();
setInterval(loadHealth, 15000);

connect();
// Presence arrives on the stream; this catches everything else -- a fest
// crossing into its next phase, an account created elsewhere.
setInterval(refresh, 30000);
