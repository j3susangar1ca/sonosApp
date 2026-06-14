// ============================================================
// Jukebox — Frontend Controller
// Connected to Go backend via WebSocket + REST API
// ============================================================

// State
let userID = "";
let socket = null;
let heartbeatInterval = null;
let trackTimer = null;
let currentTrackSeconds = 0;
let currentTrackDuration = 0;
let currentZone = "default";
const processedEvents = new Set();

// DOM
const authOverlay = document.getElementById("auth-overlay");
const authForm = document.getElementById("auth-form");
const usernameInput = document.getElementById("username-input");
const currentUserBadge = document.getElementById("current-user-badge");
const statusLed = document.getElementById("status-led");

const trackTitle = document.getElementById("track-title");
const trackUser = document.getElementById("track-user");
const trackThumbnail = document.getElementById("track-thumbnail");
const npPlaceholder = document.getElementById("np-placeholder");
const npEq = document.getElementById("np-eq");

const currentTimeEl = document.getElementById("current-time");
const totalTimeEl = document.getElementById("total-time");
const progressBar = document.getElementById("progress-bar");

const volumeSlider = document.getElementById("volume-slider");
const volumeValue = document.getElementById("volume-value");

const addTrackForm = document.getElementById("add-track-form");
const trackUrlInput = document.getElementById("track-url");
const btnEnqueue = document.getElementById("btn-enqueue");
const btnPlayNow = document.getElementById("btn-play-now");

const queueTracksList = document.getElementById("queue-tracks-list");
const queueSizeLabel = document.getElementById("queue-size");

const btnClear = document.getElementById("btn-clear");
const btnPause = document.getElementById("btn-pause");
const btnResume = document.getElementById("btn-resume");
const btnSkip = document.getElementById("btn-skip");
const zoneSelector = document.getElementById("zone-selector");

// ====== Init ======
window.addEventListener("DOMContentLoaded", async () => {
    const savedUser = localStorage.getItem("jukebox_user");
    const savedZone = localStorage.getItem("jukebox_zone");

    await loadZonesFromServer();

    if (savedZone && zoneSelector.querySelector(`option[value="${savedZone}"]`)) {
        currentZone = savedZone;
        zoneSelector.value = currentZone;
    }

    if (savedUser) {
        userID = savedUser;
        currentUserBadge.textContent = userID;
        connectWebSocket();
    } else {
        authOverlay.classList.add("active");
    }
});

// ====== Auth ======
authForm.addEventListener("submit", (e) => {
    e.preventDefault();
    const rawUser = usernameInput.value.trim().toLowerCase();
    if (!rawUser) return;
    userID = rawUser;
    localStorage.setItem("jukebox_user", userID);
    currentUserBadge.textContent = userID;
    authOverlay.classList.remove("active");
    connectWebSocket();
});

// ====== Zones ======
async function loadZonesFromServer() {
    try {
        const res = await fetch("/api/zones");
        if (res.ok) {
            const zones = await res.json();
            zoneSelector.innerHTML = "";
            if (!zones || zones.length === 0) {
                const opt = document.createElement("option");
                opt.value = "default";
                opt.textContent = "Zona por defecto";
                zoneSelector.appendChild(opt);
                return;
            }
            zones.forEach((zone) => {
                const opt = document.createElement("option");
                opt.value = zone.id;
                opt.textContent = `${zone.id} (${zone.state})`;
                zoneSelector.appendChild(opt);
            });
            const availableIDs = zones.map(z => z.id);
            if (!availableIDs.includes(currentZone)) {
                currentZone = availableIDs.includes("default") ? "default" : availableIDs[0];
                zoneSelector.value = currentZone;
            }
        }
    } catch (err) {
        log("error", "Error fetching zones", err);
    }
}

zoneSelector.addEventListener("change", (e) => {
    currentZone = e.target.value;
    localStorage.setItem("jukebox_zone", currentZone);
    loadQueueFromServer();
});

// ====== WebSocket ======
function connectWebSocket() {
    if (socket) socket.close();
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const host = window.location.host || "localhost:8080";
    const wsURL = `${protocol}//${host}/api/ws?user_id=${encodeURIComponent(userID)}`;
    socket = new WebSocket(wsURL);

    socket.onopen = () => {
        statusLed.classList.add("online");
        startHeartbeat();
        loadQueueFromServer();
    };

    socket.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);
            handleWSMessage(data);
        } catch (err) {
            log("error", "WS parse error", err);
        }
    };

    socket.onclose = () => {
        statusLed.classList.remove("online");
        stopHeartbeat();
        setTimeout(connectWebSocket, 3000);
    };

    socket.onerror = () => {};
}

function startHeartbeat() {
    stopHeartbeat();
    heartbeatInterval = setInterval(() => {
        if (socket && socket.readyState === WebSocket.OPEN) socket.send("ping");
    }, 5000);
}

function stopHeartbeat() {
    if (heartbeatInterval) {
        clearInterval(heartbeatInterval);
        heartbeatInterval = null;
    }
}

function handleWSMessage(data) {
    if (data.event_id) {
        if (processedEvents.has(data.event_id)) return;
        processedEvents.add(data.event_id);
        if (processedEvents.size > 100) {
            const firstKey = processedEvents.values().next().value;
            processedEvents.delete(firstKey);
        }
    }

    const eventZone = data.zone_id || "default";
    if (eventZone !== currentZone) return;

    switch (data.event) {
        case "state_change":
            updatePlayerUI(data.new_state, data.current_track);
            syncVolume(data.volume);
            loadQueueFromServer();
            break;
        case "volume_change":
            syncVolume(data.volume);
            break;
    }
}

// ====== Player UI ======
function updatePlayerUI(state, track) {
    if (state === "playing" || state === "paused") {
        npEq.classList.add("active");
    } else {
        npEq.classList.remove("active");
    }

    if (!track) {
        trackTitle.textContent = "Sin reproducción";
        trackUser.textContent = "Agrega una canción para comenzar";
        trackThumbnail.classList.remove("loaded");
        npPlaceholder.classList.remove("hidden");
        currentTimeEl.textContent = "0:00";
        totalTimeEl.textContent = "0:00";
        progressBar.style.width = "0%";
        stopTrackTimer();
        return;
    }

    trackTitle.textContent = track.meta.title || "Sin título";
    trackUser.textContent = `Agregado por @${track.user_id || "Sistema"}`;

    if (track.meta.thumbnail) {
        trackThumbnail.src = track.meta.thumbnail;
        trackThumbnail.onload = () => {
            trackThumbnail.classList.add("loaded");
            npPlaceholder.classList.add("hidden");
        };
    } else {
        trackThumbnail.classList.remove("loaded");
        npPlaceholder.classList.remove("hidden");
    }

    currentTrackDuration = track.dur || 0;
    totalTimeEl.textContent = formatDuration(currentTrackDuration);

    if (state === "playing") {
        startTrackTimer();
    } else {
        stopTrackTimer();
    }
}

function syncVolume(vol) {
    volumeSlider.value = vol;
    volumeValue.textContent = vol;
}

function startTrackTimer() {
    stopTrackTimer();
    trackTimer = setInterval(() => {
        if (currentTrackDuration <= 0) return;
        currentTrackSeconds++;
        if (currentTrackSeconds > currentTrackDuration) currentTrackSeconds = currentTrackDuration;
        currentTimeEl.textContent = formatDuration(currentTrackSeconds);
        const pct = (currentTrackSeconds / currentTrackDuration) * 100;
        progressBar.style.width = `${pct}%`;
    }, 1000);
}

function stopTrackTimer() {
    if (trackTimer) {
        clearInterval(trackTimer);
        trackTimer = null;
    }
}

function formatDuration(sec) {
    if (!sec || isNaN(sec)) return "0:00";
    const m = Math.floor(sec / 60);
    const s = Math.floor(sec % 60);
    return `${m}:${s < 10 ? '0' : ''}${s}`;
}

// ====== Queue ======
async function loadQueueFromServer() {
    try {
        const res = await fetch(`/api/queue?zone_id=${encodeURIComponent(currentZone)}`);
        if (res.ok) {
            const queue = await res.json();
            renderQueue(queue);
        }
    } catch (err) {
        log("error", "Error loading queue", err);
    }
}

function renderQueue(queue) {
    queueTracksList.innerHTML = "";
    queueSizeLabel.textContent = `${queue.length} canción${queue.length !== 1 ? 'es' : ''}`;

    if (!queue || queue.length === 0) {
        queueTracksList.innerHTML = `
            <div class="queue-empty">
                <svg width="40" height="40" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1" stroke-linecap="round" stroke-linejoin="round"><path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/></svg>
                <p>La cola está vacía</p>
                <span>Agrega canciones con un enlace de YouTube</span>
            </div>`;
        return;
    }

    queue.forEach((track, index) => {
        const item = document.createElement("div");
        item.className = "queue-item";
        item.dataset.trackId = track.id;

        const thumb = track.meta.thumbnail || "";
        const title = track.meta.title || track.url || "Sin título";
        const user = track.user_id || "Sistema";

        item.innerHTML = `
            <div class="q-index">${index + 1}</div>
            ${thumb ? `<img class="q-art" src="${escapeAttr(thumb)}" alt="" loading="lazy">` : `<div class="q-art" style="display:flex;align-items:center;justify-content:center;background:var(--bg-hover)"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="var(--text-tertiary)" stroke-width="2"><path d="M9 18V5l12-2v13"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="16" r="3"/></svg></div>`}
            <div class="q-details">
                <div class="q-title" title="${escapeAttr(title)}">${escapeHTML(title)}</div>
                <div class="q-meta">
                    <span>@${escapeHTML(user)}</span>
                </div>
            </div>
            <div class="q-duration">${formatDuration(track.dur)}</div>
            <div class="q-actions">
                <button class="q-action-btn q-play-btn" title="Reproducir" data-track-id="${escapeAttr(track.id)}">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><polygon points="5 3 19 12 5 21 5 3"/></svg>
                </button>
                <button class="q-action-btn q-remove" title="Eliminar" data-track-id="${escapeAttr(track.id)}">
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
                </button>
            </div>
        `;

        queueTracksList.appendChild(item);
    });

    // Bind play-from-queue buttons
    queueTracksList.querySelectorAll(".q-play-btn").forEach(btn => {
        btn.addEventListener("click", (e) => {
            e.stopPropagation();
            const trackId = btn.dataset.trackId;
            playFromQueue(trackId);
        });
    });

    // Bind remove buttons
    queueTracksList.querySelectorAll(".q-remove").forEach(btn => {
        btn.addEventListener("click", (e) => {
            e.stopPropagation();
            const trackId = btn.dataset.trackId;
            removeFromQueue(trackId);
        });
    });
}

// ====== API Calls ======

// Enqueue (add to end of queue)
addTrackForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    const urlVal = trackUrlInput.value.trim();
    if (!urlVal) return;
    await addTrackToQueue(urlVal);
    trackUrlInput.value = "";
});

// Play Now button
btnPlayNow.addEventListener("click", async () => {
    const urlVal = trackUrlInput.value.trim();
    if (!urlVal) return;
    await playNow(urlVal);
    trackUrlInput.value = "";
});

async function addTrackToQueue(url) {
    try {
        trackUrlInput.disabled = true;
        const res = await fetch("/api/tracks", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ url, user_id: userID, zone_id: currentZone })
        });
        if (res.ok) {
            showToast("Canción encolada");
        } else {
            const err = await res.text();
            showToast("Error: " + err);
        }
    } catch (err) {
        showToast("Error de conexión");
    } finally {
        trackUrlInput.disabled = false;
    }
}

async function playNow(url) {
    try {
        trackUrlInput.disabled = true;
        const res = await fetch("/api/tracks/play-now", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ url, user_id: userID, zone_id: currentZone })
        });
        if (res.ok) {
            showToast("Reproduciendo ahora");
        } else {
            const err = await res.text();
            showToast("Error: " + err);
        }
    } catch (err) {
        showToast("Error de conexión");
    } finally {
        trackUrlInput.disabled = false;
    }
}

async function playFromQueue(trackId) {
    try {
        await fetch("/api/queue/play", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ track_id: trackId, user_id: userID, zone_id: currentZone })
        });
        showToast("Reproduciendo");
    } catch (err) {
        showToast("Error de conexión");
    }
}

async function removeFromQueue(trackId) {
    try {
        await fetch("/api/queue/remove", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ track_id: trackId, zone_id: currentZone })
        });
        loadQueueFromServer();
    } catch (err) {
        showToast("Error al eliminar");
    }
}

// Skip
btnSkip.addEventListener("click", async () => {
    try {
        await fetch("/api/skip", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ zone_id: currentZone })
        });
    } catch (err) {
        log("error", "Skip failed", err);
    }
});

// Pause
btnPause.addEventListener("click", async () => {
    try {
        await fetch("/api/pause", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ zone_id: currentZone })
        });
    } catch (err) {
        log("error", "Pause failed", err);
    }
});

// Resume
btnResume.addEventListener("click", async () => {
    try {
        await fetch("/api/resume", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ zone_id: currentZone })
        });
    } catch (err) {
        log("error", "Resume failed", err);
    }
});

// Clear
btnClear.addEventListener("click", async () => {
    try {
        await fetch("/api/clear", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ zone_id: currentZone })
        });
        showToast("Cola limpiada");
    } catch (err) {
        log("error", "Clear failed", err);
    }
});

// Volume
volumeSlider.addEventListener("input", (e) => {
    volumeValue.textContent = e.target.value;
});

let volDebounce = null;
volumeSlider.addEventListener("change", (e) => {
    if (volDebounce) clearTimeout(volDebounce);
    const vol = parseInt(e.target.value, 10);
    volDebounce = setTimeout(async () => {
        try {
            await fetch("/api/volume", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({ level: vol, user_id: userID, zone_id: currentZone })
            });
        } catch (err) {
            log("error", "Volume failed", err);
        }
    }, 150);
});

// ====== Toast ======
let toastTimeout = null;
function showToast(msg) {
    let toast = document.getElementById("toast");
    if (!toast) {
        toast = document.createElement("div");
        toast.id = "toast";
        toast.className = "toast";
        document.body.appendChild(toast);
    }
    toast.textContent = msg;
    toast.classList.remove("show");
    clearTimeout(toastTimeout);
    requestAnimationFrame(() => {
        toast.classList.add("show");
        toastTimeout = setTimeout(() => toast.classList.remove("show"), 2500);
    });
}

// ====== Utilities ======
function escapeHTML(str) {
    const div = document.createElement("div");
    div.textContent = str;
    return div.innerHTML;
}

function escapeAttr(str) {
    return str.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/'/g, '&#39;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

function log(level, msg, detail = "") {
    const ts = new Date().toISOString();
    console.log(`[${ts}] [${level.toUpperCase()}] ${msg}`, detail);
}
