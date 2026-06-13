// Global variables
let userID = "";
let socket = null;
let heartbeatInterval = null;
const processedEvents = new Set(); // For event deduplication (At-Least-Once delivery mitigation)
let trackTimer = null;
let currentTrackSeconds = 0;
let currentTrackDuration = 0;
let currentZone = "default"; // Zona activa seleccionada por el usuario

// DOM Elements
const authOverlay = document.getElementById("auth-overlay");
const authForm = document.getElementById("auth-form");
const usernameInput = document.getElementById("username-input");
const currentUserBadge = document.getElementById("current-user-badge");
const statusLed = document.getElementById("status-led");
const statusText = document.getElementById("status-text");

const trackTitle = document.getElementById("track-title");
const trackUser = document.getElementById("track-user");
const trackThumbnail = document.getElementById("track-thumbnail");
const waveform = document.getElementById("waveform");
const artGlow = document.getElementById("art-glow");

const currentTimeEl = document.getElementById("current-time");
const totalTimeEl = document.getElementById("total-time");
const progressBar = document.getElementById("progress-bar");

const volumeSlider = document.getElementById("volume-slider");
const volumeValue = document.getElementById("volume-value");

const addTrackForm = document.getElementById("add-track-form");
const trackUrlInput = document.getElementById("track-url");

const activeUsersList = document.getElementById("active-users-list");
const queueTracksList = document.getElementById("queue-tracks-list");
const queueSizeLabel = document.getElementById("queue-size");
const skipVotesCount = document.getElementById("skip-votes-count");
const zoneSelector = document.getElementById("zone-selector");

// Buttons
const btnClear = document.getElementById("btn-clear");
const btnPause = document.getElementById("btn-pause");
const btnResume = document.getElementById("btn-resume");
const btnSkip = document.getElementById("btn-skip");

// Initialize application on load
window.addEventListener("DOMContentLoaded", async () => {
    // Check if user is already authenticated
    const savedUser = localStorage.getItem("jukebox_user");
    const savedZone = localStorage.getItem("jukebox_zone");

    // Load available zones from the server before anything else
    await loadZonesFromServer();

    // Restore previously selected zone if it still exists
    if (savedZone && document.querySelector(`#zone-selector option[value="${savedZone}"]`)) {
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

// Auth form submission handler
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

// Fetch available zones from the server and populate the zone selector
async function loadZonesFromServer() {
    try {
        const res = await fetch("/api/zones");
        if (res.ok) {
            const zones = await res.json();
            zoneSelector.innerHTML = ""; // Clear loading placeholder

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
                // Show zone id and state for visibility, e.g. "kitchen (playing)"
                opt.textContent = `${zone.id} (${zone.state})`;
                zoneSelector.appendChild(opt);
            });

            // If current zone no longer exists on server, reset to "default"
            const availableIDs = zones.map((z) => z.id);
            if (!availableIDs.includes(currentZone)) {
                currentZone = availableIDs.includes("default") ? "default" : availableIDs[0];
                zoneSelector.value = currentZone;
            }
        } else {
            slog("error", "Failed to fetch zones from server", res.status);
        }
    } catch (err) {
        slog("error", "Error fetching zones", err);
    }
}

// Handle zone selector change: update current zone and reload UI data
zoneSelector.addEventListener("change", (e) => {
    currentZone = e.target.value;
    localStorage.setItem("jukebox_zone", currentZone);
    slog("info", "Zone changed to: " + currentZone);

    // Reload queue and users for the new zone
    loadQueueAndUsersFromServer();
});

// Establish WebSocket connection with auto-reconnect
function connectWebSocket() {
    if (socket) {
        socket.close();
    }

    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const host = window.location.host || "localhost:8080";
    const wsURL = `${protocol}//${host}/api/ws?user_id=${encodeURIComponent(userID)}`;

    statusText.textContent = "Conectando...";
    statusLed.className = "status-led"; // resets color

    socket = new WebSocket(wsURL);

    socket.onopen = () => {
        slog("info", "WebSocket connection established");
        statusText.textContent = "Conectado";
        statusLed.classList.add("online");

        // Start heartbeat ping sweep
        startHeartbeat();
    };

    socket.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);
            handleWebSocketMessage(data);
        } catch (err) {
            slog("error", "Failed to parse WebSocket message", err);
        }
    };

    socket.onclose = () => {
        slog("warn", "WebSocket connection closed");
        statusText.textContent = "Desconectado";
        statusLed.className = "status-led";
        stopHeartbeat();

        // Attempt to reconnect in 3 seconds
        setTimeout(connectWebSocket, 3000);
    };

    socket.onerror = (err) => {
        slog("error", "WebSocket error occurred", err);
    };
}

// Heartbeat ping interval
function startHeartbeat() {
    stopHeartbeat();
    heartbeatInterval = setInterval(() => {
        if (socket && socket.readyState === WebSocket.OPEN) {
            socket.send("ping");
        }
    }, 5000); // Send heartbeat ping every 5 seconds
}

function stopHeartbeat() {
    if (heartbeatInterval) {
        clearInterval(heartbeatInterval);
        heartbeatInterval = null;
    }
}

// WebSocket message router with zone-aware filtering and event deduplicator
function handleWebSocketMessage(data) {
    // Deduplicate event IDs to handle At-Least-Once delivery safely
    if (data.event_id) {
        if (processedEvents.has(data.event_id)) {
            slog("debug", "Duplicate event discarded: " + data.event_id);
            return;
        }
        processedEvents.add(data.event_id);
        // Cap deduplication queue size to 100 entries to prevent memory growth
        if (processedEvents.size > 100) {
            const firstKey = processedEvents.values().next().value;
            processedEvents.delete(firstKey);
        }
    }

    // Zone-aware filtering: only process events for the currently selected zone
    const eventZone = data.zone_id || "default";
    const isCurrentZone = eventZone === currentZone;

    if (!isCurrentZone) {
        slog("debug", `WebSocket event for zone '${eventZone}' discarded (current: '${currentZone}')`);
        // Optional: Update a secondary background indicator for other zones
        // (e.g., show a small notification badge or update a status bar)
        return;
    }

    slog("info", "WebSocket Event: " + data.event, data);

    switch (data.event) {
        case "state_change":
            updatePlayerState(data.new_state, data.current_track);
            syncVolume(data.volume);
            loadQueueAndUsersFromServer(); // Reload lists for current zone
            break;
        case "volume_change":
            syncVolume(data.volume);
            break;
        default:
            slog("warn", "Unknown event type received: " + data.event);
    }
}

// Pull dynamic state list updates
async function fetchQueueAndUsers() {
    try {
        // Since FSM state updates are pushed over WS, we can trigger active list queries.
        // For security and cleanliness, the queue and active user lists are read directly from endpoints.
        // We will fetch lists by requesting our state endpoints.
        // If we don't have dedicated endpoints, we can request FSM summaries,
        // or let's build them by querying local endpoints if the router has them.
        // Wait, did we implement GET /api/queue in router.go?
        // Let's check router.go: we only exposed POST endpoints and stream proxy.
        // Oh! How can we query the queue if router.go doesn't have queue endpoints?
        // Wait! We can add GET endpoints for `/api/queue` and `/api/users` in `router.go` to support loading the queue lists!
        // Yes, that is a missing part of our API router that we need for the UI to list the tracks and users!
        // Let's see: we should make sure our router supports GET `/api/state` or GET `/api/queue` and GET `/api/users`.
        // Let's plan to make a fetch query, or if we fetch, we can read the FSM state.
        // Wait, does `/api/ws` upgrade handshake state change push everything?
        // Yes, when a connection upgrades or when state changes, it pushes `state_change` with `current_track` and `volume`.
        // But what about the queue list? It doesn't push the entire queue in the StateChange event, or does it?
        // Wait! Let's check FSMObserver OnStateChange implementation in main.go:
        // ```go
        // 	msg, err := json.Marshal(map[string]interface{}{
        // 		"event":         "state_change",
        // 		"old_state":     oldState.String(),
        // 		"new_state":     newState.String(),
        // 		"current_track": currentTrack,
        // 		"volume":        volume,
        // 	})
        // ```
        // It pushes current_track, but it does NOT push the entire queue list or active users!
        // To show the queue list and active users, the client needs endpoints!
        // Let's verify if we can add endpoints `GET /api/queue` and `GET /api/users` to `internal/api/router.go`.
        // Yes, that is an excellent API refinement that will allow the JS client to load the list of tracks in the queue and active users!
        // Let's write them down. We will implement these endpoints.
        // Wait, let's look at how the JS client will fetch them:
        // `/api/queue` -> returns a list of tracks in the queue.
        // `/api/users` -> returns a list of active users and skip votes.
        
    } catch (err) {
        slog("error", "Failed to fetch lists", err);
    }
}

// Real Fetch API implementers
async function loadQueueAndUsersFromServer() {
    try {
        // Fetch queue tracks for the current zone
        const qRes = await fetch(`/api/queue?zone_id=${encodeURIComponent(currentZone)}`);
        if (qRes.ok) {
            const queue = await qRes.json();
            renderQueue(queue);
        }

        // Fetch users & votes for the current zone
        const uRes = await fetch(`/api/users?zone_id=${encodeURIComponent(currentZone)}`);
        if (uRes.ok) {
            const data = await uRes.json();
            renderUsersAndVotes(data.users, data.votes, data.karma);
        }
    } catch (err) {
        slog("error", "Error loading queue/users", err);
    }
}

// Override fetchQueueAndUsers to call our implementation
fetchQueueAndUsers = loadQueueAndUsersFromServer;

// Render queue list dynamically
function renderQueue(queue) {
    queueTracksList.innerHTML = "";
    queueSizeLabel.textContent = `${queue.length} pistas`;

    if (!queue || queue.length === 0) {
        queueTracksList.innerHTML = '<div class="no-data">La cola está vacía</div>';
        return;
    }

    queue.forEach((track, index) => {
        const item = document.createElement("div");
        item.className = "queue-item";

        const thumb = track.meta.thumbnail || "https://images.unsplash.com/photo-1614613535308-eb5fbd3d2c17?q=80&w=150&auto=format&fit=crop";
        const title = track.meta.title || track.url;
        const user = track.user_id || "Sistema";

        item.innerHTML = `
            <div class="q-index">${index + 1}</div>
            <img src="${thumb}" alt="Thumb" class="q-art">
            <div class="q-details">
                <div class="q-title" title="${title}">${title}</div>
                <div class="q-added">Agregado por @${user}</div>
            </div>
            <div class="q-duration">${formatDuration(track.dur)}</div>
        `;
        queueTracksList.appendChild(item);
    });
}

// Render active users and their skip votes
function renderUsersAndVotes(users, votes, karma) {
    activeUsersList.innerHTML = "";
    const activeUserCount = Object.keys(users).length;

    if (activeUserCount === 0) {
        activeUsersList.innerHTML = '<div class="no-data">No hay usuarios activos</div>';
        return;
    }

    let voteCount = 0;

    for (const username in users) {
        const hasVoted = votes[username] === true;
        if (hasVoted) voteCount++;

        const badge = document.createElement("div");
        badge.className = "active-user-badge";
        if (hasVoted) {
            badge.style.borderColor = "rgba(139, 92, 246, 0.4)";
            badge.style.color = "#a78bfa";
        }

        const score = karma[username] !== undefined ? karma[username].toFixed(1) : "0.0";
        badge.innerHTML = `@${username} <span style="font-size:10px; opacity:0.6; margin-left:4px;">(${score}⭐)</span> ${hasVoted ? '🗳️' : ''}`;
        activeUsersList.appendChild(badge);
    }

    skipVotesCount.textContent = voteCount;
}

// Update the Now Playing interface card
function updatePlayerState(state, track) {
    if (state === "playing" || state === "paused") {
        waveform.classList.add("active");
        artGlow.style.opacity = "0.35";
    } else {
        waveform.classList.remove("active");
        artGlow.style.opacity = "0.1";
    }

    if (!track) {
        trackTitle.textContent = "Ninguna pista activa";
        trackUser.textContent = "Agrega una canción a la cola para iniciar la música";
        trackThumbnail.src = "https://images.unsplash.com/photo-1614613535308-eb5fbd3d2c17?q=80&w=400&auto=format&fit=crop";
        
        currentTimeEl.textContent = "0:00";
        totalTimeEl.textContent = "0:00";
        progressBar.style.width = "0%";
        stopTrackTimer();
        return;
    }

    trackTitle.textContent = track.meta.title || "Pista sin título";
    trackUser.textContent = `Agregado por @${track.user_id || "Sistema"}`;
    if (track.meta.thumbnail) {
        trackThumbnail.src = track.meta.thumbnail;
    } else {
        trackThumbnail.src = "https://images.unsplash.com/photo-1614613535308-eb5fbd3d2c17?q=80&w=400&auto=format&fit=crop";
    }

    currentTrackDuration = track.dur || 0;
    totalTimeEl.textContent = formatDuration(currentTrackDuration);

    if (state === "playing") {
        startTrackTimer();
    } else {
        stopTrackTimer();
    }
}

// Sync volume inputs
function syncVolume(vol) {
    volumeSlider.value = vol;
    volumeValue.textContent = `${vol}%`;
}

// Mock dynamic progress timer
function startTrackTimer() {
    stopTrackTimer();
    trackTimer = setInterval(() => {
        if (currentTrackDuration <= 0) return;
        currentTrackSeconds++;
        if (currentTrackSeconds > currentTrackDuration) {
            currentTrackSeconds = currentTrackDuration;
        }

        currentTimeEl.textContent = formatDuration(currentTrackSeconds);
        const percent = (currentTrackSeconds / currentTrackDuration) * 100;
        progressBar.style.width = `${percent}%`;
    }, 1000);
}

function stopTrackTimer() {
    if (trackTimer) {
        clearInterval(trackTimer);
        trackTimer = null;
    }
    // Only reset progress seconds if we are not pausing
}

// Format seconds into m:ss format
function formatDuration(sec) {
    if (!sec || isNaN(sec)) return "0:00";
    const m = Math.floor(sec / 60);
    const s = Math.floor(sec % 60);
    return `${m}:${s < 10 ? '0' : ''}${s}`;
}

// REST Control Triggers
addTrackForm.addEventListener("submit", async (e) => {
    e.preventDefault();
    const urlVal = trackUrlInput.value.trim();
    if (!urlVal) return;

    trackUrlInput.disabled = true;
    trackUrlInput.placeholder = "Encolando...";

    try {
        const res = await fetch("/api/tracks", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ url: urlVal, user_id: userID, zone_id: currentZone })
        });
        if (res.ok) {
            trackUrlInput.value = "";
            slog("info", "Track queued successfully via API");
        } else {
            const err = await res.text();
            alert("Error al encolar pista: " + err);
        }
    } catch (err) {
        slog("error", "Queue request failed", err);
    } finally {
        trackUrlInput.disabled = false;
        trackUrlInput.placeholder = "URL de YouTube...";
    }
});

btnSkip.addEventListener("click", async () => {
    try {
        await fetch("/api/skip", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ user_id: userID, zone_id: currentZone })
        });
    } catch (err) {
        slog("error", "Skip request failed", err);
    }
});

btnPause.addEventListener("click", async () => {
    try {
        await fetch("/api/pause", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ zone_id: currentZone })
        });
    } catch (err) {
        slog("error", "Pause request failed", err);
    }
});

// Since we will modify router.go to add queue, users, pause, and resume endpoints, let's prepare the HTTP fetch requests:
async function triggerPause() {
    await fetch("/api/pause", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ zone_id: currentZone })
    });
}

async function triggerResume() {
    await fetch("/api/resume", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ zone_id: currentZone })
    });
}

async function triggerClear() {
    await fetch("/api/clear", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ zone_id: currentZone })
    });
}

btnPause.onclick = triggerPause;
btnResume.onclick = triggerResume;
btnClear.onclick = triggerClear;

volumeSlider.addEventListener("input", async (e) => {
    const vol = parseInt(e.target.value, 10);
    volumeValue.textContent = `${vol}%`;
});

// Debounce volume slider API requests
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
            slog("error", "Volume change request failed", err);
        }
    }, 150);
});

// Logger helper
function slog(level, msg, detail = "") {
    const ts = new Date().toISOString();
    console.log(`[${ts}] [${level.toUpperCase()}] ${msg}`, detail);
}
