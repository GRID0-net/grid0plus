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
        <td><strong>${escape(p.nickname)}</strong></td>
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
            `<span class="pip ${stateClass(p.state)}"></span>` +
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
connect();
// Presence arrives on the stream; this catches everything else -- a fest
// crossing into its next phase, an account created elsewhere.
setInterval(refresh, 30000);
