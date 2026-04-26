package groupconfig

import (
	"net/http"

	"github.com/zightch/frp/frps/internal/api/httpx"
)

type SessionGuard func(http.ResponseWriter, *http.Request) bool

type Handler struct {
	service        *Service
	requireSession SessionGuard
}

func NewHandler(service *Service, requireSession SessionGuard) *Handler {
	return &Handler{
		service:        service,
		requireSession: requireSession,
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}

	mux.HandleFunc("/api/v1/proxy-groups", h.handleProxyGroups)
	mux.HandleFunc("/api/v1/proxy-groups/", h.handleProxyGroupResource)
	mux.HandleFunc("/api/v1/tunnels", h.handleTunnels)
	mux.HandleFunc("/api/v1/tunnels/", h.handleTunnelResource)
	mux.HandleFunc("/api/v1/local-ips", h.handleLocalIPs)
}

func (h *Handler) handleProxyGroups(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	switch request.Method {
	case http.MethodGet:
		items, err := service.ListProxyGroups(request.Context())
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var payload ProxyGroupCreateRequest
		if err := httpx.DecodeJSONBody(request, &payload); err != nil {
			httpx.WriteError(writer, err)
			return
		}

		item, key, err := service.CreateProxyGroup(request.Context(), payload)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusCreated, map[string]any{
			"item": item,
			"key":  key,
		})
	default:
		httpx.WriteMethodNotAllowed(writer)
	}
}

func (h *Handler) handleProxyGroupResource(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	id, suffix, err := httpx.ParseResourcePath(request.URL.Path, "/api/v1/proxy-groups/")
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}

	switch {
	case suffix == "" && request.Method == http.MethodPatch:
		var payload ProxyGroupPatchRequest
		if err := httpx.DecodeJSONBody(request, &payload); err != nil {
			httpx.WriteError(writer, err)
			return
		}

		item, err := service.UpdateProxyGroup(request.Context(), id, payload)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"item": item})
	case suffix == "" && request.Method == http.MethodDelete:
		if err := service.DeleteProxyGroup(request.Context(), id); err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"deleted": true})
	case suffix == "key" && request.Method == http.MethodPost:
		item, key, err := service.RotateProxyGroupKey(request.Context(), id)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{
			"item": item,
			"key":  key,
		})
	default:
		httpx.WriteMethodNotAllowed(writer)
	}
}

func (h *Handler) handleTunnels(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	switch request.Method {
	case http.MethodGet:
		items, err := service.ListTunnels(request.Context())
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var payload TunnelRequest
		if err := httpx.DecodeJSONBody(request, &payload); err != nil {
			httpx.WriteError(writer, err)
			return
		}

		item, err := service.CreateTunnel(request.Context(), payload)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusCreated, map[string]any{"item": item})
	default:
		httpx.WriteMethodNotAllowed(writer)
	}
}

func (h *Handler) handleTunnelResource(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	id, suffix, err := httpx.ParseResourcePath(request.URL.Path, "/api/v1/tunnels/")
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}
	if suffix != "" {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusNotFound, Message: "resource not found"})
		return
	}

	switch request.Method {
	case http.MethodPatch:
		var payload TunnelRequest
		if err := httpx.DecodeJSONBody(request, &payload); err != nil {
			httpx.WriteError(writer, err)
			return
		}

		item, err := service.UpdateTunnel(request.Context(), id, payload)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"item": item})
	case http.MethodDelete:
		if err := service.DeleteTunnel(request.Context(), id); err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"deleted": true})
	default:
		httpx.WriteMethodNotAllowed(writer)
	}
}

func (h *Handler) handleLocalIPs(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	if request.Method != http.MethodGet {
		httpx.WriteMethodNotAllowed(writer)
		return
	}

	items, err := service.ListLocalIPs()
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) requireManagementSession(writer http.ResponseWriter, request *http.Request) bool {
	if h == nil || h.requireSession == nil {
		return true
	}
	return h.requireSession(writer, request)
}

func (h *Handler) requireService(writer http.ResponseWriter) *Service {
	if h == nil || h.service == nil {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "management store is unavailable"})
		return nil
	}
	return h.service
}
