/**
 * Office Jukebox - Collaborative Music Queue Controller
 * Apple Design Language Implementation
 */

// ── STATE MANAGEMENT ───────────────────────────────────────────
const state = {
    username: null,
    connected: false,
    currentTrack: null,
    queue: [],
    users: [],
    isPlaying: false,
    volume: 15,
    progress: 0,
    duration: 0,
    currentTime: 0,
    skipVotes: 0,
    zone: 'default'
};

// ── DOM ELEMENTS ───────────────────────────────────────────────
const elements = {
    // Auth
    authOverlay: document.getElementById('auth-overlay'),
    authForm: document.getElementById('auth-form'),
    usernameInput: document.getElementById('username-input'),
    
    // Header
    zoneSelector: document.getElementById('zone-selector'),
    currentUserBadge: document.getElementById('current-user-badge'),
    statusLed: document.getElementById('status-led'),
    statusText: document.getElementById('status-text'),
    
    // Player
    trackThumbnail: document.getElementById('track-thumbnail'),
    trackTitle: document.getElementById('track-title'),
    trackUser: document.getElementById('track-user'),
    progressBar: document.getElementById('progress-bar'),
    currentTime: document.getElementById('current-time'),
    totalTime: document.getElementById('total-time'),
    waveform: document.getElementById('waveform'),
    artGlow: document.getElementById('art-glow'),
    npDot: document.getElementById('np-dot'),
    volumeSlider: document.getElementById('volume-slider'),
    volumeValue: document.getElementById('volume-value'),
    skipVotesCount: document.getElementById('skip-votes-count'),
    
    // Controls
    btnPause: document.getElementById('btn-pause'),
    btnResume: document.getElementById('btn-resume'),
    btnSkip: document.getElementById('btn-skip'),
    btnClear: document.getElementById('btn-clear'),
    
    // Add Track
    addTrackForm: document.getElementById('add-track-form'),
    trackUrl: document.getElementById('track-url'),
    
    // Lists
    activeUsersList: document.getElementById('active-users-list'),
    queueTracksList: document.getElementById('queue-tracks-list'),
    queueSize: document.getElementById('queue-size')
};

// ── AUTHENTICATION ─────────────────────────────────────────────
elements.authForm.addEventListener('submit', (e) => {
    e.preventDefault();
    const username = elements.usernameInput.value.trim();
    if (username) {
        state.username = username;
        state.connected = true;
        elements.currentUserBadge.textContent = username;
        elements.authOverlay.classList.remove('active');
        updateConnectionStatus(true);
        initializeDemoData();
    }
});

function updateConnectionStatus(connected) {
    if (connected) {
        elements.statusLed.classList.add('online');
        elements.statusText.textContent = 'Conectado';
    } else {
        elements.statusLed.classList.remove('online');
        elements.statusText.textContent = 'Desconectado';
    }
}

// ── DEMO DATA (for visualization purposes) ─────────────────────
function initializeDemoData() {
    // Demo users
    state.users = [
        { name: 'Ana García', tracksAdded: 3 },
        { name: 'Carlos M.', tracksAdded: 2 },
        { name: 'Lucía R.', tracksAdded: 5 },
        { name: 'David P.', tracksAdded: 1 }
    ];
    
    // Demo queue
    state.queue = [
        {
            id: 1,
            title: 'Bohemian Rhapsody',
            artist: 'Queen',
            thumbnail: 'https://images.unsplash.com/photo-1614613535308-eb5fbd3d2c17?q=80&w=400&auto=format&fit=crop',
            duration: '5:55',
            addedBy: 'Ana García',
            url: 'https://youtube.com/watch?v=fJ9rUzIMcZQ'
        },
        {
            id: 2,
            title: 'Blinding Lights',
            artist: 'The Weeknd',
            thumbnail: 'https://images.unsplash.com/photo-1619983081563-430dc6b5eb3f?q=80&w=400&auto=format&fit=crop',
            duration: '3:20',
            addedBy: 'Carlos M.',
            url: 'https://youtube.com/watch?v=4NRXx6U8ABQ'
        },
        {
            id: 3,
            title: 'Levitating',
            artist: 'Dua Lipa',
            thumbnail: 'https://images.unsplash.com/photo-1493225255756-d9584f8606e9?q=80&w=400&auto=format&fit=crop',
            duration: '3:23',
            addedBy: 'Lucía R.',
            url: 'https://youtube.com/watch?v=TUVcZfQe-Kw'
        }
    ];
    
    // Demo current track
    state.currentTrack = {
        title: 'Midnight City',
        artist: 'M83',
        thumbnail: 'https://images.unsplash.com/photo-1470225620780-dba8ba36b745?q=80&w=400&auto=format&fit=crop',
        duration: 244,
        addedBy: 'Lucía R.'
    };
    
    state.isPlaying = true;
    state.duration = 244;
    state.currentTime = 45;
    
    renderAll();
    startProgressSimulation();
}

// ── RENDERING ──────────────────────────────────────────────────
function renderAll() {
    renderCurrentTrack();
    renderQueue();
    renderUsers();
    renderControls();
}

function renderCurrentTrack() {
    if (state.currentTrack) {
        elements.trackThumbnail.src = state.currentTrack.thumbnail;
        elements.trackTitle.textContent = state.currentTrack.title;
        elements.trackUser.textContent = `Añadida por ${state.currentTrack.addedBy}`;
        
        if (state.isPlaying) {
            elements.waveform.classList.add('active');
            elements.artGlow.style.opacity = '0.35';
        } else {
            elements.waveform.classList.remove('active');
            elements.artGlow.style.opacity = '0.1';
        }
        
        updateTimeDisplay();
    }
}

function renderQueue() {
    elements.queueTracksList.innerHTML = '';
    elements.queueSize.textContent = `${state.queue.length} pista${state.queue.length !== 1 ? 's' : ''}`;
    
    if (state.queue.length === 0) {
        elements.queueTracksList.innerHTML = '<div class="no-data">La cola está vacía</div>';
        return;
    }
    
    state.queue.forEach((track, index) => {
        const item = document.createElement('div');
        item.className = 'queue-item';
        item.innerHTML = `
            <span class="q-index">${index + 1}</span>
            <img src="${track.thumbnail}" alt="${track.title}" class="q-art">
            <div class="q-details">
                <div class="q-title">${track.title}</div>
                <div class="q-added">${track.addedBy}</div>
            </div>
            <span class="q-duration">${track.duration}</span>
        `;
        elements.queueTracksList.appendChild(item);
    });
}

function renderUsers() {
    elements.activeUsersList.innerHTML = '';
    
    if (state.users.length === 0) {
        elements.activeUsersList.innerHTML = '<span class="no-data">Cargando usuarios...</span>';
        return;
    }
    
    state.users.forEach(user => {
        const badge = document.createElement('span');
        badge.className = 'active-user-badge';
        badge.innerHTML = `
            <span>${user.name}</span>
        `;
        elements.activeUsersList.appendChild(badge);
    });
}

function renderControls() {
    elements.skipVotesCount.textContent = state.skipVotes;
    elements.volumeSlider.value = state.volume;
    elements.volumeValue.textContent = `${state.volume}%`;
    
    // Update volume slider gradient
    const pct = state.volume + '%';
    elements.volumeSlider.style.background = `linear-gradient(to right,#007AFF ${pct},rgba(0,0,0,0.08) ${pct})`;
}

function updateTimeDisplay() {
    elements.currentTime.textContent = formatTime(state.currentTime);
    elements.totalTime.textContent = formatTime(state.duration);
    
    const progressPercent = state.duration > 0 ? (state.currentTime / state.duration) * 100 : 0;
    elements.progressBar.style.width = `${progressPercent}%`;
}

function formatTime(seconds) {
    const mins = Math.floor(seconds / 60);
    const secs = Math.floor(seconds % 60);
    return `${mins}:${secs.toString().padStart(2, '0')}`;
}

// ── PROGRESS SIMULATION ────────────────────────────────────────
function startProgressSimulation() {
    setInterval(() => {
        if (state.isPlaying && state.currentTime < state.duration) {
            state.currentTime++;
            updateTimeDisplay();
        }
    }, 1000);
}

// ── EVENT LISTENERS ────────────────────────────────────────────

// Volume control
elements.volumeSlider.addEventListener('input', (e) => {
    state.volume = parseInt(e.target.value, 10);
    elements.volumeValue.textContent = `${state.volume}%`;
    
    const pct = state.volume + '%';
    elements.volumeSlider.style.background = `linear-gradient(to right,#007AFF ${pct},rgba(0,0,0,0.08) ${pct})`;
    
    // In a real app, this would send volume change to server
    console.log(`Volume changed to ${state.volume}%`);
});

// Pause button
elements.btnPause.addEventListener('click', () => {
    state.isPlaying = false;
    renderCurrentTrack();
    console.log('Playback paused');
});

// Resume button
elements.btnResume.addEventListener('click', () => {
    state.isPlaying = true;
    renderCurrentTrack();
    console.log('Playback resumed');
});

// Skip button
elements.btnSkip.addEventListener('click', () => {
    state.skipVotes++;
    elements.skipVotesCount.textContent = state.skipVotes;
    
    // Visual feedback
    elements.btnSkip.style.transform = 'scale(0.95)';
    setTimeout(() => {
        elements.btnSkip.style.transform = '';
    }, 150);
    
    console.log(`Skip vote added. Total votes: ${state.skipVotes}`);
    
    // In a real app, this would send skip vote to server
    if (state.skipVotes >= 3) {
        skipTrack();
    }
});

// Clear queue button
elements.btnClear.addEventListener('click', () => {
    if (confirm('¿Estás seguro de que quieres limpiar toda la cola?')) {
        state.queue = [];
        renderQueue();
        console.log('Queue cleared');
    }
});

// Add track form
elements.addTrackForm.addEventListener('submit', (e) => {
    e.preventDefault();
    const url = elements.trackUrl.value.trim();
    
    if (url && state.username) {
        // In a real app, this would send to server and get track info back
        // For demo, we'll create a mock track
        const newTrack = {
            id: Date.now(),
            title: 'Nueva Canción',
            artist: 'Artista Desconocido',
            thumbnail: 'https://images.unsplash.com/photo-1459749411177-287ce3288b7d?q=80&w=400&auto=format&fit=crop',
            duration: '3:45',
            addedBy: state.username,
            url: url
        };
        
        state.queue.push(newTrack);
        renderQueue();
        elements.trackUrl.value = '';
        
        // Show success feedback
        const btn = elements.addTrackForm.querySelector('.btn-enqueue');
        const originalText = btn.innerHTML;
        btn.innerHTML = '✓ Añadida';
        btn.style.background = 'var(--green)';
        
        setTimeout(() => {
            btn.innerHTML = originalText;
            btn.style.background = '';
        }, 1500);
        
        console.log(`Track added by ${state.username}: ${url}`);
    }
});

function skipTrack() {
    if (state.queue.length > 0) {
        const nextTrack = state.queue.shift();
        state.currentTrack = {
            ...nextTrack,
            duration: 240, // Mock duration
            thumbnail: nextTrack.thumbnail
        };
        state.currentTime = 0;
        state.skipVotes = 0;
        state.isPlaying = true;
        
        renderAll();
        console.log(`Skipped to next track: ${state.currentTrack.title}`);
    }
}

// Zone selector (mock functionality)
elements.zoneSelector.addEventListener('change', (e) => {
    state.zone = e.target.value;
    console.log(`Zone changed to: ${state.zone}`);
});

// ── INITIALIZATION ─────────────────────────────────────────────
document.addEventListener('DOMContentLoaded', () => {
    // Show auth overlay on load
    elements.authOverlay.classList.add('active');
    
    // Load zones (mock)
    loadZones();
    
    console.log('Office Jukebox initialized');
});

function loadZones() {
    // Mock zone loading
    const zones = [
        { id: 'default', name: 'Oficina Principal' },
        { id: 'kitchen', name: 'Cocina' },
        { id: 'meeting', name: 'Sala de Reuniones' }
    ];
    
    elements.zoneSelector.innerHTML = '';
    zones.forEach(zone => {
        const option = document.createElement('option');
        option.value = zone.id;
        option.textContent = zone.name;
        elements.zoneSelector.appendChild(option);
    });
}

// Expose state for debugging (optional)
window.jukeboxState = state;
