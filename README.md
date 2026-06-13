# 🔬 Análisis Matemático-Formal: Sistema "Office Jukebox" para Ecosistema Sonos One SL

---

## 1. Definiciones Fundamentales y Espacios de Estado

### 1.1 Universo del Sistema

Sea $\mathcal{J}$ el sistema cerrado *Office Jukebox*. Se definen los siguientes conjuntos fundamentales que delimitan el dominio del sistema:

$$U = \{u_1, u_2, \dots, u_6\} \quad \text{(Conjunto universal de usuarios, } |U| \leq 6\text{)}$$

$$T = \{t \mid t = (id, src, meta, url, dur, play\_count, skip\_count, \tau_{last})\} \quad \text{(Conjunto de pistas multimedia con historial)}$$

$$S_{src} = \{\text{youtube}, \text{smb\_local}\} \quad \text{(Conjunto de orígenes de contenido)}$$

Donde para cada pista $t \in T$:

- $id(t) \in \Sigma^{*}$: Identificador único del track (cadena de caracteres sobre el alfabeto $\Sigma$)
- $src(t) \in S_{src}$: Función que evalúa el origen del contenido
- $meta(t) \in M$: Metadatos asociados (título, miniatura, duración)
- $url(t) \in \mathcal{U} \cup \{\bot\}$: URL de streaming resuelta ($\bot$ si no resuelta aún)
- $dur(t) \in \mathbb{N}$: Duración en segundos
- $play\_count(t) \in \mathbb{N}_0$: Contador de reproducciones completas
- $skip\_count(t) \in \mathbb{N}_0$: Contador de omisiones por democracia
- $\tau_{last}(t) \in \mathbb{T}$: Marca temporal de última reproducción

### 1.2 Espacios de Estado del Sistema

El estado global del sistema $\sigma \in \Sigma_{global}$ se define como la tupla:

$$\sigma = (q, Q, t_c, v_{level}, U_a, V_s, C_{cb}^{yt}, C_{cb}^{so}, C_{lru}, B_{cmd})$$

Donde:

| Componente | Dominio | Descripción |
|---|---|---|
| $q$ | $\mathcal{S}_{FSM}$ | Estado de la Máquina de Estados Finita |
| $Q$ | $\text{RingBuffer}(T)$ | Cola de reproducción (buffer circular de pistas) |
| $t_c$ | $T \cup \{\bot\}$ | Pista actualmente en reproducción |
| $v_{level}$ | $[0, 100] \cap \mathbb{N}$ | Nivel de volumen del hardware |
| $U_a$ | $\mathcal{P}(U)$ | Subconjunto de usuarios activos conectados |
| $V_s$ | $\mathcal{P}(U_a \times T)$ | Conjunto de votos de skip registrados |
| $C_{cb}^{yt}$ | $\mathcal{S}_{CB}$ | Estado del Circuit Breaker de YouTube |
| $C_{cb}^{so}$ | $\mathcal{S}_{CB}$ | Estado del Circuit Breaker de Sonos |
| $C_{lru}$ | $K \rightharpoonup V$ | Caché LRU con TTL adaptativo (mapeo parcial) |
| $B_{cmd}$ | $\text{FIFO}(\mathcal{E}_{user})$ | Buffer de comandos diferidos durante transiciones |

---

## 2. Máquina de Estados Finita (FSM) del Motor de Reproducción

### 2.1 Modelo Formal

La FSM se define como la quintuple:

$$\mathcal{M} = (\mathcal{S}_{FSM}, \mathcal{E}, \delta, s_0, \mathcal{F})$$

**Conjunto de Estados:**

$$\mathcal{S}_{FSM} = \{s_{idle}, s_{playing}, s_{paused}, s_{transitioning}, s_{autoplay}\}$$

**Conjunto de Eventos Disparadores:**

$$\mathcal{E} = \{e_{add}, e_{play}, e_{pause}, e_{resume}, e_{skip}, e_{ack\_ok}, e_{ack\_fail}, e_{eof}, e_{clear}, e_{queue\_empty}, e_{set\_volume}\}$$

**Estado Inicial:**

$$s_0 = s_{idle}$$

**Conjunto de Estados Finales (no-terminal):**

$$\mathcal{F} = \{s_{idle}\}$$

### 2.2 Función de Transición $\delta$

La función de transición parcial $\delta: \mathcal{S}_{FSM} \times \mathcal{E} \rightharpoonup \mathcal{S}_{FSM}$ se define como:

$$\delta(s_{idle}, e_{add}) = \begin{cases} s_{transitioning} & \text{si } Q \neq \emptyset \\ s_{idle} & \text{si } Q = \emptyset \end{cases}$$

$$\delta(s_{idle}, e_{queue\_empty}) = s_{autoplay} \quad \text{(si historial } H \neq \emptyset \text{)}$$

$$\delta(s_{transitioning}, e_{ack\_ok}) = s_{playing}$$

$$\delta(s_{transitioning}, e_{ack\_fail}) = \begin{cases} s_{transitioning} & \text{si } r_{retry} > 0 \\ s_{idle} & \text{si } r_{retry} = 0 \end{cases}$$

$$\delta(s_{playing}, e_{pause}) = s_{paused}$$

$$\delta(s_{paused}, e_{resume}) = s_{playing}$$

$$\delta(s_{playing}, e_{eof}) = \begin{cases} s_{transitioning} & \text{si } |Q| > 0 \\ s_{autoplay} & \text{si } |Q| = 0 \wedge H \neq \emptyset \\ s_{idle} & \text{si } |Q| = 0 \wedge H = \emptyset \end{cases}$$

$$\delta(s_{playing}, e_{skip}) = s_{transitioning} \quad \text{(requiere } \text{SkipAuth}(V_s, U_a) = 1 \text{)}$$

$$\delta(s_{paused}, e_{skip}) = s_{transitioning} \quad \text{(requiere } \text{SkipAuth}(V_s, U_a) = 1 \text{)}$$

$$\delta(s_{playing}, e_{clear}) = s_{idle}$$

$$\delta(s_{autoplay}, e_{add}) = s_{transitioning}$$

$$\delta(s_{autoplay}, e_{eof}) = s_{autoplay} \quad \text{(selección automática de siguiente pista)}$$

**Protección durante transiciones — Buffer de comandos diferidos:**

$$\forall e \in \mathcal{E}_{user}: \quad q = s_{transitioning} \implies e \xrightarrow{\text{buffer}} B_{cmd}$$

$$q \neq s_{transitioning} \implies \text{Flush}(B_{cmd}) \quad \text{(procesamiento secuencial del buffer)}$$

Donde $\mathcal{E}_{user} = \{e_{add}, e_{pause}, e_{resume}, e_{skip}, e_{set\_volume}\}$ es el subconjunto de eventos originados por usuarios.

### 2.3 Protección de Concurrencia (RWMutex como Semáforo)

Sea $\ell \in \{0, 1, 2, \dots\}$ el contador del candado de lectura-escritura:

$$\text{ReadLock}(\ell): \quad \ell > 0 \implies \text{lectura permitida concurrentemente}$$

$$\text{WriteLock}(\ell): \quad \ell = -1 \implies \text{escritura exclusiva, } \forall r: \text{lectura bloqueada}$$

**Invariante de exclusión mutua:**

$$\forall \sigma: \quad \text{WriteLock activo} \implies \nexists \text{lectura concurrente en FSM}$$

$$\forall \sigma: \quad \neg(\text{WriteLock activo}) \implies |\{r : r \text{ lee FSM}\}| \in \mathbb{N}_0 \cup \{\infty\}$$

**Orden jerárquico global de locks:**

Para garantizar la ausencia de deadlocks por espera circular, todo hilo de ejecución debe adquirir locks siguiendo estrictamente el orden parcial:

$$\text{FSM Lock} \prec \text{Democracy Lock} \prec \text{FileSystem Lock} \prec \text{Cache Lock}$$

**Invariante de ausencia de deadlock:**

$$\forall h_1, h_2 \in \text{Hilos}: \quad h_1 \text{ posee } L_j \wedge h_1 \text{ espera } L_i \implies i > j \quad \text{(índice en jerarquía)}$$

**Estrategia copy-on-write para persistencia:**

$$\text{AsyncDump}(\sigma): \quad \sigma_{copy} \leftarrow \text{Clone}(\sigma) \mid_{\text{bajo WriteLock}} \implies \text{Release WriteLock} \implies \text{Serialize}(\sigma_{copy}) \implies \text{WriteFile}$$

La serialización y escritura a disco ocurren fuera de la sección crítica. El lock se mantiene únicamente durante la clonación $O(1)$ de la referencia al estado, no durante la serialización $O(|\sigma|)$.

### 2.4 Pseudocódigo Científico: Máquina de Estados

```text
MACHINE JukeboxFSM:

    STATE s IN {s_idle, s_playing, s_paused, s_transitioning, s_autoplay}
    QUEUE Q: RingBuffer(Track)
    CURRENT t_c: Track OR NULL
    VOLUME v_level IN [0, 100]
    BUFFER B_cmd: FIFO(UserEvent)
    RETRY r_retry IN N0

    ON EVENT e:
        IF s == s_transitioning AND e IN E_user:
            ENQUEUE e INTO B_cmd
            RETURN

        ACQUIRE WriteLock IF e mutates state
        LET s_next = delta(s, e)
        IF s_next IS DEFINED:
            s ← s_next
            EMIT event(state_changed, s, t_c, v_level)
        RELEASE WriteLock

        IF s != s_transitioning AND B_cmd IS NOT EMPTY:
            FLUSH B_cmd (process sequentially)

    ON EVENT e_ack_fail:
        IF r_retry > 0:
            r_retry ← r_retry - 1
            WAIT(JitterBackoff(r_retry))
            RESEND last SOAP command
        ELSE:
            CB_so.failure_count ← CB_so.failure_count + 1
            s ← s_idle

    ON EVENT e_set_volume(level):
        v_level ← level
        AVTransport.SetVolume(level)
        EMIT event(volume_changed, v_level)

    READ STATE:
        ACQUIRE ReadLock
        RETURN (s, Q, t_c, v_level)
        RELEASE ReadLock
```

---

## 3. Modelo de Democracia: Sistema de Votación Skip con Karma

### 3.1 Predicado de Autorización

Sea $U_a \subseteq U$ el conjunto de usuarios activos en un instante $\tau$. Sea $V_s \subseteq U_a \times T$ el subconjunto de usuarios que han emitido voto de skip para la pista actual $t_c$.

**Función de peso de usuario (Karma):**

$$w: U \rightarrow \mathbb{R}^{+}, \quad w(u) = f(\text{karma}(u))$$

$$\text{karma}(u) = \alpha \cdot \sum_{t \in H_u} \mathbb{1}[\text{no skip}(t)] - \beta \cdot \sum_{t \in H_u} \mathbb{1}[\text{skip}(t)]$$

Donde $H_u$ es el historial de participaciones del usuario $u$, y $\alpha, \beta \in \mathbb{R}^{+}$ son pesos de recompensa y penalización respectivamente.

**Función de umbral democrático ponderado:**

$$\theta(U_a) = \left\lfloor \frac{\sum_{u \in U_a} w(u)}{2} \right\rfloor + 1$$

**Caso simétrico (sin karma):** $w(u) = 1 \quad \forall u \implies \theta(U_a) = \lfloor |U_a|/2 \rfloor + 1$

**Predicado de autorización de skip:**

$$\text{SkipAuth}(V_s, U_a) = \begin{cases} 1 & \text{si } \sum_{(u, \_) \in V_s} w(u) \geq \theta(U_a) \\ 0 & \text{si } \sum_{(u, \_) \in V_s} w(u) < \theta(U_a) \end{cases}$$

### 3.2 Restricciones de Integridad del Voto

**Unicidad por sesión y pista:**

$$\forall u \in U_a: \quad (u, t_c) \in V_s \implies \nexists (u, t_c)' \in V_s \text{ (duplicado)}$$

**Pertenencia al conjunto activo:**

$$\forall (u, t) \in V_s: \quad u \in U_a \wedge t = t_c$$

### 3.3 Atomicidad de Purga y Re-evaluación

La desconexión de un usuario y la re-evaluación del umbral democrático constituyen una operación atómica bajo el Democracy Lock:

$$\text{PurgeAndReeval}(u, \tau_{now}) = \text{Acquire}(\text{DemocracyLock}) \implies U_a \leftarrow U_a \setminus \{u\} \implies V_s \leftarrow V_s \setminus \{(u, t_c)\} \implies \text{SkipAuth}(V_s, U_a) \implies \text{Release}(\text{DemocracyLock})$$

**Invariante de consistencia temporal:**

$$\forall \tau: \quad \text{SkipAuth}(V_s(\tau), U_a(\tau)) \text{ se evalúa con } V_s \text{ y } U_a \text{ del mismo instante lógico}$$

$$\nexists \tau: \quad \text{Purge}(\mathcal{C}, \tau) \text{ y } \text{EvaluateSkip}(V_s, U_a) \text{ ocurren en instantes lógicos distintos sin sincronización}$$

### 3.4 Pseudocódigo Científico: Votación Skip

```text
FUNCTION EvaluateSkip(Votes Vs, ActiveUsers Ua):
    LET WeightSum = SUM(w(u) FOR (u, _) IN Vs)
    LET Threshold = FLOOR(SUM(w(u) FOR u IN Ua) / 2) + 1
    IF WeightSum >= Threshold:
        RETURN AUTHORIZED
    ELSE:
        RETURN PENDING

ON EVENT UserDisconnect(u):
    ACQUIRE DemocracyLock
    Ua ← Ua \ {u}
    Vs ← Vs \ {(u, t_c)}
    IF EvaluateSkip(Vs, Ua) == AUTHORIZED:
        RELEASE DemocracyLock
        INJECT EVENT e_skip INTO FSM
    ELSE:
        RELEASE DemocracyLock
```

---

## 4. Modelo de Caché LRU con TTL Adaptativo

### 4.1 Definición Formal

Sea $C_{lru}: K \rightharpoonup V \times \mathbb{T}_{exp}$ una función parcial que mapea claves de caché a pares (valor, tiempo de expiración real).

**Dominio de claves:**

$$K = \{k \mid k = \text{URL\_YouTube\_normalizada}\}$$

**Rango de valores:**

$$V = \{(stream\_url, meta) \mid stream\_url \in \mathcal{U}, meta \in M\}$$

**Campo de expiración:**

$$\mathbb{T}_{exp} = \mathbb{T} \quad \text{(marca temporal de expiración extraída de la URL, no un TTL fijo)}$$

### 4.2 Extracción de TTL Adaptativo

Sea $\text{ExtractExpiry}: \mathcal{U} \rightarrow \mathbb{T} \cup \{\bot\}$ la función que extrae el parámetro `expire` o `ratebypass` de la URL de streaming de YouTube:

$$\text{ExtractExpiry}(url) = \begin{cases} \tau_{exp} & \text{si el parámetro de expiración está presente y decodificable} \\ \tau_{now} + T_{min} & \text{en otro caso, donde } T_{min} = 300 \text{ (5 minutos)} \end{cases}$$

### 4.3 Predicado de Validez Adaptativo

$$\text{Valid}(k, \tau_{now}) = \begin{cases} 1 & \text{si } k \in \text{dom}(C_{lru}) \wedge \tau_{now} < C_{lru}(k).\tau_{exp} \\ 0 & \text{en otro caso} \end{cases}$$

**Validación pasiva antes de servicio:**

$$\text{Serve}(k, \tau_{now}) = \begin{cases} C_{lru}(k).v & \text{si } \text{Valid}(k, \tau_{now}) = 1 \wedge \text{HEAD}(C_{lru}(k).v.stream\_url) = 200 \\ \text{FetchExternal}(k) & \text{si } \text{Valid}(k, \tau_{now}) = 0 \vee \text{HEAD} \neq 200 \\ C_{lru}(k).v & \text{si } \text{Valid}(k, \tau_{now}) = 1 \wedge \text{HEAD} = 200 \text{ (confirmación)} \end{cases}$$

### 4.4 Política de Evicción LRU

Sea $\prec_{lru}$ un orden total sobre $\text{dom}(C_{lru})$ definido por accesibilidad temporal:

$$k_1 \prec_{lru} k_2 \iff \tau_{access}(k_1) < \tau_{access}(k_2)$$

Cuando $|C_{lru}| = C_{max}$ y se inserta un nuevo par $(k_{new}, v_{new})$:

$$C_{lru} \leftarrow C_{lru} \setminus \{k_{min} \mid k_{min} = \min_{\prec_{lru}} (\text{dom}(C_{lru}))\}$$

$$C_{lru} \leftarrow C_{lru} \cup \{(k_{new}, (v_{new}, \text{ExtractExpiry}(v_{new}.stream\_url)))\}$$

### 4.5 Pseudocódigo Científico: Caché LRU con TTL Adaptativo

```text
FUNCTION CacheLookup(Key k, Time now):
    IF k IN dom(C_lru) AND now < C_lru(k).t_exp:
        ── Validación pasiva: HEAD request a la URL cacheada ──
        IF HEAD(C_lru(k).v.stream_url) == 200:
            C_lru(k).t_access ← now
            RETURN C_lru(k).value
        ELSE:
            REMOVE k FROM C_lru

    ── Cache miss o entrada expirada/inválida ──
    LET v = FetchExternal(k)
    LET t_exp = ExtractExpiry(v.stream_url)
    IF t_exp == NULL:
        t_exp ← now + 300   (TTL mínimo: 5 minutos)
    IF |C_lru| == C_max:
        LET k_evict = MIN(dom(C_lru), BY t_access)
        REMOVE k_evict FROM C_lru
    C_lru(k) ← (v, now, t_exp)
    RETURN v
```

---

## 5. Modelo del Circuit Breaker con Reintentos

### 5.1 Espacio de Estados

Se definen dos instancias independientes del Circuit Breaker: $CB_{yt}$ (YouTube) y $CB_{so}$ (Sonos). Cada instancia opera sobre el mismo espacio de estados:

$$\mathcal{S}_{CB} = \{cb_{closed}, cb_{open}, cb_{half\_open}\}$$

### 5.2 Variables de Estado Internas

Para cada instancia $CB \in \{CB_{yt}, CB_{so}\}$:

- $cb_s \in \mathcal{S}_{CB}$: Estado actual
- $cb_f \in \mathbb{N}_0$: Contador de fallas consecutivas
- $cb_\tau \in \mathbb{T} \cup \{\bot\}$: Marca temporal de apertura (definida solo si $cb_s = cb_{open}$)

### 5.3 Funciones de Transición

**Umbral de fallas:** $F_{max} = 3$

**Periodo de enfriamiento:** $T_{cool} = 60$ segundos

$$\delta_{CB}(cb_{closed}, \text{failure}) = \begin{cases} cb_{open} & \text{si } cb_f + 1 \geq F_{max} \\ cb_{closed} & \text{si } cb_f + 1 < F_{max} \end{cases}$$

$$\delta_{CB}(cb_{closed}, \text{success}) = cb_{closed}, \quad cb_f \leftarrow 0$$

$$\delta_{CB}(cb_{open}, \tau_{now}) = \begin{cases} cb_{half\_open} & \text{si } \tau_{now} - cb_\tau \geq T_{cool} \\ cb_{open} & \text{si } \tau_{now} - cb_\tau < T_{cool} \end{cases}$$

$$\delta_{CB}(cb_{half\_open}, \text{success}) = cb_{closed}, \quad cb_f \leftarrow 0$$

$$\delta_{CB}(cb_{half\_open}, \text{failure}) = cb_{open}, \quad cb_\tau \leftarrow \tau_{now}$$

### 5.4 Predicado de Pasarela (Gate)

$$\text{Gate}(CB) = \begin{cases} 1 & \text{si } cb_s = cb_{closed} \\ 0 & \text{si } cb_s = cb_{open} \\ 1 & \text{si } cb_s = cb_{half\_open} \wedge \text{es petición de prueba} \end{cases}$$

**Invariante:**

$$\text{Gate}(CB) = 0 \implies \forall \text{operación dirigida al subsistema: RECHAZO inmediato}$$

### 5.5 Política de Reintento con Jitter para Sonos

Antes de declarar una falla al Circuit Breaker de Sonos, se ejecuta una política de reintento con backoff exponencial y jitter aleatorio:

$$\text{JitterBackoff}(i) = \min(2^{i} \cdot 500\text{ms} + \text{rand}(0, 500\text{ms}), 5000\text{ms})$$

**Parámetros:** Máximo de reintentos $R_{max} = 3$, intervalos base $\{500\text{ms}, 1000\text{ms}, 2000\text{ms}\}$.

$$\text{ExecuteWithRetry}(cmd) = \begin{cases} \text{resultado} & \text{si } \text{ACK}(\text{cmd}) = 1 \text{ en intento } i \leq R_{max} \\ \text{failure} & \text{si } \forall i \in [1, R_{max}]: \text{ACK}(\text{cmd}) = 0 \end{cases}$$

**Invariante de reintento:**

$$\text{ACK}(\text{cmd}) = 0 \wedge r_{retry} > 0 \implies \text{reintento con JitterBackoff} \wedge \neg(\text{incremento de } cb_f)$$

$$\text{ACK}(\text{cmd}) = 0 \wedge r_{retry} = 0 \implies cb_f \leftarrow cb_f + 1$$

### 5.6 Pseudocódigo Científico: Circuit Breaker con Reintento

```text
MACHINE CircuitBreaker:

    STATE cb_s IN {cb_closed, cb_open, cb_half_open}
    COUNTER cb_f IN N0
    TIMESTAMP cb_tau

    ON REQUEST TO EXTERNAL SUBSYSTEM:
        IF Gate(CB) == 0:
            RETURN ERROR(circuit_open)

        IF CB == CB_so (Sonos):
            LET result = ExecuteWithRetry(request)
        ELSE:
            LET result = ExecuteOnce(request)

        ON SUCCESS:
            cb_s ← cb_closed
            cb_f ← 0
        ON MAX_RETRIES_EXHAUSTED:
            cb_f ← cb_f + 1
            IF cb_s == cb_closed AND cb_f >= F_max:
                cb_s ← cb_open
                cb_tau ← now
            IF cb_s == cb_half_open:
                cb_s ← cb_open
                cb_tau ← now

    ON TICK(now):
        IF cb_s == cb_open AND (now - cb_tau) >= T_cool:
            cb_s ← cb_half_open

FUNCTION ExecuteWithRetry(cmd):
    FOR i IN [1, R_max]:
        LET response = EXECUTE cmd
        IF ACK(response) == 1:
            RETURN response
        WAIT(JitterBackoff(i))
    RETURN FAILURE
```

---

## 6. Modelo de Validación y Sanitización de Dominios (Whitelist)

### 6.1 Conjunto de Dominios Permitidos

$$D_{allowed} = \{\text{youtube.com}, \text{youtu.be}, \text{music.youtube.com}\}$$

### 6.2 Función de Extracción de Dominio

Sea $u_{in} \in \mathcal{U}$ una URL de entrada. Sea $\text{ExtractDomain}: \mathcal{U} \rightarrow \Sigma^{*}$ la función que extrae el componente de autoridad (dominio) de la URL.

### 6.3 Predicado de Validación

$$\text{ValidURL}(u_{in}) = \begin{cases} 1 & \text{si } \text{ExtractDomain}(u_{in}) \in D_{allowed} \\ 0 & \text{en otro caso} \end{cases}$$

### 6.4 Expresión Formal de la Restricción de Inyección

$$\forall u_{in} \in \text{Input}: \quad \text{ValidURL}(u_{in}) = 0 \implies \text{RECHAZO}(u_{in}) \wedge \neg(\text{ejecución de comando de sistema})$$

**Invariante de seguridad:**

$$\neg\exists u_{in}: \quad \text{ValidURL}(u_{in}) = 0 \wedge u_{in} \in \text{Input procesado por yt-dlp}$$

### 6.5 Pseudocódigo Científico: Whitelist

```text
FUNCTION ValidateAndSanitize(URL u_in):
    LET d = ExtractDomain(u_in)
    IF d IN D_allowed:
        RETURN ACCEPTED(u_in)
    ELSE:
        RETURN REJECTED
```

---

## 7. Modelo del Hub WebSocket con Heartbeats y Fan-Out Paralelo

### 7.1 Conjunto de Conexiones Activas

Sea $\mathcal{C} \subseteq U \times \mathbb{T}$ el conjunto de conexiones activas, donde cada par $(u, \tau_{last})$ representa un usuario conectado con su última marca temporal de heartbeat recibida.

### 7.2 Predicado de Conexión Viva

$$\text{Alive}(u, \tau_{now}) = \begin{cases} 1 & \text{si } (\tau_{now} - \tau_{last}(u)) \leq T_{heartbeat} \\ 0 & \text{si } (\tau_{now} - \tau_{last}(u)) > T_{heartbeat} \end{cases}$$

Donde $T_{heartbeat} = 10$ segundos.

### 7.3 Función de Purga

$$\text{Purge}(\mathcal{C}, \tau_{now}) = \{(u, \tau_{last}) \in \mathcal{C} \mid \text{Alive}(u, \tau_{now}) = 1\}$$

**Efecto colateral atómico sobre el conjunto de usuarios activos:**

$$\text{PurgeAtomic}(\mathcal{C}, \tau_{now}) = \text{Acquire}(\text{DemocracyLock}) \implies U_a \leftarrow \{u \in U_a \mid (u, \_) \in \text{Purge}(\mathcal{C}, \tau_{now})\} \implies V_s \leftarrow V_s \cap (U_a \times T) \implies \text{SkipAuth}(V_s, U_a) \implies \text{Release}(\text{DemocracyLock})$$

### 7.4 Función de Broadcast con Fan-Out Paralelo y Timeout

$$\text{Broadcast}(\mathcal{C}, msg) = \text{FanOutParalle l}(\{(u, \_) \in \mathcal{C}\}, msg, T_{send})$$

Donde $T_{send} = 100\text{ms}$ es el timeout por cliente y:

$$\text{FanOutParallel}(S, msg, T) = \bigsqcup_{u \in S} \text{SendWithTimeout}(u, msg, T)$$

El operador $\bigsqcup$ denota ejecución paralela (concurrente). El tiempo total de broadcast es:

$$T_{total} = \max_{u \in S} (\text{SendWithTimeout}(u, msg, T_{send})) + \text{overhead}$$

$$\text{SendWithTimeout}(u, msg, T) = \begin{cases} \text{OK} & \text{si entrega completada en } \leq T \\ \text{TIMEOUT} & \text{si entrega excede } T \text{ (cliente desligado del broadcast actual)} \end{cases}$$

### 7.5 Semántica de Entrega del Event Bus: At-Least-Once con Deduplicación

Cada evento despachado por el Event Bus porta un identificador único:

$$\text{EventID} = uuid_4() \quad \text{(asignado al momento de la emisión)}$$

**Política de entrega:** At-least-once con retransmisión si no se recibe ACK del cliente en un ventana $T_{ack} = 2\text{s}$.

**Deduplicación en el cliente:**

$$\text{DedupClient}(e) = \begin{cases} \text{Procesar} & \text{si } e.id \notin \text{SeenSet}_{client} \\ \text{Descartar} & \text{si } e.id \in \text{SeenSet}_{client} \end{cases}$$

$$\text{SeenSet}_{client} \leftarrow \text{SeenSet}_{client} \cup \{e.id\}, \quad \text{purga periódica de IDs antiguos}$$

### 7.6 Invariante de Consistencia

$$\forall \sigma: \quad U_a = \{u \mid (u, \_) \in \mathcal{C}\}$$

**Es decir:** El conjunto de usuarios activos para cálculos de democracia siempre es un reflejo exacto de las conexiones vivas en el Hub.

### 7.7 Pseudocódigo Científico: WebSocket Hub

```text
MACHINE WebSocketHub:

    SET C: Connections(u, t_last_heartbeat)
    T_heartbeat = 10s
    T_send = 100ms

    ON RECEIVE Heartbeat(u):
        UPDATE (u, t_last) IN C WITH (u, now)

    ON TICK(now):
        LET C_purged = {(u, t) IN C | (now - t) <= T_heartbeat}
        C ← C_purged
        PurgeAtomic(C, now)   ── Actualiza Ua, Vs y re-evalúa SkipAuth ──

    ON STATE_CHANGE(event):
        LET msg = Serialize(event)
        msg.event_id ← uuid4()
        FanOutParallel(C, msg, T_send)
        ── Reenviar a clientes sin ACK después de T_ack ──
        FOR EACH u WITHOUT ack(msg.event_id) WITHIN T_ack:
            SendWithTimeout(u, msg, T_send)
```

---

## 8. Modelo del Proxy de Streaming Seguro (Zero-Trust)

### 8.1 Generación de Token Efímero

Sea $\mathcal{T}_{token} = \{(token, filepath, \tau_{gen}) \mid token \in UUID, filepath \in \text{ValidPath}, \tau_{gen} \in \mathbb{T}\}$ el conjunto de tokens activos.

**Función de generación:**

$$\text{GenToken}(filepath, \tau_{now}) = (uuid_4(), filepath, \tau_{now}) \quad \text{(solo si } \text{ValidPath}(filepath) = 1 \text{)}$$

### 8.2 Validación de Ruta de Archivo (Anti-Path-Traversal)

Sea $fp_{base}$ la ruta raíz del directorio SMB compartido. Sea $\text{Sanitize}: \Sigma^{*} \rightarrow \Sigma^{*}$ la función que resuelve y normaliza rutas eliminando componentes `..` y symlinks.

$$\text{ValidPath}(fp) = \begin{cases} 1 & \text{si } \text{Sanitize}(fp) \in \text{Prefix}(\text{Sanitize}(fp_{base})) \wedge \neg(\exists ".." \in \text{components}(fp)) \\ 0 & \text{en otro caso} \end{cases}$$

**Restricción de origen de tokens:**

$$\text{GenToken}(fp, \tau) \text{ solo es invocada por la FSM autorizando reproducción de } t_c \in Q$$

$$\neg\exists \text{invocación directa desde input de cliente sin mediación de la FSM}$$

### 8.3 Predicado de Validez del Token

$$\text{ValidToken}(token, \tau_{now}) = \begin{cases} 1 & \text{si } (token, fp, \tau_g) \in \mathcal{T}_{token} \wedge (\tau_{now} - \tau_g) \leq T_{token} \\ 0 & \text{en otro caso} \end{cases}$$

Donde $T_{token} = 5$ segundos.

### 8.4 Modelo de Transferencia por Fragmentos (Range Requests) con Streaming Kernel

Sea $f: \text{Path} \rightarrow \text{FileDescriptor}$ la función que abre un descriptor de archivo. El streaming se realiza mediante la primitiva `sendfile` del kernel o `io.Copy` equivalente, manteniendo los datos en espacio de kernel y nunca cargando el archivo completo en heap de Go.

**Encabezado HTTP Range:**

$$\text{Range}(a, b) = \text{bytes } a \text{–} b \text{/} L, \quad 0 \leq a \leq b < L$$

**Función de transferencia:**

$$\text{StreamResponse}(path, Range) = (\text{HTTP 206}, \text{Content-Range: bytes } a \text{–} b \text{/} L, \text{KernelSendFile}(fd, a, b - a + 1))$$

Donde $\text{KernelSendFile}$ transfiere bytes directamente del descriptor de archivo al socket TCP sin pasar por el heap de la aplicación.

### 8.5 Invariantes de Seguridad Zero-Trust

$$\forall \text{request } r: \quad \text{ValidToken}(r.token, \tau_{now}) = 0 \implies r \text{ RECHAZADO (HTTP 403)}$$

$$\neg\exists r: \quad \text{filepath real de SMB expuesto en respuesta HTTP}$$

$$\forall (token, fp, \tau_g) \in \mathcal{T}_{token}: \quad (\tau_{now} - \tau_g) > T_{token} \implies (token, fp, \tau_g) \text{ eliminado de } \mathcal{T}_{token}$$

$$\forall fp \text{ servido}: \quad \text{ValidPath}(fp) = 1$$

$$\neg\exists fp \text{ servido}: \quad fp \notin \text{Prefix}(\text{Sanitize}(fp_{base}))$$

### 8.6 Pseudocódigo Científico: Proxy Stream

```text
FUNCTION SecureStream(Request r):
    LET token = r.query_param("token")
    IF NOT ValidToken(token, now):
        RETURN HTTP_403(Forbidden)

    LET (t, filepath, t_gen) = LookupToken(token)
    IF NOT ValidPath(filepath):
        RETURN HTTP_403(Forbidden)

    LET fd = OpenFileDescriptor(filepath)
    LET Range = ParseRangeHeader(r.headers)
    IF Range IS DEFINED:
        LET (a, b) = Range
        RETURN HTTP_206(Content-Range=a-b/L,
                        Body=KernelSendFile(fd, a, b-a+1))
    ELSE:
        RETURN HTTP_200(Body=KernelSendFile(fd, 0, L))

    REMOVE token FROM T_token
    CloseFileDescriptor(fd)
```

---

## 9. Modelo de Persistencia de Estado con Log Diferencial

### 9.1 Log de Eventos Append-Only

Sea $\mathcal{L} = \langle \Delta_1, \Delta_2, \dots, \Delta_n \rangle$ el log de eventos persistido en `state.log`. Cada entrada $\Delta_i$ representa un diferencial de estado:

$$\Delta = (op, data, \tau)$$

Donde:

$$op \in \{\text{append}, \text{dequeue}, \text{skip}, \text{volume}, \text{state\_change}\}$$

$$data = \begin{cases} \{track: t\} & \text{si } op = \text{append} \\ \{track\_id: id\} & \text{si } op = \text{dequeue} \\ \{track\_id: id, votes: V_s\} & \text{si } op = \text{skip} \\ \{level: v\} & \text{si } op = \text{volume} \\ \{from: q_{old}, to: q_{new}\} & \text{si } op = \text{state\_change} \end{cases}$$

### 9.2 Condición de Volcado Diferencial

$$\forall e \in \mathcal{E}_{mutation}: \quad e \text{ ejecutado sobre } \sigma \implies \text{AppendLog}(\Delta(e), \texttt{state.log})$$

Donde $\Delta(e)$ es el diferencial generado por el evento $e$, y $\text{AppendLog}$ es una operación de escritura append-only:

$$\text{AppendLog}(\Delta, file) = \text{Open}(file, \text{O\_APPEND}) \implies \text{Write}(\text{Serialize}(\Delta)) \implies \text{Close}$$

### 9.3 Rotación con Snapshot

Sea $N_{snapshot} = 1000$ el número máximo de entradas en el log antes de una rotación.

$$|\mathcal{L}| \geq N_{snapshot} \implies \text{WriteFile}(\texttt{state.json}, \text{Serialize}(\sigma_{current})) \wedge \text{Truncate}(\texttt{state.log})$$

### 9.4 Función de Restauración

En el arranque del sistema ($\tau = \tau_{boot}$):

$$\text{Restore}() = \begin{cases} \text{Replay}(\texttt{state.log}, \text{Deserialize}(\texttt{state.json})) & \text{si ambos existen} \\ \text{Replay}(\texttt{state.log}, \sigma_0) & \text{si solo log existe} \\ \text{Deserialize}(\texttt{state.json}) & \text{si solo snapshot existe} \\ \sigma_0 & \text{si ninguno existe} \end{cases}$$

Donde $\text{Replay}(\mathcal{L}, \sigma_{base})$ aplica secuencialmente cada $\Delta_i \in \mathcal{L}$ sobre $\sigma_{base}$:

$$\text{Replay}(\mathcal{L}, \sigma) = \Delta_n \circ \Delta_{n-1} \circ \dots \circ \Delta_1 (\sigma)$$

### 9.5 Invariantes de Durabilidad

$$\forall e \in \mathcal{E}_{mutation}: \quad \Delta(e) \text{ se escribe en tiempo finito } O(|\Delta|) \text{ en lugar de } O(|\sigma|)$$

$$|\text{AppendLog}| \ll |\text{FullDump}| \quad \text{(aproximadamente 95% de reducción en I/O)}$$

### 9.6 Pseudocódigo Científico: Persistencia Diferencial

```text
ON MUTATION EVENT e:
    LET delta = Delta(e)
    LET snapshot = Clone(σ)      ── Copy-on-write bajo WriteLock ──
    RELEASE WriteLock            ── Lock liberado antes de I/O ──
    AppendLog(delta, "state.log")
    IF |L| >= N_snapshot:
        WriteFile("state.json", Serialize(snapshot))
        Truncate("state.log")
        L ← ∅

ON BOOT:
    LET σ_base = LoadSnapshot("state.json") OR σ_0
    LET log = ReadLog("state.log")
    σ ← Replay(log, σ_base)
```

---

## 10. Modelo del Bus de Eventos Interno (Pub/Sub)

### 10.1 Estructura de Canales

Sea $\mathcal{CH}_{in}$ el canal de entrada (comandos) y $\mathcal{CH}_{out}^{(i)}$ los canales de salida (uno por suscriptor $i$).

**Función de inyección:**

$$\text{Inject}: \mathcal{E}_{command} \rightarrow \mathcal{CH}_{in}$$

**Función de distribución:**

$$\text{Distribute}: \mathcal{CH}_{in} \rightarrow \bigsqcup_{i \in \text{Subscribers}} \mathcal{CH}_{out}^{(i)}$$

### 10.2 Modelo de Tópico de Suscripción

Sea $Sub \subseteq \mathcal{P}(\text{Componentes})$ el conjunto de suscriptores por tipo de evento:

$$\text{Sub}(e) = \{c \mid c \text{ está suscrito al evento tipo } e\}$$

**Invariante de desacoplamiento:**

$$\forall e, \forall c_1, c_2 \in \text{Sub}(e): \quad c_1 \text{ no depende de } c_2 \text{ para procesar } e$$

### 10.3 Semántica de Entrega: At-Least-Once

Cada evento $e$ emitido por el Bus porta:

- $e.id \in UUID$: Identificador único para deduplicación
- $e.type \in \mathcal{E}_{type}$: Tipo de evento
- $e.payload$: Datos del evento
- $e.\tau_{emit} \in \mathbb{T}$: Marca temporal de emisión

**Garantía de entrega:**

$$\forall e \text{ emitido}, \forall c \in \text{Sub}(e.type): \quad e \text{ es entregado a } c \text{ al menos una vez}$$

**Mecanismo de retransmisión:**

$$\forall c \in \text{Sub}(e.type): \quad c \text{ no envía ACK}(e.id) \text{ en } T_{ack} \implies \text{re-envío de } e \text{ a } c$$

### 10.4 Pseudocódigo Científico: Event Bus

```text
MACHINE EventBus:

    CHANNEL CH_in: Buffer(Command)
    MAP Subscribers: EventType → SET(Channel)

    ON RECEIVE cmd FROM CH_in:
        LET e_type = cmd.type
        LET event_id = uuid4()
        FOR EACH ch IN Subscribers(e_type):
            SEND (event_id, cmd) TO ch   (non-blocking)
            SCHEDULE Retransmit(event_id, ch) AFTER T_ack IF NO ACK

    FUNCTION Subscribe(e_type, ch):
        Subscribers(e_type) ← Subscribers(e_type) ∪ {ch}

    ON ACK(event_id, ch):
        CANCEL Retransmit(event_id, ch)
```

---

## 11. Modelo del Worker Pool Asíncrono Limitado

### 11.1 Estructura del Pool con Concurrencia Limitada

Sea $W = \{w_1, w_2\}$ el conjunto fijo de goroutines worker ($|W| = 2$). Cada worker $w_i$ ejecuta tareas de resolución:

$$w_i: \text{Task} \rightarrow \text{Result} \cup \{\text{Timeout}\}$$

### 11.2 Cola de Prioridad para Tareas

Sea $\mathcal{T}_{queue}$ una cola de prioridad donde las tareas de pre-fetch de la siguiente pista tienen prioridad sobre solicitudes de usuario:

$$\text{Priority}(task) = \begin{cases} 1 & \text{si } task.type = \text{prefetch} \\ 2 & \text{si } task.type = \text{user\_request} \end{cases}$$

$$\text{Dequeue}(\mathcal{T}_{queue}) = \arg\min_{t \in \mathcal{T}_{queue}} \text{Priority}(t)$$

### 11.3 Restricción de Timeout con Contexto

Sea $T_{timeout} = 30$ segundos. Sea $\text{ctx}$ un contexto con cancelación:

$$\forall w_i, \forall task: \quad \text{dur}(task) > T_{timeout} \implies \text{Cancel}(ctx) \implies w_i \text{ termina (kill a nivel OS)}$$

### 11.4 Invariante de Aislamiento y Recursos

$$\forall w_i \in W: \quad \text{fallo de } w_i \text{ no propaga excepción a } w_j, \quad i \neq j$$

$$|W| = 2 \implies \text{consumo máximo de RAM por yt-dlp} \approx 300\text{MB} \quad (\text{viabilizable en Raspberry Pi})$$

### 11.5 Pseudocódigo Científico: Worker Pool Limitado con Prioridad

```text
MACHINE WorkerPool:

    SET W = {w_1, w_2}
    QUEUE T_queue: PriorityQueue(Task, BY Priority ASC)

    FOR EACH w_i IN W (CONCURRENT):
        LOOP:
            LET task = Dequeue(T_queue)   ── Bloquea si vacío ──
            LET ctx = ContextWithTimeout(T_timeout)
            LET result = ExecuteWithCtx(task, ctx)
            IF ctx.IsCancelled():
                EMIT event(resolution_timeout, task)
            ELSE:
                EMIT event(resolution_complete, result)

    ON ENQUEUE(task):
        IF task.type == "prefetch":
            T_queue.INSERT(task, priority=1)
        ELSE:
            T_queue.INSERT(task, priority=2)
```

---

## 12. Modelo del Adaptador Sonos UPnP/SOAP

### 12.1 Interfaz de Transporte AV

Sea $\text{AVTransport}: \text{Command} \times \text{Parameter} \rightarrow \text{HTTP Response}$ la función de invocación SOAP.

**Comandos mapeados:**

$$\text{SetAVTransportURI}(url) = \text{SOAP\_Call}(\text{"SetAVTransportURI"}, \{InstanceID: 0, CurrentURI: url\})$$

$$\text{Play}() = \text{SOAP\_Call}(\text{"Play"}, \{InstanceID: 0, Speed: 1\})$$

$$\text{Pause}() = \text{SOAP\_Call}(\text{"Pause"}, \{InstanceID: 0\})$$

$$\text{SetVolume}(v) = \text{SOAP\_Call}(\text{"SetVolume"}, \{InstanceID: 0, Channel: "Master", DesiredVolume: v\})$$

### 12.2 Predicado de Confirmación

$$\text{ACK}(\text{response}) = \begin{cases} 1 & \text{si } \text{response.StatusCode} = 200 \\ 0 & \text{en otro caso} \end{cases}$$

### 12.3 Invariante de Hardware

$$\text{Gate}(CB_{so}) = 0 \implies \neg(\text{SOAP\_Call ejecutada})$$

---

## 13. Modelo de Cola de Reproducción como Buffer Circular

### 13.1 Estructura Formal

Sea $Q = \text{RingBuffer}(T)$ una cola circular de capacidad $C_Q$ con dos punteros:

- $p_{head} \in [0, C_Q - 1]$: Puntero de lectura (dequeue)
- $p_{tail} \in [0, C_Q - 1]$: Puntero de escritura (enqueue)

**Invariantes estructurales:**

$$|Q| = (p_{tail} - p_{head}) \mod C_Q$$

$$|Q| < C_Q \quad \text{(la cola nunca se desborda; se expande dinámicamente si es necesario)}$$

### 13.2 Operaciones

**Enqueue ($O(1)$):**

$$Q.\text{enqueue}(t) = Q[p_{tail}] \leftarrow t \implies p_{tail} \leftarrow (p_{tail} + 1) \mod C_Q$$

**Dequeue ($O(1)$):**

$$Q.\text{dequeue}() = t_{head} \leftarrow Q[p_{head}] \implies p_{head} \leftarrow (p_{head} + 1) \mod C_Q \implies \text{RETURN } t_{head}$$

**Head ($O(1)$):**

$$Q.\text{head}() = Q[p_{head}]$$

**Invariante de presión sobre GC:**

$$\text{Dequeue} \text{ no genera copias de elementos} \implies \neg(\text{presión sobre el garbage collector de Go})$$

---

## 14. Modelo de Historial y Recomendación

### 14.1 Registro de Historial

Sea $H \subseteq T \times \mathbb{T}$ el historial de reproducciones completadas. Cada entrada $(t, \tau)$ registra que la pista $t$ fue reproducida en el instante $\tau$.

**Extensión del modelo de pista:**

El rango de valores de la caché LRU se extiende a:

$$V_{extended} = (stream\_url, meta, play\_count, skip\_count, \tau_{last})$$

### 14.2 Función de Scoring para Modo Radio Autónomo

$$\text{Score}(t, \tau_{now}) = \frac{play\_count(t) + 1}{skip\_count(t) + 1} \times e^{-\lambda (\tau_{now} - \tau_{last}(t))}$$

Donde $\lambda \in \mathbb{R}^{+}$ es un factor de decaimiento temporal que penaliza pistas reproducidas recientemente.

### 14.3 Selección de Pista para Autoplay

$$\text{SelectAutoplay}(H, \tau_{now}) = \arg\max_{t \in H} \text{Score}(t, \tau_{now})$$

**Condición de activación:**

$$q = s_{autoplay} \implies t_{next} = \text{SelectAutoplay}(H, \tau_{now}) \implies \text{Inject}(e_{add}, t_{next})$$

### 14.4 Pseudocódigo Científico: Historial y Recomendación

```text
ON TRACK_FINISHED(t_c):
    play_count(t_c) ← play_count(t_c) + 1
    τ_last(t_c) ← now
    H ← H ∪ {(t_c, now)}
    AppendLog(Delta(track_finished, t_c), "state.log")

ON TRACK_SKIPPED(t_c):
    skip_count(t_c) ← skip_count(t_c) + 1
    AppendLog(Delta(track_skipped, t_c), "state.log")

FUNCTION SelectAutoplay(H, now):
    LET scored = {(t, Score(t, now)) FOR t IN H}
    RETURN argmax(scored, BY value)

ON STATE s_autoplay:
    LET t_next = SelectAutoplay(H, now)
    IF t_next != NULL:
        Q.enqueue(t_next)
        INJECT e_add INTO FSM
    ELSE:
        q ← s_idle
```

---

## 15. Modelo de Eventos Temporales (Integración con Calendario)

### 15.1 Extensión del Conjunto de Eventos

$$\mathcal{E}_{temporal} = \{e_{meeting\_start}, e_{meeting\_end}, e_{lunch\_start}\} \subseteq \mathcal{E}$$

### 15.2 Transiciones Inducidas por Eventos Temporales

$$\delta(s_{playing}, e_{meeting\_start}) = s_{paused}$$

$$\delta(s_{paused}, e_{meeting\_end}) = s_{playing}$$

$$\delta(s_{autoplay}, e_{meeting\_start}) = s_{idle}$$

### 15.3 Inyección en el Bus de Eventos

Los eventos temporales se inyectan en el mismo canal $\mathcal{CH}_{in}$ del Bus de Eventos, sin distinción de origen:

$$\text{CalendarWebhook}(\text{event}) = \text{Inject}(\mathcal{E}_{temporal}(\text{event}), \mathcal{CH}_{in})$$

**Invariante de uniformidad:**

$$\forall e \in \mathcal{E}_{temporal}: \quad e \text{ se procesa idénticamente a un evento de usuario del mismo tipo}$$

---

## 16. Contratos de Datos: Esquemas Formales

### 16.1 Mensaje de Entrada (Cliente → Servidor)

Sea $M_{in} = (action, payload)$ donde:

$$action \in \mathcal{A}_{in} = \{\text{add\_track}, \text{pause}, \text{resume}, \text{skip}, \text{set\_volume}, \text{clear\_queue}\}$$

$$\text{Payload}(add\_track) = (url: \mathcal{U}, user\_id: \Sigma^{*}, timestamp: \mathbb{T})$$

$$\text{Payload}(set\_volume) = (level: [0, 100] \cap \mathbb{N}, user\_id: \Sigma^{*})$$

**Restricciones de integridad:**

$$\forall m \in M_{in}: \quad m.action = \text{add\_track} \implies \text{ValidURL}(m.payload.url) = 1 \wedge m.payload.user\_id \in U_a$$

$$\forall m \in M_{in}: \quad m.action = \text{set\_volume} \implies m.payload.level \in [0, 100]$$

### 16.2 Mensaje de Salida (Servidor → Broadcast)

Sea $M_{out} = (event, payload, event\_id)$ donde:

$$event \in \mathcal{E}_{out} = \{\text{global\_state\_update}, \text{hardware\_unreachable}, \text{circuit\_open}, \text{error}\}$$

$$\text{Payload}(global\_state\_update) = (current\_state: \mathcal{S}_{FSM}, democracy\_votes: (\text{required}: \mathbb{R}^{+}, \text{current}: \mathbb{R}^{+}), current\_track: T \cup \{\bot\}, queue: \text{RingBuffer}(T), volume: [0, 100])$$

**Invariante de consistencia del broadcast:**

$$\forall m_{out} \in M_{out}: \quad m_{out}.payload.current\_state = q \iff q \text{ es el estado actual de la FSM}$$

$$\forall m_{out} \in M_{out}: \quad m_{out}.payload.volume = v_{level}$$

---

## 17. Mapeo del Entorno de Ejecución

### 17.1 Correspondencia Matemática → Código Go

| Entidad Matemática | Estructura en Código Go | Tipo de Dato / Memoria |
|---|---|---|
| $\mathcal{S}_{FSM}$ | `player/fsm.go`: campo `state` de tipo `int` (constante iota) | Entero en registro de CPU; protegido por `sync.RWMutex` |
| $Q$ (Buffer Circular) | `player/fsm.go`: `ring.Buffer[Track]` | Arreglo circular pre-asignado en heap; sin re-alocación por dequeue |
| $v_{level}$ | `player/fsm.go`: campo `volume int` | Entero; persistido en `state.json`; sincronizado con Sonos vía SOAP |
| $U_a$ | `ws/hub.go`: mapa `map[string]*Client` | Tabla hash en heap; llave = `user_id` string |
| $V_s$ | `player/fsm.go`: mapa `map[string]bool` | Tabla hash en heap; llave = `user_id` |
| $C_{lru}$ | `cache/lru.go`: estructura LRU con mapa + lista doblemente enlazada | Heap; nodos enlazados con punteros; campo `t_exp` por entrada |
| $CB_{yt}, CB_{so}$ | Patrón implementado en `extractor/ytdlp.go` y `player/sonos_upnp.go` | Structs con campos atómicos `int64` para contadores; retry counter incluido |
| $\mathcal{CH}_{in}$ | Canal Go `chan Command` con buffer | Estructura de cola FIFO en heap; sincronización interna del runtime |
| $D_{allowed}$ | `extractor/ytdlp.go`: validación `regexp.MustCompile` | Expresión regular compilada estáticamente en segmento de datos |
| $\mathcal{T}_{token}$ | `streaming/proxy.go`: mapa `map[string]tokenEntry` | Tabla hash efímera; purga por goroutine de fondo |
| $\texttt{state.log}$ | Archivo append-only en disco accesible vía `os.OpenFile(O_APPEND)` | I/O bufferizado por el kernel; escritura diferida |
| $\texttt{state.json}$ | Archivo snapshot en disco accesible vía `os.WriteFile` | I/O completo; generado cada $N_{snapshot}$ eventos |
| $B_{cmd}$ | `player/fsm.go`: slice `[]UserEvent` como FIFO | Buffer dinámico en heap; procesado al salir de `s_transitioning` |
| $W$ (Workers) | `extractor/ytdlp.go`: pool de 2 goroutines con cola de prioridad | 2 pilas de goroutines independientes (4KB iniciales c/u) en el scheduler de Go |
| $f(path)$ (Streaming) | `streaming/proxy.go`: `os.Open` + `http.ServeContent` / `io.Copy` | Descriptor de archivo kernel → `sendfile` al socket TCP; sin copia a heap |
| $H$ (Historial) | `models/contracts.go`: slice `[]HistoryEntry` persistido en `state.json` | Array dinámico en heap |
| $M_{in}, M_{out}$ | `models/contracts.go`: structs JSON `IncomingMessage`, `OutgoingMessage` | Serialización/deserialización vía `encoding/json` |

### 17.2 Correspondencia de Capas Arquitectónicas

| Capa Matemática | Paquete Go | Responsabilidad en el Modelo |
|---|---|---|
| Capa de Presentación | `web/` (index.html, app.js) | $\emptyset$ (sin lógica de negocio); emite $M_{in}$, interpreta $M_{out}$; deduplica por `event_id` |
| Capa de Transporte | `ws/hub.go`, `ws/client.go` | Gestión de $\mathcal{C}$; Heartbeats; Fan-out paralelo con $T_{send}$ |
| Capa de Orquestación | Canales Go en `cmd/jukebox-server/main.go` | $\text{Distribute}$ sobre $\mathcal{CH}_{in}$; at-least-once con ACK |
| Capa de Negocio | `player/fsm.go` | $\mathcal{M} = (\mathcal{S}_{FSM}, \mathcal{E}, \delta, s_0, \mathcal{F})$; buffer $B_{cmd}$; volumen $v_{level}$ |
| Capa de Servicios | `extractor/ytdlp.go` | $W = \{w_1, w_2\}$: $\text{FetchExternal}$ con $T_{timeout}$ y cola de prioridad |
| Capa de Adaptadores | `player/sonos_upnp.go`, `streaming/proxy.go` | $\text{AVTransport}$ (con retry), $\text{SecureStream}$ (con `ValidPath`) |
| Capa de Persistencia | `cmd/jukebox-server/main.go` (o `internal/persist/`) | Log diferencial $\mathcal{L}$; Snapshot rotativo; Replay al arranque |

### 17.3 Orden Jerárquico de Locks

| Nivel | Lock | Componente |
|---|---|---|
| 1 | `fsm.mu` (RWMutex) | Máquina de estados y cola $Q$ |
| 2 | `democracy.mu` (Mutex) | Conjuntos $U_a$, $V_s$ y evaluación de votos |
| 3 | `persist.mu` (Mutex) | Archivos `state.log` y `state.json` |
| 4 | `cache.mu` (Mutex) | Caché LRU $C_{lru}$ |

**Regla de adquisición:** Todo hilo que requiera múltiples locks debe adquirirlos en orden ascendente de nivel. La violación de este orden es un invariant violation.

---

## 18. Propiedades Invariantes Globales del Sistema

### 18.1 Seguridad de Concurrencia

$$\forall \sigma, \forall u \in U_a: \quad \text{lectura de } \sigma \text{ nunca bloquea a otra lectura}$$

$$\forall \sigma: \quad \text{escritura de } \sigma \text{ es exclusiva y atómica respecto a lecturas y otras escrituras}$$

$$\forall h_1, h_2: \quad \neg(\text{espera circular sobre locks}) \quad \text{(garantizado por el orden jerárquico)}$$

### 18.2 Integridad del Estado FSM

$$\forall \tau: \quad q(\tau) \in \mathcal{S}_{FSM} \quad \text{(el estado siempre pertenece al conjunto válido)}$$

$$q = s_{transitioning} \implies \forall e \in \mathcal{E}_{user}: \quad e \xrightarrow{\text{buffer}} B_{cmd} \quad \text{(ningún evento de usuario se pierde)}$$

$$q \neq s_{transitioning} \implies B_{cmd} = \emptyset \quad \text{(el buffer se vacía al salir de transición)}$$

### 18.3 Consistencia de la Democracia

$$\forall \tau: \quad \theta(U_a(\tau)) = \left\lfloor \frac{\sum_{u \in U_a(\tau)} w(u)}{2} \right\rfloor + 1$$

$$|V_s(\tau)| \geq \theta(U_a(\tau)) \implies e_{skip} \text{ inyectado en la FSM}$$

$$\text{PurgeAtomic}: \quad U_a \text{ y } V_s \text{ se mutan bajo el mismo Democracy Lock}$$

### 18.4 Aislamiento de Circuit Breaker

$$CB_{yt} = cb_{open} \implies \neg(\exists \text{ resolución de YouTube en progreso})$$

$$CB_{so} = cb_{open} \implies \neg(\exists \text{ comando SOAP a Sonos en progreso})$$

$$CB_{yt} = cb_{open} \not\implies CB_{so} = cb_{open} \quad \text{(independencia de fallas)}$$

$$\text{ACK} = 0 \implies \text{reintento con JitterBackoff antes de incrementar } cb_f$$

### 18.5 Seguridad del Streaming

$$\forall r: \quad \text{acceso a archivo SMB} \implies \text{ValidToken}(r.token, \tau_{now}) = 1 \wedge \text{ValidPath}(r.filepath) = 1$$

$$\forall t \in \mathcal{T}_{token}: \quad (\tau_{now} - t.\tau_{gen}) > T_{token} \implies t \notin \mathcal{T}_{token}$$

$$\neg\exists fp \text{ servido}: \quad fp \notin \text{Prefix}(\text{Sanitize}(fp_{base}))$$

### 18.6 Consistencia del Broadcast

$$\forall e \in \mathcal{E}_{mutation}: \quad e \text{ procesado por FSM} \implies \exists m_{out} \in M_{out}: \text{FanOutParallel}(\mathcal{C}, m_{out}, T_{send})$$

$$m_{out}.payload.current\_state = q \text{ (post-mutación)}$$

$$m_{out}.payload.volume = v_{level}$$

### 18.7 Persistencia Diferencial

$$\forall e \in \mathcal{E}_{mutation}: \quad \Delta(e) \in \mathcal{L} \quad \text{(todo evento deja huella en el log)}$$

$$|\mathcal{L}| \geq N_{snapshot} \implies \text{Snapshot}(\sigma) \wedge \text{Truncate}(\mathcal{L})$$

### 18.8 Consistencia del Volumen

$$\forall \tau: \quad v_{level}(\tau) = \text{último valor asignado por } e_{set\_volume} \text{ o restaurado de } \texttt{state.json}$$

$$\text{Restore}() \implies \text{AVTransport.SetVolume}(v_{level}) \quad \text{(sincronización con hardware al arranque)}$$

---

## 19. Modelo Integrado: Flujo de Añadir y Reproducir Track de YouTube

### 19.1 Secuencia de Transformaciones de Estado

Sea $\sigma_0$ el estado inicial. La secuencia de transformaciones ante el evento $e_{add\_track}$ es:

$$\sigma_0 \xrightarrow{\text{ValidURL}(u_{in})=1} \sigma_1 \xrightarrow{\text{Inject}(e_{add})} \sigma_2 \xrightarrow{\text{FSM: } Q.\text{enqueue}(t)} \sigma_3 \xrightarrow{\text{FanOutParallel}(\mathcal{C}, queue\_updated)} \sigma_4$$

$$\sigma_4 \xrightarrow{\text{PreFetch}(t_{next}, \text{priority}=1)} \sigma_5 \xrightarrow{C_{lru}(k) \leftarrow v \text{ con TTL adaptativo}} \sigma_6 \xrightarrow{e_{eof} \vee e_{skip}} \sigma_7$$

$$\sigma_7 \xrightarrow{q \leftarrow s_{transitioning}, B_{cmd} \text{ activo}} \sigma_8 \xrightarrow{\text{ExecuteWithRetry}(\text{SetAVTransportURI})} \sigma_9 \xrightarrow{\text{ACK}=1} \sigma_{10} \xrightarrow{q \leftarrow s_{playing}, \text{Flush}(B_{cmd})} \sigma_{11}$$

$$\sigma_8 \xrightarrow{\text{ACK}=0} \text{JitterBackoff} \xrightarrow{\text{ACK}=1} \sigma_{10}$$

$$\sigma_8 \xrightarrow{\text{ACK}=0 \wedge r_{retry}=0} q \leftarrow s_{idle}, cb_f \leftarrow cb_f + 1$$

### 19.2 Pseudocódigo Científico Integrado

```text
FUNCTION AddAndPlayTrack(URL u_in, User u):

    ── FASE 1: VALIDACIÓN ──
    IF ValidURL(u_in) == 0:
        RETURN ERROR(invalid_domain)

    ── FASE 2: INYECCIÓN EN BUS ──
    LET cmd = Command(type=e_add, url=u_in, user=u)
    Inject(cmd, CH_in)

    ── FASE 3: MUTACIÓN DE FSM ──
    FSM.ACQUIRE_WRITE_LOCK()
    IF q == s_transitioning:
        ENQUEUE cmd INTO B_cmd
        FSM.RELEASE_WRITE_LOCK()
        RETURN (deferred)
    LET t = Track(url=u_in, resolved=FALSE)
    Q.enqueue(t)
    IF q == s_idle:
        q ← s_transitioning
    FSM.RELEASE_WRITE_LOCK()
    FanOutParallel(C, Event(queue_updated, Q))

    ── FASE 4: PRE-FETCH ASÍNCRONO CON PRIORIDAD ──
    LET t_next = Q.head()
    IF t_next.resolved == FALSE:
        LET k = NormalizeURL(t_next.url)
        LET v = CacheLookup(k, now)
        IF v == CACHE_MISS:
            ENQUEUE Task(ResolveViaYtDlp, k, priority=1) TO WorkerPool
        t_next.url_resolved ← v.stream_url
        t_next.resolved ← TRUE

    ── FASE 5: TRANSICIÓN DE REPRODUCCIÓN CON RETRY ──
    ON EVENT (e_eof ∨ e_skip):
        FSM.ACQUIRE_WRITE_LOCK()
        q ← s_transitioning
        FSM.RELEASE_WRITE_LOCK()

        IF Gate(CB_so) == 1:
            LET result = ExecuteWithRetry(SetAVTransportURI(t_next.url_resolved))
            IF result == SUCCESS:
                AVTransport.Play()
                AVTransport.SetVolume(v_level)
                FSM.ACQUIRE_WRITE_LOCK()
                q ← s_playing
                t_c ← Q.dequeue()
                play_count(t_c) ← play_count(t_c) + 1
                τ_last(t_c) ← now
                FSM.RELEASE_WRITE_LOCK()
                ── Procesar comandos diferidos ──
                Flush(B_cmd)
                FanOutParallel(C, Event(state_changed, playing, t_c, v_level))
            ELSE:
                CB_so.failure_count ← CB_so.failure_count + 1
                FSM.ACQUIRE_WRITE_LOCK()
                q ← s_idle
                FSM.RELEASE_WRITE_LOCK()
                FanOutParallel(C, Event(hardware_unreachable))

    ── FASE 6: PERSISTENCIA DIFERENCIAL ──
    LET snapshot = Clone(σ)
    AppendLog(Delta(e_add, t), "state.log")
    IF |L| >= N_snapshot:
        WriteFile("state.json", Serialize(snapshot))
        Truncate("state.log")
```

---

## 20. Extensión: Múltiples Zonas de Audio

### 20.1 Vectorización del Estado Global

El modelo de estado se generaliza para soportar $m$ zonas de audio (altavoces Sonos independientes):

$$\sigma_{multi} = (\vec{q}, \vec{Q}, \vec{t_c}, \vec{v_{level}}, U_a, V_s, C_{cb}^{yt}, \vec{C_{cb}^{so}}, C_{lru})$$

Donde $\vec{q} = (q^{(1)}, q^{(2)}, \dots, q^{(m)})$ y cada $q^{(i)} \in \mathcal{S}_{FSM}$ opera su propia instancia de FSM independiente.

### 20.2 Invariante de Independencia de Zonas

$$\forall i, j \in [1, m], i \neq j: \quad q^{(i)} \text{ y } q^{(j)} \text{ no comparten estado mutable}$$

$$\forall i: \quad C_{cb}^{so(i)} \text{ es independiente de } C_{cb}^{so(j)}$$

### 20.3 Broadcast por Zona

$$\text{FanOutParallel}(\mathcal{C}, msg^{(i)}) \quad \text{donde } msg^{(i)} \text{ contiene solo el estado de la zona } i$$

Los clientes se suscriben a una o más zonas. El payload del broadcast incluye un campo `zone_id` para filtrado en el cliente.

---

*Fin del análisis matemático-formal.*
