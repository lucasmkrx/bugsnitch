// SPDX-License-Identifier: MPL-2.0
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lucasmkrx/bugsnitch/internal/audit"
	"github.com/lucasmkrx/bugsnitch/internal/incidents"
)

func (s *Server) configureMonitor(c *domainCall) error {
	var in incidents.MonitorConfig
	if err := c.decode(&in); err != nil {
		return err
	}
	reg, err := s.Incidents.ConfigureMonitor(c.ctx, c.Workspace, c.Project, c.ID, in)
	if err != nil {
		return incidentError(err)
	}
	return c.reply(201, reg)
}
func (s *Server) clearHold(c *domainCall) error {
	var in struct {
		Hold int64 `json:"holdId"`
	}
	if err := c.decode(&in); err != nil {
		return err
	}
	if err := s.Incidents.ClearDeploymentHold(c.ctx, c.Workspace, c.Project, c.ID, in.Hold); err != nil {
		return incidentError(err)
	}
	return c.reply(200, object{"cleared": in.Hold})
}
func (s *Server) releaseSignal(ctx context.Context, r DomainRequest) (any, error) {
	if r.Operation == "deployment.gate" {
		if r.Actor != [2]string{"service", "deployment-gate"} {
			return nil, denied
		}
		var in incidents.GateSubject
		if json.Unmarshal(r.Input, &in) != nil {
			return nil, bad("Invalid gate subject")
		}
		result, err := s.Incidents.DeploymentGate(ctx, in)
		if errors.Is(err, incidents.ErrUnmapped) {
			return nil, denied
		}
		if err != nil {
			return nil, incidentError(err)
		}
		return result, nil
	}
	var sig incidents.GitHubRelease
	if json.Unmarshal(r.Input, &sig) != nil {
		return nil, bad("Invalid release signal")
	}
	if r.Actor != [2]string{"provider", fmt.Sprintf("github/%d", sig.Installation)} {
		return nil, denied
	}
	err := s.Incidents.GitHubRelease(audit.WithActor(ctx, audit.Provider, r.Actor[1]), sig)
	if err != nil {
		return nil, incidentError(err)
	}
	return true, nil
}

func (s *Server) disableMonitor(c *domainCall) error {
	var in struct {
		Monitor string `json:"monitorId"`
	}
	if err := c.decode(&in); err != nil {
		return err
	}
	if err := s.Incidents.DisableMonitor(c.ctx, c.Workspace, c.Project, c.ID, in.Monitor); err != nil {
		return incidentError(err)
	}
	return c.reply(200, object{"disabled": in.Monitor})
}
