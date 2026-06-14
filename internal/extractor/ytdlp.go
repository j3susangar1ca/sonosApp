package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/models"
	"github.com/jesuslangarica/sonosApp/internal/telemetry"
)

// Allowed YouTube domains for whitelisting (Section 6)
var allowedDomains = map[string]bool{
	"youtube.com":       true,
	"youtu.be":          true,
	"music.youtube.com": true,
}

var execCommandContext = exec.CommandContext

// ValidateURL parses the URL and ensures it belongs to the whitelisted domains.
// Prevents command injections and SSRF.
func ValidateURL(inputURL string) (string, error) {
	u, err := url.Parse(inputURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL format: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid URL scheme: %s", u.Scheme)
	}

	host := u.Hostname()
	normalizedHost := strings.TrimPrefix(host, "www.")

	if !allowedDomains[normalizedHost] {
		return "", fmt.Errorf("domain not allowed: %s", host)
	}

	// Reconstruct sanitised URL string
	return u.String(), nil
}

// ResolveURL runs yt-dlp to retrieve metadata and stream URLs.
// Enforces contextual execution and prevents shell injection.
func ResolveURL(ctx context.Context, ytdlpPath string, targetURL string) (models.Track, error) {
	sanitizedURL, err := ValidateURL(targetURL)
	if err != nil {
		return models.Track{}, err
	}

	// Direct execution without sh/bash shell wrapper to avoid command injection
	cmd := execCommandContext(ctx, ytdlpPath, "-j", "-f", "bestaudio[ext=m4a]/bestaudio[ext=mp3]/bestaudio", "--no-playlist", sanitizedURL)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Measure yt-dlp execution latency for telemetry observability.
	// This captures the real wall-clock time of the OS process invocation,
	// regardless of whether the caller uses the WorkerPool or calls ResolveURL directly.
	startTime := time.Now()
	defer func() {
		telemetry.ObserveYoutubeLatency(time.Since(startTime).Seconds())
	}()

	err = cmd.Run()
	if err != nil {
		return models.Track{}, fmt.Errorf("yt-dlp execution failed: %w (stderr: %s)", err, stderr.String())
	}

	var output struct {
		ID        string  `json:"id"`
		Title     string  `json:"title"`
		Duration  float64 `json:"duration"`
		Thumbnail string  `json:"thumbnail"`
		URL       string  `json:"url"`
	}

	err = json.Unmarshal(stdout.Bytes(), &output)
	if err != nil {
		return models.Track{}, fmt.Errorf("failed to parse yt-dlp JSON: %w", err)
	}

	if output.URL == "" {
		return models.Track{}, errors.New("no streaming URL found in yt-dlp output")
	}

	track := models.Track{
		ID:     output.ID,
		UserID: "", // Set by FSM caller on EventAdd
		Src:    models.SourceYoutube,
		Meta: models.Metadata{
			Title:     output.Title,
			Thumbnail: output.Thumbnail,
			Duration:  int(output.Duration),
		},
		URL: output.URL,
		Dur: int(output.Duration),
	}

	return track, nil
}

// Task represents a YouTube extraction job.
type Task struct {
	URL        string
	Priority   int // Lower values mean higher priority (e.g. 1 = prefetch, 2 = user request)
	ResultChan chan Result
}

// Result wraps the resolution output.
type Result struct {
	Track models.Track
	Err   error
}

// PriorityQueue represents a thread-safe priority queue for resolution tasks.
type PriorityQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	tasks   []*Task
	stopped bool
}

// NewPriorityQueue initializes a PriorityQueue.
func NewPriorityQueue() *PriorityQueue {
	pq := &PriorityQueue{}
	pq.cond = sync.NewCond(&pq.mu)
	return pq
}

// Push appends a task keeping the list sorted by priority.
// Nil tasks are treated as shutdown sentinels and appended to the end.
func (pq *PriorityQueue) Push(task *Task) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	if task == nil {
		pq.tasks = append(pq.tasks, nil)
		pq.cond.Signal()
		return
	}

	inserted := false
	for i, t := range pq.tasks {
		if t != nil && task.Priority < t.Priority {
			pq.tasks = append(pq.tasks[:i], append([]*Task{task}, pq.tasks[i:]...)...)
			inserted = true
			break
		}
	}
	if !inserted {
		pq.tasks = append(pq.tasks, task)
	}

	pq.cond.Signal()
}

// Pop retrieves and removes the highest priority task from the queue.
// It blocks until a task is available or the queue is stopped.
func (pq *PriorityQueue) Pop(stopCh <-chan struct{}) (*Task, error) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	for len(pq.tasks) == 0 && !pq.stopped {
		// Verificación no bloqueante antes de dormir la goroutine
		select {
		case <-stopCh:
			pq.stopped = true
			return nil, errors.New("queue stopped")
		default:
		}

		// Patrón seguro: Wait libera el mutex, espera la señal y lo re-adquiere dinámicamente
		pq.cond.Wait()

		// Verificación inmediata tras despertar
		select {
		case <-stopCh:
			pq.stopped = true
			return nil, errors.New("queue stopped")
		default:
		}
	}

	if pq.stopped && len(pq.tasks) == 0 {
		return nil, errors.New("queue stopped")
	}

	task := pq.tasks[0]
	pq.tasks[0] = nil // avoid memory leak
	pq.tasks = pq.tasks[1:]
	return task, nil
}

// WorkerPool controls extraction tasks with a concurrency cap of 2 goroutines.
type WorkerPool struct {
	queue      *PriorityQueue
	ytdlpPath  string
	stopChan   chan struct{}
	wg         sync.WaitGroup
	activeJobs int32
}

// NewWorkerPool instantiates a WorkerPool.
func NewWorkerPool(ytdlpPath string) *WorkerPool {
	return &WorkerPool{
		queue:     NewPriorityQueue(),
		ytdlpPath: ytdlpPath,
		stopChan:  make(chan struct{}),
	}
}

// Start spawns exactly 2 worker goroutines.
func (wp *WorkerPool) Start() {
	for i := 0; i < 2; i++ {
		wp.wg.Add(1)
		go wp.worker()
	}
}

// Stop signals all workers to exit and waits for their termination.
func (wp *WorkerPool) Stop() {
	close(wp.stopChan)

	// Send nil sentinels to unblock any workers waiting on Pop()
	wp.queue.Push(nil)
	wp.queue.Push(nil)

	wp.wg.Wait()
}

// Submit enqueues a resolution task and returns its result channel.
func (wp *WorkerPool) Submit(targetURL string, priority int) <-chan Result {
	resChan := make(chan Result, 1)
	task := &Task{
		URL:        targetURL,
		Priority:   priority,
		ResultChan: resChan,
	}
	wp.queue.Push(task)
	return resChan
}

// ActiveJobs returns the number of currently running extraction processes.
func (wp *WorkerPool) ActiveJobs() int32 {
	return atomic.LoadInt32(&wp.activeJobs)
}

func (wp *WorkerPool) worker() {
	defer wp.wg.Done()
	for {
		select {
		case <-wp.stopChan:
			return
		default:
		}

		task, err := wp.queue.Pop(wp.stopChan)
		if err != nil {
			// Queue stopped, exit worker
			return
		}
		if task == nil {
			// Sentinel nil unblocked us or queue was cleared. Exit loop if stopped.
			select {
			case <-wp.stopChan:
				return
			default:
				continue
			}
		}

		atomic.AddInt32(&wp.activeJobs, 1)
		slog.Info("Worker resolving track URL", "url", task.URL, "priority", task.Priority)

		// Enforce strict 30-second timeout for resolving URLs
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		track, err := ResolveURL(ctx, wp.ytdlpPath, task.URL)
		cancel()

		atomic.AddInt32(&wp.activeJobs, -1)

		task.ResultChan <- Result{Track: track, Err: err}
		close(task.ResultChan)
	}
}
