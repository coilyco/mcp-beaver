package mcpserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/coilyco/umbra/http/opcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	adminDescribePath = "/admin/describe"
	adminReloadPath   = "/admin/reload"
	transportMode     = "streamable-http"
)

type adminDescribeResponse struct {
	Server struct {
		Name     string `json:"name"`
		Spec     string `json:"spec"`
		SpecPath string `json:"specPath,omitempty"`
	} `json:"server"`
	Transport struct {
		Mode        string `json:"mode"`
		MCPPath     string `json:"mcpPath"`
		APIPrefix   string `json:"apiPrefix"`
		InboundAuth string `json:"inboundAuth"`
		HealthPath  string `json:"healthPath"`
		AdminPath   string `json:"adminPath"`
	} `json:"transport"`
	Projection struct {
		ToolCount int      `json:"toolCount"`
		Tools     []string `json:"tools"`
	} `json:"projection"`
	Upstreams []adminUpstreamResponse `json:"upstreams,omitempty"`
	Config    adminConfigResponse     `json:"config"`
	Reload    adminReloadResponse     `json:"reload"`
}

type adminUpstreamResponse struct {
	Kind string `json:"kind"`
	Mode string `json:"mode"`
	// Auth names the scheme, never the credential. Whether a proxy actually
	// presents one is otherwise invisible from every surface this exposes.
	Auth string `json:"auth,omitempty"`
}

// upstreamAuthScheme reuses spec mode's name for the same mechanism.
func upstreamAuthScheme(headers []UpstreamHeader) string {
	if len(headers) == 0 {
		return "none"
	}
	return "header-token"
}

type adminConfigResponse struct {
	AuthScheme string `json:"authScheme"`
	AuthHeader string `json:"authHeader,omitempty"`
	AuthPrefix string `json:"authPrefix,omitempty"`
	// OAuth2Clients names the declared minted clients. Names only: never an
	// endpoint, never a client secret, never a token. An operator otherwise
	// cannot tell a server that mints from one that reads.
	OAuth2Clients []string `json:"oauth2Clients,omitempty"`
	BaseURLMode   string   `json:"baseUrlMode"`
	RestrictCount int      `json:"restrictCount"`
}

type adminReloadResponse struct {
	Mode     string `json:"mode"`
	Endpoint string `json:"endpoint"`
	Status   string `json:"status"`
}

func (s *Server) adminDescribe() adminDescribeResponse {
	out := adminDescribeResponse{}
	out.Server.Name = s.name
	out.Server.Spec = s.specName()
	out.Server.SpecPath = s.specPath
	out.Transport.Mode = transportMode
	out.Transport.MCPPath = "/mcp"
	out.Transport.APIPrefix = apiPrefix
	out.Transport.InboundAuth = "deployment-owned"
	out.Transport.HealthPath = "/healthz"
	out.Transport.AdminPath = "/admin"
	out.Projection.ToolCount = len(s.tools)
	out.Projection.Tools = projectedToolNames(s.tools)
	if len(s.upstreams) > 0 {
		out.Upstreams = s.upstreams
	} else {
		out.Upstreams = adminUpstreams(s.cfg)
	}
	out.Config.AuthScheme = s.cfg.Auth.Scheme
	out.Config.AuthHeader = s.cfg.Auth.Header
	out.Config.AuthPrefix = s.cfg.Auth.Prefix
	out.Config.OAuth2Clients = s.OAuth2ClientNames()
	out.Config.BaseURLMode = adminBaseURLMode(s.cfg)
	out.Config.RestrictCount = len(s.cfg.Restrict)
	out.Reload = adminReloadResponse{
		Mode:     "restart-only",
		Endpoint: adminReloadPath,
		Status:   "restart-required",
	}
	return out
}

func adminUpstreams(cfg opcore.RuntimeConfig) []adminUpstreamResponse {
	if strings.TrimSpace(cfg.BaseURL) == "" && cfg.BaseURLValue.IsZero() {
		return nil
	}
	mode := "static"
	if !cfg.BaseURLValue.IsZero() {
		mode = "value-chain"
	}
	return []adminUpstreamResponse{{Kind: "base-url", Mode: mode}}
}

func adminBaseURLMode(cfg opcore.RuntimeConfig) string {
	switch {
	case !cfg.BaseURLValue.IsZero():
		return "value-chain"
	case strings.TrimSpace(cfg.BaseURL) != "":
		return "static"
	default:
		return "unset"
	}
}

func projectedToolNames(tools []*mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		out = append(out, tool.Name)
	}
	return out
}

func (s *Server) serveAdminDescribe(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.adminDescribe())
}

func (s *Server) serveAdminReload(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(adminReloadResponse{
		Mode:     "restart-only",
		Endpoint: adminReloadPath,
		Status:   "restart-required",
	})
}
