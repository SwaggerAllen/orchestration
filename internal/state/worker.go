package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SwaggerAllen/orchestration/internal/core"
	"github.com/SwaggerAllen/orchestration/internal/protocol"
)

// Worker is the Store backed by the metronome's ProjectState Durable
// Object.
//
// It lives behind the same Worker the metronome does rather than in a
// service of its own, because the alternatives all cost more than the
// problem: a Linear seat per role, a database to operate, or a second
// deployment to keep in step. The Worker is already deployed, already
// holds secrets, and already has a Durable Object per project.
type Worker struct {
	BaseURL string // e.g. https://pipeline-metronome.<sub>.workers.dev
	Project string // the path segment naming this project's object
	Token   string
	HTTP    *http.Client
}

// NewWorker builds the client. A missing base URL or token yields nil
// rather than a broken client, so a project that has not been wired up
// records nothing and is judged on nothing — the same safe place a
// brand-new ticket sits in.
func NewWorker(baseURL, project, token string) *Worker {
	if baseURL == "" || project == "" || token == "" {
		return nil
	}
	return &Worker{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Project: project,
		Token:   token,
		// Short, and deliberately so on the write path: the caller
		// declines to transition when this fails, and the sweep is
		// convergent, so waiting a long time to fail is worse than
		// failing and retrying on the next beat.
		HTTP: &http.Client{Timeout: 10 * time.Second},
	}
}

type wireMove struct {
	Ticket string `json:"ticket,omitempty"`
	From   string `json:"from"`
	To     string `json:"to"`
	Role   string `json:"role"`
}

func (w *Worker) Record(ctx context.Context, ticketID string, m core.RecordedMove) error {
	body, err := json.Marshal(wireMove{
		Ticket: ticketID,
		From:   string(m.From),
		To:     string(m.To),
		Role:   string(m.Role),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url("/record"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "Bearer "+w.Token)
	req.Header.Set("content-type", "application/json")
	res, err := w.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("state: recording %s: HTTP %d: %s", ticketID, res.StatusCode, snippet(res.Body))
	}
	return nil
}

func (w *Worker) All(ctx context.Context) (map[string]core.RecordedMove, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url("/all"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("authorization", "Bearer "+w.Token)
	res, err := w.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("state: reading the move record: HTTP %d: %s", res.StatusCode, snippet(res.Body))
	}
	var wire map[string]wireMove
	if err := json.NewDecoder(res.Body).Decode(&wire); err != nil {
		return nil, fmt.Errorf("state: decoding the move record: %w", err)
	}
	out := make(map[string]core.RecordedMove, len(wire))
	for id, m := range wire {
		out[id] = core.RecordedMove{
			From: protocol.State(m.From),
			To:   protocol.State(m.To),
			Role: core.Role(m.Role),
		}
	}
	return out, nil
}

func (w *Worker) url(op string) string {
	return w.BaseURL + "/state/" + w.Project + op
}

// snippet keeps an error body short enough to read in a log line. The
// Worker's errors are one line; anything longer is a proxy or an outage
// page, and the first line of those says as much as the whole.
func snippet(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(b))
}
