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

  // Null and zero mean different things here and are shown differently. Null is
  // "the game server is not reporting" -- cmd/npln down, or redis.url unset in
  // one of the two processes. Zero is "nobody is queuing". A tile that showed 0
  // for both would have an operator debugging matchmaking when the problem is
  // that nothing is publishing.
  const queue = $("#stat-queue");
  const note = $("#stat-queue-note");
  if (!o.matchmaking) {
    queue.textContent = "—";
    note.textContent = "no report from the game server";
    note.hidden = false;
  } else {
    queue.textContent = o.matchmaking.waiting;
    const modes = Object.entries(o.matchmaking.byMode || {});
    note.textContent = modes.length
      ? modes.map(([m, n]) => `${m} ${n}`).join(" · ")
      : `reported ${ago(o.matchmaking.reportAt)}`;
    note.hidden = false;
  }

  renderSeed(o.seed);

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

function renderSeed(seed) {
  const el = $("#seed-status");
  if (!el) return;
  if (!seed || !seed.configured) {
    el.innerHTML = `<p class="empty">No <code>bcat.seed_dir</code> configured, so the installer has nothing to fetch.</p>`;
    return;
  }
  if (!seed.built) {
    el.innerHTML = `<p class="empty">Configured, but no seed has been built yet.</p>`;
    return;
  }
  el.innerHTML = `
    <div class="fid">${escape(seed.festId || "(unnamed)")}</div>
    <div class="meta">${escape(seed.resourceId || "?")} · ${(seed.regions || []).join(" ")}</div>
    <div class="meta mono">${escape(seed.revision || "")}</div>`;
}

let matches = [];

function renderMatches() {
  const tbody = $("#matches-table tbody");
  if (!tbody) return;
  tbody.replaceChildren(
    ...matches.map((m) => {
      const tr = document.createElement("tr");
      tr.innerHTML = `
        <td style="color:var(--fg-dim)">${ago(m.createdAt)}</td>
        <td><code>${escape(m.mode)}</code>
          ${m.forced ? `<span class="pill warn" title="Started by an operator with fewer players than the mode wanted, rather than filling on its own">forced</span>` : ""}
        </td>
        <td><strong>${escape(playerName(m.hostUserId))}</strong></td>
        <td>${(m.playerIds || []).map((p) => escape(playerName(p))).join(", ")}</td>`;
      return tr;
    })
  );
  $("#matches-empty").hidden = matches.length > 0;
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

// ---- Salmon Run / Big Run --------------------------------------------------

// coopReference holds the real stage/weapon/boss tables from
// /api/coop-reference, fetched once: they are the game's own static data
// (internal/toyohr/coop_ids.go), not something that changes while the page
// is open, unlike the shifts themselves.
let coopReference = null;

async function loadCoopReference() {
  coopReference = await api("/api/coop-reference");
  populateCoopStageSelect();
  const weaponOptions = coopReference.weapons
    .map((w) => `<option value="${w.id}">${escape(w.name)}</option>`)
    .join("");
  $$(".coop-weapon").forEach((sel) => (sel.innerHTML = weaponOptions));
  $("#coop-kuma").innerHTML = coopReference.kumaWeapons
    .map((w) => `<option value="${w.id}">${escape(w.name)}</option>`)
    .join("");
  const bossSelect = $("#coop-boss");
  coopReference.bosses.forEach((b) => {
    const opt = document.createElement("option");
    opt.value = b;
    opt.textContent = b;
    bossSelect.appendChild(opt);
  });
}

// populateCoopStageSelect fills the stage dropdown with only the stages that
// match the currently-selected Kind -- a real console never sees Big Run
// scheduled on an ordinary stage, so the form does not offer the combination.
function populateCoopStageSelect() {
  if (!coopReference) return;
  const bigRun = $('input[name="coop-kind"]:checked').value === "big_run";
  $("#coop-stage").innerHTML = coopReference.stages
    .filter((s) => s.bigRun === bigRun)
    .map((s) => `<option value="${s.id}">${escape(s.name)}</option>`)
    .join("");
}

// updateCoopKindFields shows/hides the fields that only some Kinds carry on
// the wire, rather than letting the operator fill in a value Validate will
// only reject after a round trip to the server: CoopSchedule_TeamContest has
// no boss and no reward_gear_id field, and only BigRun/TeamContest carry
// rewards at all.
function updateCoopKindFields() {
  const kind = $('input[name="coop-kind"]:checked').value;
  $("#coop-rewards-field").hidden = kind !== "big_run" && kind !== "team_contest";
  $("#coop-boss-field").hidden = kind === "team_contest";
  $("#coop-reward-gear-field").hidden = kind === "team_contest";
}

$$('input[name="coop-kind"]').forEach((radio) =>
  radio.addEventListener("change", () => {
    populateCoopStageSelect();
    updateCoopKindFields();
  })
);

const COOP_STATUS_LABEL = { upcoming: "upcoming", live: "live", ended: "ended" };
const COOP_KIND_LABEL = { big_run: "Big Run", team_contest: "Team Contest", normal: "Normal" };

function renderCoopShifts(shifts) {
  $("#coop-empty").hidden = shifts.length > 0;
  $("#coop-list").replaceChildren(
    ...shifts.map((c) => {
      const card = document.createElement("div");
      card.className = "fest-card" + (c.status === "live" ? " live" : "");
      const statusPill =
        c.status === "live" ? "on" : c.status === "ended" ? "warn" : "";
      card.innerHTML = `
        <div class="cols">
          <div class="fid">${escape(c.stageName)}
            <span class="pill">${COOP_KIND_LABEL[c.kind] || c.kind}</span>
            <span class="pill ${statusPill}">${COOP_STATUS_LABEL[c.status] || c.status}</span>
            ${c.generated ? '<span class="pill" title="Not stored -- computed live off the clock, the same as a console currently sees. Nothing to delete.">auto</span>' : ""}
          </div>
          <div class="meta">${c.weaponNames.map(escape).join(" · ")}${
            c.kumaWeaponName ? " · kuma: " + escape(c.kumaWeaponName) : ""
          }</div>
          <div class="meta">${c.boss ? "boss: " + escape(c.boss) + " · " : ""}${new Date(c.startAt).toLocaleString()} &rarr; ${new Date(c.endAt).toLocaleString()}</div>
        </div>
        <div class="actions">
          ${c.generated ? "" : `<button class="btn btn-ghost btn-danger" data-delete-coop="${escape(c.id)}">Delete</button>`}
        </div>`;
      return card;
    })
  );
}

// ---- schedule viewer --------------------------------------------------

/** One Turf War/Anarchy slot, or one Salmon Run shift, rendered as a card --
 * the same fest-card look the Salmon Run tab already uses, since this is
 * the same kind of content, just read-only. */
function renderVsSchedule(slots) {
  $("#schedule-vs-empty").hidden = slots.length > 0;
  $("#schedule-vs-list").replaceChildren(
    ...slots.map((s) => {
      const card = document.createElement("div");
      card.className = "fest-card";
      const regular = (s.regular || []).map((st) => escape(st.name)).join(" · ");
      const anarchy = (s.anarchy || [])
        .map((r) => `${escape(r.name)}: ${r.stages.map((st) => escape(st.name)).join(" · ")}`)
        .join("<br>");
      card.innerHTML = `
        <div class="cols">
          <div class="fid">${new Date(s.startAt).toLocaleString()} &rarr; ${new Date(s.endAt).toLocaleString()}</div>
          ${regular ? `<div class="meta">Turf War: ${regular}</div>` : ""}
          ${anarchy ? `<div class="meta">${anarchy}</div>` : ""}
        </div>`;
      return card;
    })
  );
}

/** Reuses renderCoopShifts' card markup for the read-only viewer too --
 * same data, same shape (coopShiftRow), just a different list element and
 * with delete buttons never shown here regardless of c.generated. */
function renderScheduleCoop(shifts) {
  $("#schedule-coop-empty").hidden = shifts.length > 0;
  $("#schedule-coop-list").replaceChildren(
    ...shifts.map((c) => {
      const card = document.createElement("div");
      card.className = "fest-card" + (c.status === "live" ? " live" : "");
      const statusPill = c.status === "live" ? "on" : c.status === "ended" ? "warn" : "";
      card.innerHTML = `
        <div class="cols">
          <div class="fid">${escape(c.stageName)}
            <span class="pill">${COOP_KIND_LABEL[c.kind] || c.kind}</span>
            <span class="pill ${statusPill}">${COOP_STATUS_LABEL[c.status] || c.status}</span>
            ${c.generated ? '<span class="pill" title="Not stored -- computed live off the clock.">auto</span>' : ""}
          </div>
          <div class="meta">${c.weaponNames.map(escape).join(" · ")}${
            c.kumaWeaponName ? " · kuma: " + escape(c.kumaWeaponName) : ""
          }</div>
          <div class="meta">${c.boss ? "boss: " + escape(c.boss) + " · " : ""}${new Date(c.startAt).toLocaleString()} &rarr; ${new Date(c.endAt).toLocaleString()}</div>
        </div>`;
      return card;
    })
  );
}

async function loadScheduleViewer() {
  try {
    const data = await api("/api/schedule-viewer");
    renderVsSchedule(data.vs || []);
    renderScheduleCoop(data.coop || []);
  } catch (err) {
    // Read-only, low-priority tab: fail quietly rather than alert()ing
    // whoever happens to have this tab open when a poll misses.
    console.error("schedule viewer:", err);
  }
}

$("#coop-list").addEventListener("click", async (e) => {
  const del = e.target.closest("[data-delete-coop]");
  if (!del) return;
  try {
    if (!confirm(`Delete this shift? This cannot be undone.`)) return;
    await api(`/api/coop-shifts/${encodeURIComponent(del.dataset.deleteCoop)}`, { method: "DELETE" });
    await refresh();
  } catch (err) {
    alert(err.message);
  }
});

$("#coop-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const err = $("#coop-error");
  err.hidden = true;

  const startLocal = $("#coop-start").value;
  const endLocal = $("#coop-end").value;
  const kind = $('input[name="coop-kind"]:checked').value;
  const rewardsRaw = $("#coop-rewards").value.trim();

  // team_contest carries neither on the wire (CoopSchedule_TeamContest has no
  // boss or reward_gear_id field), so a value left over from switching Kind
  // away from normal/big_run is cleared here rather than submitted and
  // rejected by Validate -- the field is only hidden, not disabled, so its
  // old value is still sitting in the DOM.
  const teamContest = kind === "team_contest";

  try {
    await api("/api/coop-shifts", {
      method: "POST",
      body: JSON.stringify({
        kind,
        stageId: Number($("#coop-stage").value),
        boss: teamContest ? "" : $("#coop-boss").value,
        weapons: $$(".coop-weapon").map((sel) => Number(sel.value)),
        kumaWeaponId: Number($("#coop-kuma").value),
        rewardGearId: teamContest ? 0 : Number($("#coop-reward-gear").value || 0),
        rewards: rewardsRaw
          ? rewardsRaw.split(",").map((s) => Number(s.trim())).filter((n) => !Number.isNaN(n))
          : [],
        startAt: startLocal ? new Date(startLocal).toISOString() : "",
        endAt: endLocal ? new Date(endLocal).toISOString() : "",
      }),
    });
    $("#coop-reward-gear").value = "";
    $("#coop-rewards").value = "";
    await refresh();
  } catch (e2) {
    err.textContent = e2.message;
    err.hidden = false;
  }
});

$("#coop-generate-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const err = $("#coop-gen-error");
  const result = $("#coop-gen-result");
  err.hidden = true;
  result.hidden = true;

  const fromLocal = $("#coop-gen-from").value;
  const durationHours = Number($("#coop-gen-duration").value || 0);

  try {
    const res = await api("/api/coop-shifts/generate", {
      method: "POST",
      body: JSON.stringify({
        fromAt: fromLocal ? new Date(fromLocal).toISOString() : "",
        count: Number($("#coop-gen-count").value || 0),
        durationHours,
      }),
    });
    result.textContent = `Generated ${res.created} shift(s)` + (res.skipped ? `, ${res.skipped} slot(s) already had one and were left alone.` : ".");
    result.hidden = false;
    await refresh();
  } catch (e2) {
    err.textContent = e2.message;
    err.hidden = false;
  }
});

function escape(s) {
  return String(s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c])
  );
}

// ---- actions --------------------------------------------------------------

$("#player-filter").addEventListener("input", renderPlayers);

$("#players-table tbody").addEventListener("click", (e) => {
  const row = e.target.closest("tr[data-player]");
  if (row) openPlayerDetail(row.dataset.player);
});

$("#player-detail-close").addEventListener("click", () => $("#player-detail").close());
// A click on the ::backdrop lands on the <dialog> element itself, never on
// anything inside it -- that is what distinguishes "clicked outside" from
// "clicked the content" without tracking coordinates by hand.
$("#player-detail").addEventListener("click", (e) => {
  if (e.target === e.currentTarget) e.currentTarget.close();
});

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

// ---- player detail ----------------------------------------------------------

/**
 * Everything the click-through detail panel needs is already fetched
 * elsewhere: the player rows for state changes, /api/players/{id} for
 * friends and device accounts, and the match history already loaded for the
 * Matches tab, filtered here rather than re-fetched -- refresh() always
 * fetches it before the page is interactive.
 */
async function openPlayerDetail(id) {
  const dialog = $("#player-detail");
  const body = $("#player-detail-body");
  body.innerHTML = `<p class="empty">Loading…</p>`;
  dialog.showModal();

  let d;
  try {
    d = await api(`/api/players/${encodeURIComponent(id)}`);
  } catch (e) {
    body.innerHTML = `<p class="empty">Could not load this player: ${escape(e.message)}</p>`;
    return;
  }
  renderPlayerDetail(d);
}

function renderPlayerDetail(d) {
  const played = matches.filter((m) => m.hostUserId === d.id || m.playerIds.includes(d.id));

  const friendsHTML = d.friends.length
    ? d.friends
        .map(
          (f) => `
        <li class="player-cell">
          ${avatarWithStatusHTML(f, "avatar-sm")}
          <span>${escape(f.nickname)}</span>
          ${f.favorite ? `<span class="pill on" title="Favourite friend">★</span>` : ""}
          <span class="meta">${f.activity ? escape(f.activity) : stateWord(f.state)}</span>
        </li>`
        )
        .join("")
    : `<li class="empty">No friends.</li>`;

  const devicesHTML = d.deviceAccounts.length
    ? d.deviceAccounts
        .map(
          (da) => `
        <li>
          <code>${escape(da.id)}</code>
          <span class="meta">linked ${ago(da.createdAt)}</span>
        </li>`
        )
        .join("")
    : `<li class="empty">No device accounts.</li>`;

  const matchesHTML = played.length
    ? played
        .map(
          (m) => `
        <li>
          <span class="pill">${escape(m.mode)}</span>
          <span class="meta">${m.hostUserId === d.id ? "hosted" : "played"} · ${ago(m.createdAt)}</span>
        </li>`
        )
        .join("")
    : `<li class="empty">No recorded matches.</li>`;

  $("#player-detail-body").innerHTML = `
    <div class="player-detail-head">
      ${avatarWithStatusHTML(d)}
      <div>
        <h3>${escape(d.nickname)}</h3>
        <p class="meta">
          <span class="badge ${stateClass(d.state)}">${stateWord(d.state)}</span>
          ${d.activity ? escape(d.activity) : ""}
          · account since ${ago(d.createdAt)}
          · last seen ${ago(d.lastSeenAt)}
        </p>
      </div>
    </div>
    <div class="player-detail-cols">
      <section>
        <h4>Friends <span class="meta">${d.friends.length}</span></h4>
        <ul class="player-detail-list">${friendsHTML}</ul>
      </section>
      <section>
        <h4>Device accounts <span class="meta">${d.deviceAccounts.length}</span></h4>
        <ul class="player-detail-list">${devicesHTML}</ul>
      </section>
      <section>
        <h4>Recent matches <span class="meta">${played.length}</span></h4>
        <ul class="player-detail-list">${matchesHTML}</ul>
      </section>
    </div>`;
}

// ---- debug ------------------------------------------------------------

// [key in the /api/debug/stats response, label] -- listed rather than
// iterated over Object.entries so the order on the page is deliberate and a
// key the server adds later does not silently appear at a random spot.
const DEBUG_STAT_LABELS = [
  ["users", "Users"],
  ["deletedUsers", "Deleted users"],
  ["deviceAccounts", "Device accounts"],
  ["deletedDeviceAccounts", "Deleted device accounts"],
  ["devices", "Devices seen"],
  ["friendships", "Friendship rows"],
  ["fests", "Splatfests"],
  ["coopShifts", "Coop shifts"],
  ["matches", "Matches recorded"],
  ["images", "Images stored"],
];

function renderDebugStats(stats) {
  $("#debug-stats").replaceChildren(
    ...DEBUG_STAT_LABELS.map(([key, label]) => {
      const div = document.createElement("div");
      div.className = "debug-stat";
      div.innerHTML =
        `<span class="debug-stat-value">${stats[key] ?? 0}</span>` +
        `<span class="debug-stat-label">${label}</span>`;
      return div;
    })
  );
}

async function loadDebugStats() {
  try {
    renderDebugStats(await api("/api/debug/stats"));
  } catch (e) {
    $("#debug-stats").replaceChildren(
      Object.assign(document.createElement("p"), {
        className: "empty",
        textContent: "Could not load database stats: " + e.message,
      })
    );
  }
}

$("#debug-stats-refresh").addEventListener("click", loadDebugStats);

function presenceRowsHTML(p) {
  return `<ul class="player-detail-list">
    <li><span class="badge ${stateClass(p.state)}">${stateWord(p.state)}</span>${
      p.appId ? ` <span class="meta">${escape(p.appId)}</span>` : ""
    }</li>
    <li class="meta">reported ${p.reportedAt ? ago(p.reportedAt) : "never"}</li>
    <li class="meta">last seen ${p.lastSeenAt ? ago(p.lastSeenAt) : "never"}</li>
    ${p.logoutAt ? `<li class="meta">signed out ${ago(p.logoutAt)}</li>` : ""}
  </ul>`;
}

function renderDebugAccount(d) {
  $("#debug-lookup-empty").hidden = true;

  const devicesHTML = d.deviceAccounts.length
    ? d.deviceAccounts
        .map(
          (da) => `
      <li>
        <code>${escape(da.id)}</code>
        <span class="meta">linked ${ago(da.createdAt)}</span>
        ${da.deletedAt ? `<span class="pill warn">deleted ${ago(da.deletedAt)}</span>` : ""}
        ${
          da.tokensRevokedBefore
            ? `<span class="pill" title="Tokens minted before this instant are refused">tokens revoked ${ago(da.tokensRevokedBefore)}</span>`
            : ""
        }
        <span class="actions">
          <button class="btn btn-ghost btn-tiny" data-revoke="${escape(da.id)}">Revoke tokens</button>
          ${
            da.deletedAt
              ? `<button class="btn btn-ghost btn-tiny" data-undelete-da="${escape(da.id)}">Undelete</button>`
              : `<button class="btn btn-ghost btn-danger btn-tiny" data-delete-da="${escape(da.id)}">Delete</button>`
          }
        </span>
      </li>`
        )
        .join("")
    : `<li class="empty">No device accounts.</li>`;

  $("#debug-account").innerHTML = `
    <div class="debug-account-head">
      <div>
        <h3>${escape(d.nickname || d.id)} <code class="meta">${escape(d.id)}</code></h3>
        <p class="meta">account since ${ago(d.createdAt)}</p>
      </div>
      <button class="btn btn-ghost${d.deleted ? "" : " btn-danger"}" id="debug-toggle-deleted"
        data-id="${escape(d.id)}" data-next="${!d.deleted}">
        ${d.deleted ? "Undelete account" : "Delete account"}
      </button>
    </div>
    <div class="debug-presence-cols">
      <section>
        <h4>Raw presence <span class="meta" title="What the console last reported, unresolved">as stored</span></h4>
        ${presenceRowsHTML(d.rawPresence)}
      </section>
      <section>
        <h4>Effective presence <span class="meta" title="What the friends applet actually sees, resolved the same way everywhere else">resolved</span></h4>
        ${presenceRowsHTML(d.effectivePresence)}
      </section>
    </div>
    <section>
      <h4>Device accounts <span class="meta">${d.deviceAccounts.length}</span></h4>
      <ul class="player-detail-list debug-device-list">${devicesHTML}</ul>
    </section>`;
}

async function lookupDebugAccount(id) {
  const container = $("#debug-account");
  container.innerHTML = `<p class="empty">Loading…</p>`;
  try {
    renderDebugAccount(await api(`/api/debug/accounts/${encodeURIComponent(id)}`));
  } catch (e) {
    container.innerHTML = `<p class="empty">Could not load this account: ${escape(e.message)}</p>`;
  }
}

$("#debug-lookup-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const id = $("#debug-lookup-id").value.trim();
  if (id) lookupDebugAccount(id);
});

$("#debug-account").addEventListener("click", async (e) => {
  const toggleAccount = e.target.closest("#debug-toggle-deleted");
  const revoke = e.target.closest("[data-revoke]");
  const deleteDa = e.target.closest("[data-delete-da]");
  const undeleteDa = e.target.closest("[data-undelete-da]");
  if (!toggleAccount && !revoke && !deleteDa && !undeleteDa) return;

  const id = $("#debug-lookup-id").value.trim();
  try {
    if (toggleAccount) {
      const next = toggleAccount.dataset.next === "true";
      if (
        next &&
        !confirm(`Delete account ${toggleAccount.dataset.id}? It will be refused at login until undeleted here.`)
      ) {
        return;
      }
      await api(`/api/debug/accounts/${encodeURIComponent(toggleAccount.dataset.id)}/deleted`, {
        method: "POST",
        body: JSON.stringify({ deleted: next }),
      });
    } else if (revoke) {
      await api(
        `/api/debug/accounts/${encodeURIComponent(id)}/device-accounts/${encodeURIComponent(revoke.dataset.revoke)}/revoke-tokens`,
        { method: "POST" }
      );
    } else if (deleteDa) {
      if (
        !confirm(
          `Delete device account ${deleteDa.dataset.deleteDa}? The console it belongs to will be refused at login until undeleted here.`
        )
      ) {
        return;
      }
      await api(
        `/api/debug/accounts/${encodeURIComponent(id)}/device-accounts/${encodeURIComponent(deleteDa.dataset.deleteDa)}/deleted`,
        { method: "POST", body: JSON.stringify({ deleted: true }) }
      );
    } else if (undeleteDa) {
      await api(
        `/api/debug/accounts/${encodeURIComponent(id)}/device-accounts/${encodeURIComponent(undeleteDa.dataset.undeleteDa)}/deleted`,
        { method: "POST", body: JSON.stringify({ deleted: false }) }
      );
    }
    await lookupDebugAccount(id);
  } catch (err) {
    alert(err.message);
  }
});

// ---- live table browser ----------------------------------------------------
//
// A generic, spreadsheet-shaped view over internal/dashboard/tables.go's
// registry: every column that registry marks editable becomes an input
// here, every PK/reference column renders read-only. Nothing about which
// columns are which is decided in this file -- it all comes from
// /api/debug/tables, so this stays correct if that registry ever changes.

let debugTableDefs = [];
let debugCurrentTable = null;
let debugTableEditingCell = null; // set while a cell has focus, so a live
// poll never overwrites input the operator has not finished typing.
let debugTablePreviousByKey = new Map();
let debugTablesTimer = null;

function debugTableDef(name) {
  return debugTableDefs.find((t) => t.table === name);
}

function debugRowPK(def, row) {
  const pk = {};
  def.columns.forEach((c, i) => {
    if (c.pk) pk[c.name] = row[i];
  });
  return pk;
}

function debugRowKey(def, row) {
  return JSON.stringify(debugRowPK(def, row));
}

function debugCellKey(pk, colName) {
  return JSON.stringify(pk) + "|" + colName;
}

async function loadDebugTableList() {
  try {
    const { available, tables } = await api("/api/debug/tables");
    debugTableDefs = tables;
    $("#debug-tables-unavailable").hidden = available;
    $("#debug-table-select").disabled = !available;
    if (!available) return;

    $("#debug-table-select").replaceChildren(
      ...tables.map((t) => {
        const opt = document.createElement("option");
        opt.value = t.table;
        opt.textContent = t.table;
        return opt;
      })
    );
    debugCurrentTable = tables[0]?.table ?? null;
    if (debugCurrentTable) await loadDebugTableRows();
  } catch (e) {
    $("#debug-tables-unavailable").hidden = false;
    $("#debug-tables-unavailable").textContent = "Could not load the table list: " + e.message;
  }
}

async function saveDebugCell(def, col, pk, value, td, input) {
  const fields = { [col.name]: col.nullable && value === "" ? null : value };
  try {
    await api(`/api/debug/tables/${encodeURIComponent(def.table)}`, {
      method: "PATCH",
      body: JSON.stringify({ pk, fields }),
    });
    if (input) {
      input.dataset.original = value;
      input.classList.remove("dirty", "save-error");
    }
    td.closest("tr")?.classList.remove("just-changed");
  } catch (e) {
    if (input) input.classList.add("save-error");
    alert(`Could not save ${col.name}: ${e.message}`);
  }
}

function makeDebugCell(def, col, row, pk) {
  const td = document.createElement("td");
  const value = row[def.columns.indexOf(col)];

  if (!col.editable) {
    const span = document.createElement("span");
    span.className = "debug-cell readonly";
    span.textContent = value ?? "";
    if (value) span.title = value;
    td.appendChild(span);
    return td;
  }

  if (col.kind === "boolean") {
    const wrap = document.createElement("span");
    wrap.className = "debug-cell-bool";
    const input = document.createElement("input");
    input.type = "checkbox";
    input.checked = value === "true";
    input.addEventListener("change", () =>
      saveDebugCell(def, col, pk, input.checked ? "true" : "false", td)
    );
    wrap.appendChild(input);
    td.appendChild(wrap);
    return td;
  }

  // A single-line input for everything except JSON: a textarea's own line
  // wrapping fights the grid's fixed row height, and every other kind here
  // (text, numbers, timestamps, Postgres's own '{a,b,c}' array literal) is
  // naturally one line. JSON is the one kind worth more room to read and edit.
  const input = document.createElement(col.kind === "jsonb" ? "textarea" : "input");
  if (input.tagName === "TEXTAREA") input.rows = 1;
  else input.type = "text";
  input.className = "debug-cell";
  input.value = value ?? "";
  input.dataset.original = value ?? "";
  const key = debugCellKey(pk, col.name);

  input.addEventListener("focus", () => (debugTableEditingCell = key));
  input.addEventListener("blur", () => {
    if (debugTableEditingCell === key) debugTableEditingCell = null;
    if (input.value !== input.dataset.original) saveDebugCell(def, col, pk, input.value, td, input);
  });
  input.addEventListener("input", () => {
    input.classList.toggle("dirty", input.value !== input.dataset.original);
    input.classList.remove("save-error");
  });
  input.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      input.blur();
    } else if (e.key === "Escape") {
      input.value = input.dataset.original;
      input.classList.remove("dirty");
      input.blur();
    }
  });

  td.appendChild(input);
  return td;
}

function renderDebugTableHead(def) {
  const tr = document.createElement("tr");
  tr.append(
    ...def.columns.map((c) => {
      const th = document.createElement("th");
      th.textContent = c.name;
      if (c.pk) th.title = "primary key";
      return th;
    })
  );
  $("#debug-table-grid thead").replaceChildren(tr);
}

function renderDebugTableBody(def, rows) {
  const tbody = $("#debug-table-grid tbody");
  if (!rows.length) {
    const tr = document.createElement("tr");
    tr.innerHTML = `<td colspan="${def.columns.length}" class="empty">No rows.</td>`;
    tbody.replaceChildren(tr);
    return;
  }

  tbody.replaceChildren(
    ...rows.map((row) => {
      const pk = debugRowPK(def, row);
      const key = debugRowKey(def, row);
      const tr = document.createElement("tr");
      tr.append(...def.columns.map((c) => makeDebugCell(def, c, row, pk)));

      const prev = debugTablePreviousByKey.get(key);
      if (prev !== undefined && prev !== JSON.stringify(row)) {
        tr.classList.add("just-changed");
      }
      return tr;
    })
  );
}

async function loadDebugTableRows() {
  if (!debugCurrentTable || debugTableEditingCell) return;
  const def = debugTableDef(debugCurrentTable);
  if (!def) return;

  try {
    const { rows } = await api(`/api/debug/tables/${encodeURIComponent(debugCurrentTable)}`);
    renderDebugTableHead(def);
    renderDebugTableBody(def, rows);
    debugTablePreviousByKey = new Map(rows.map((row) => [debugRowKey(def, row), JSON.stringify(row)]));
  } catch (e) {
    $("#debug-table-grid tbody").innerHTML =
      `<tr><td colspan="99" class="empty">Could not load ${escape(debugCurrentTable)}: ${escape(e.message)}</td></tr>`;
  }
}

function startDebugTablesLive() {
  stopDebugTablesLive();
  debugTablesTimer = setInterval(loadDebugTableRows, 4000);
}
function stopDebugTablesLive() {
  if (debugTablesTimer) clearInterval(debugTablesTimer);
  debugTablesTimer = null;
}

$("#debug-table-select").addEventListener("change", (e) => {
  debugCurrentTable = e.target.value;
  debugTablePreviousByKey = new Map();
  loadDebugTableRows();
});
$("#debug-tables-refresh").addEventListener("click", loadDebugTableRows);
$("#debug-tables-live").addEventListener("change", (e) => {
  if (e.target.checked) startDebugTablesLive();
  else stopDebugTablesLive();
});

loadDebugTableList();
if ($("#debug-tables-live").checked) startDebugTablesLive();

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
    const [o, ps, fs, cs, ms] = await Promise.all([
      api("/api/overview"),
      api("/api/players"),
      api("/api/fests"),
      api("/api/coop-shifts"),
      api("/api/matches"),
    ]);
    players = ps;
    matches = ms;
    renderOverview(o);
    renderPlayers();
    renderFests(fs);
    renderCoopShifts(cs);
    // After players, so a match's host/players can be shown by nickname
    // rather than by id.
    renderMatches();
    await loadMatchmaking();
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

// A default shift window: starting two hours from now (the next slot),
// running eight hours -- a real rotation's typical length -- rather than
// leaving the operator to work out two aligned timestamps from empty fields.
(function seedCoopWindow() {
  const pad = (n) => String(n).padStart(2, "0");
  const toLocal = (d) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  const start = new Date();
  start.setMinutes(0, 0, 0);
  start.setHours(start.getHours() - (start.getHours() % 2) + 2);
  const end = new Date(start.getTime() + 8 * 60 * 60 * 1000);
  $("#coop-start").value = toLocal(start);
  $("#coop-end").value = toLocal(end);
  $("#coop-gen-from").value = toLocal(start);
})();

loadCoopReference();
refresh();

// Health is loaded SEPARATELY from refresh(), and never inside its try.
//
// refresh() reads players and the overview, both of which fail when the
// database is down -- which is exactly when the health page is the thing being
// looked at. Chaining health onto the end of it meant the one view that could
// explain the outage was the one that rendered empty during it.
loadHealth();
setInterval(loadHealth, 15000);

// Same reasoning as loadHealth above, and the same-slow cadence as the rest
// of refresh(): stats are a sanity check, not a live view, and nothing here
// needs to update between one poll and the next.
loadDebugStats();
setInterval(loadDebugStats, 30000);

// Same slow, low-priority cadence as loadDebugStats -- this is a viewer,
// not a control, so nothing here needs to update between polls.
loadScheduleViewer();
setInterval(loadScheduleViewer, 30000);

connect();
// Presence arrives on the stream; this catches everything else -- a fest
// crossing into its next phase, an account created elsewhere.
setInterval(refresh, 30000);
