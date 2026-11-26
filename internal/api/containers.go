package api

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"dc0s/internal/dockerapi"
	"dc0s/internal/services"
)

func findService(groups []services.Group, key string) (services.Service, bool) {
	return services.Find(groups, key)
}

// inspectFor inspects the container named in the path. On failure it has sent
// the response.
func (s *server) inspectFor(w http.ResponseWriter, r *http.Request) (dockerapi.Client, container.InspectResponse, bool) {
	_, cli, ok := s.clientFor(w, r)
	if !ok {
		return nil, container.InspectResponse{}, false
	}
	ref := r.PathValue("id")
	if !validContainerRef(ref) {
		writeError(w, http.StatusBadRequest, "invalid", "not a container name or ID")
		return nil, container.InspectResponse{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), hostCallTimeout)
	defer cancel()
	res, err := cli.ContainerInspect(ctx, ref, client.ContainerInspectOptions{})
	if err != nil {
		dockerFailure(w, "container "+ref, err)
		return nil, container.InspectResponse{}, false
	}
	return cli, res.Container, true
}

// validContainerRef accepts what Docker accepts as a container name or ID.
func validContainerRef(ref string) bool {
	if ref == "" || len(ref) > 255 {
		return false
	}
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '-':
		default:
			return false
		}
	}
	return true
}

// splitEnv splits "NAME=value" on the first "=". Values routinely contain "="
// themselves (URLs, base64), and an entry may have no "=" at all.
func splitEnv(entry string) (name, value string) {
	name, value, _ = strings.Cut(entry, "=")
	return name, value
}

// handleEnvNames lists the names of a container's environment variables, and
// only the names. Values are secrets as often as not; they are sent one by one
// when the person asks to see them.
func (s *server) handleEnvNames(w http.ResponseWriter, r *http.Request) {
	_, c, ok := s.inspectFor(w, r)
	if !ok {
		return
	}
	names := []string{}
	if c.Config != nil {
		for _, entry := range c.Config.Env {
			name, _ := splitEnv(entry)
			names = append(names, name)
		}
	}
	writeJSON(w, http.StatusOK, map[string][]string{"names": names})
}

// handleEnvReveal returns the values of the named variables, or of all when no
// names are given. It is a POST so that it passes the cross-site check: values
// must not be obtainable by a request another site can cause.
func (s *server) handleEnvReveal(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Names []string `json:"names"`
	}
	if !readOptionalJSON(w, r, &in) {
		return
	}
	_, c, ok := s.inspectFor(w, r)
	if !ok {
		return
	}
	values := map[string]string{}
	if c.Config != nil {
		for _, entry := range c.Config.Env {
			name, value := splitEnv(entry)
			if in.Names == nil || slices.Contains(in.Names, name) {
				values[name] = value
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]map[string]string{"values": values})
}

type portView struct {
	Container string `json:"container"`
	Host      string `json:"host"`
}

type mountView struct {
	Type        string `json:"type"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly"`
}

type networkView struct {
	Name    string   `json:"name"`
	IP      string   `json:"ip"`
	Aliases []string `json:"aliases"`
}

type composeView struct {
	Project    string `json:"project"`
	Service    string `json:"service"`
	WorkingDir string `json:"workingDir"`
}

// infoView is what the Info tab shows. It deliberately has no environment.
type infoView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	ImageID       string            `json:"imageId"`
	Digests       []string          `json:"digests"`
	Created       string            `json:"created"`
	StartedAt     string            `json:"startedAt"`
	FinishedAt    string            `json:"finishedAt"`
	State         string            `json:"state"`
	Health        string            `json:"health"`
	ExitCode      int               `json:"exitCode"`
	RestartPolicy string            `json:"restartPolicy"`
	Restarts      int               `json:"restarts"`
	Command       []string          `json:"command"`
	Ports         []portView        `json:"ports"`
	Mounts        []mountView       `json:"mounts"`
	Networks      []networkView     `json:"networks"`
	Labels        map[string]string `json:"labels"`
	Compose       composeView       `json:"compose"`
}

func (s *server) handleInfo(w http.ResponseWriter, r *http.Request) {
	cli, c, ok := s.inspectFor(w, r)
	if !ok {
		return
	}
	info := infoView{
		ID:       c.ID,
		Name:     strings.TrimPrefix(c.Name, "/"),
		ImageID:  c.Image,
		Created:  c.Created,
		Restarts: c.RestartCount,
		Digests:  []string{},
		Command:  []string{},
		Ports:    []portView{},
		Mounts:   []mountView{},
		Networks: []networkView{},
		Labels:   map[string]string{},
	}
	if c.Path != "" {
		info.Command = append([]string{c.Path}, c.Args...)
	}
	if st := c.State; st != nil {
		info.State, info.ExitCode = string(st.Status), st.ExitCode
		info.StartedAt, info.FinishedAt = zeroTimeAsEmpty(st.StartedAt), zeroTimeAsEmpty(st.FinishedAt)
		if st.Health != nil && st.Health.Status != container.NoHealthcheck {
			info.Health = string(st.Health.Status)
		}
	}
	if cfg := c.Config; cfg != nil {
		info.Image = cfg.Image
		if cfg.Labels != nil {
			info.Labels = cfg.Labels
		}
		info.Compose = composeView{
			Project:    cfg.Labels[services.LabelProject],
			Service:    cfg.Labels[services.LabelService],
			WorkingDir: cfg.Labels[services.LabelWorkingDir],
		}
	}
	if hc := c.HostConfig; hc != nil {
		info.RestartPolicy = string(hc.RestartPolicy.Name)
		if hc.RestartPolicy.MaximumRetryCount > 0 {
			info.RestartPolicy += ":" + strconv.Itoa(hc.RestartPolicy.MaximumRetryCount)
		}
	}
	info.Ports = portViews(c)
	for _, m := range c.Mounts {
		source := m.Source
		if m.Name != "" {
			source = m.Name // a named volume: the name says more than its path
		}
		info.Mounts = append(info.Mounts, mountView{Type: string(m.Type), Source: source, Destination: m.Destination, ReadOnly: !m.RW})
	}
	if ns := c.NetworkSettings; ns != nil {
		for name, ep := range ns.Networks {
			nv := networkView{Name: name, Aliases: []string{}}
			if ep != nil {
				if ep.IPAddress.IsValid() {
					nv.IP = ep.IPAddress.String()
				}
				if ep.Aliases != nil {
					nv.Aliases = ep.Aliases
				}
			}
			info.Networks = append(info.Networks, nv)
		}
		slices.SortFunc(info.Networks, func(a, b networkView) int { return cmp.Compare(a.Name, b.Name) })
	}

	// The digest tells exactly which build is running; it lives on the image.
	ctx, cancel := context.WithTimeout(r.Context(), hostCallTimeout)
	defer cancel()
	if img, err := cli.ImageInspect(ctx, c.Image); err == nil && img.RepoDigests != nil {
		info.Digests = img.RepoDigests
	}

	writeJSON(w, http.StatusOK, info)
}

// portViews lists a container's ports: what is actually published while it
// runs, otherwise what it is configured to publish.
func portViews(c container.InspectResponse) []portView {
	var ports network.PortMap
	if c.NetworkSettings != nil && len(c.NetworkSettings.Ports) > 0 {
		ports = c.NetworkSettings.Ports
	} else if c.HostConfig != nil {
		ports = c.HostConfig.PortBindings
	}
	out := []portView{}
	for port, bindings := range ports {
		if len(bindings) == 0 {
			out = append(out, portView{Container: port.String()})
			continue
		}
		for _, b := range bindings {
			host := b.HostPort
			if b.HostIP.IsValid() && host != "" {
				host = b.HostIP.String() + ":" + host
				if b.HostIP.Is6() {
					host = "[" + b.HostIP.String() + "]:" + b.HostPort
				}
			}
			out = append(out, portView{Container: port.String(), Host: host})
		}
	}
	slices.SortFunc(out, func(a, b portView) int {
		return cmp.Or(cmp.Compare(a.Container, b.Container), cmp.Compare(a.Host, b.Host))
	})
	return out
}

// zeroTimeAsEmpty hides Docker's "never" timestamp (year 1).
func zeroTimeAsEmpty(s string) string {
	if strings.HasPrefix(s, "0001-01-01") {
		return ""
	}
	return s
}
