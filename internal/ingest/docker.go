// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"context"

	"github.com/maxie7/logscry/internal/model"
)

// DockerSelector describes which containers a DockerSource should follow.
type DockerSelector struct {
	All       bool     // --docker-all
	Label     string   // --docker-label k=v
	NameRegex string   // --docker-name <regex>
	IDs       []string // explicit container IDs
}

// DockerSource follows logs from Docker containers, auto-attaching to new ones.
type DockerSource struct {
	Selector DockerSelector
}

// NewDockerSource returns a Source that follows Docker container logs.
//
// TODO(M1): use github.com/docker/docker/client — ContainerLogs (Follow,
// Timestamps, Tail), demux non-TTY streams via pkg/stdcopy.StdCopy (detect TTY
// via ContainerInspect), parse RFC3339Nano timestamps, and auto-attach/detach
// via client.Events on container start/die.
func NewDockerSource(sel DockerSelector) *DockerSource {
	return &DockerSource{Selector: sel}
}

// Name implements Source.
func (s *DockerSource) Name() string { return "docker" }

// Lines implements Source.
//
// TODO(M1): implement Docker log following.
func (s *DockerSource) Lines(ctx context.Context, out chan<- model.LogLine) error {
	_ = ctx
	_ = out
	return nil
}
