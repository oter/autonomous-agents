// Package docker is the sliver of the Docker Engine API the control plane
// uses, spoken over a unix socket with net/http. Both Runners are plain unix
// sockets (SPEC §3), so this is the whole client.
package docker

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrNotFound wraps any 404 from the Engine: no such container on inspect,
// no such image on create. Docker's own message is appended.
var ErrNotFound = errors.New("docker: not found")

type Client struct {
	http *http.Client
}

// New returns a client for a docker_host of the form unix:///path/to.sock.
func New(host string) (*Client, error) {
	path, ok := strings.CutPrefix(host, "unix://")
	if !ok {
		return nil, fmt.Errorf("docker_host %q: only unix:// sockets are supported", host)
	}
	return &Client{http: &http.Client{
		// A stalled socket must fail, not hang the poller forever (SPEC §3).
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", path)
			},
		},
	}}, nil
}

// ContainerConfig is the body of POST /containers/create.
type ContainerConfig struct {
	Image       string     `json:"Image"`
	Env         []string   `json:"Env"`
	StopTimeout int        `json:"StopTimeout"`
	HostConfig  HostConfig `json:"HostConfig"`
}

type HostConfig struct {
	Memory     int64 `json:"Memory,omitzero"`
	MemorySwap int64 `json:"MemorySwap,omitzero"`
	NanoCPUs   int64 `json:"NanoCpus,omitzero"`
}

// State is the part of ContainerInspect the control plane reads.
type State struct {
	Status    string `json:"Status"` // created | running | exited | dead | ...
	ExitCode  int    `json:"ExitCode"`
	OOMKilled bool   `json:"OOMKilled"`
}

// Exited reports whether the container has reached a terminal state.
func (s State) Exited() bool { return s.Status == "exited" || s.Status == "dead" }

func (c *Client) Create(ctx context.Context, name string, cfg ContainerConfig) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, "POST", "/containers/create?name="+name, cfg, &out)
	return out.ID, err
}

func (c *Client) Start(ctx context.Context, id string) error {
	return c.do(ctx, "POST", "/containers/"+id+"/start", nil, nil)
}

// Remove deletes an exited container. Nothing here stops one: a Run is
// never killed by the control plane (SPEC §9).
func (c *Client) Remove(ctx context.Context, id string) error {
	return c.do(ctx, "DELETE", "/containers/"+id, nil, nil)
}

// Image is the part of GET /images/{ref}/json a Journal records (SPEC
// §10): the content id, and the registry digest when the image was pulled.
// A tag can move; neither of these can.
type Image struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
}

func (c *Client) Image(ctx context.Context, ref string) (Image, error) {
	var out Image
	err := c.do(ctx, "GET", "/images/"+ref+"/json", nil, &out)
	return out, err
}

func (c *Client) Inspect(ctx context.Context, id string) (State, error) {
	var out struct {
		State State `json:"State"`
	}
	err := c.do(ctx, "GET", "/containers/"+id+"/json", nil, &out)
	return out.State, err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		json.UnmarshalRead(res.Body, &e)
		if res.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrNotFound, e.Message)
		}
		return fmt.Errorf("docker %s %s: %s: %s", method, path, res.Status, e.Message)
	}
	if out == nil {
		return nil
	}
	return json.UnmarshalRead(res.Body, out)
}
