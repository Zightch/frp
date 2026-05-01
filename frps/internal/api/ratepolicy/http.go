package ratepolicy

import (
	"net/http"
	"strconv"
	"strings"

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

	mux.HandleFunc("/api/v1/rate-policies", h.handleRatePolicies)
	mux.HandleFunc("/api/v1/rate-policies/", h.handleRatePolicyResource)
}

func (h *Handler) handleRatePolicies(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}
	service := h.requireService(writer)
	if service == nil {
		return
	}

	switch request.Method {
	case http.MethodGet:
		items, err := service.ListRatePolicies(request.Context())
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var payload RatePolicyRequest
		if err := httpx.DecodeJSONBody(request, &payload); err != nil {
			httpx.WriteError(writer, err)
			return
		}

		item, err := service.CreateRatePolicy(request.Context(), payload)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusCreated, map[string]any{"item": item})
	default:
		httpx.WriteMethodNotAllowed(writer)
	}
}

func (h *Handler) handleRatePolicyResource(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}
	service := h.requireService(writer)
	if service == nil {
		return
	}

	resource, err := parseRatePolicyResource(request.URL.Path)
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}

	switch {
	case resource.kind == ratePolicyResourceKindPolicy && request.Method == http.MethodPatch:
		var payload RatePolicyRequest
		if err := httpx.DecodeJSONBody(request, &payload); err != nil {
			httpx.WriteError(writer, err)
			return
		}
		item, err := service.UpdateRatePolicy(request.Context(), resource.policyID, payload)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"item": item})
	case resource.kind == ratePolicyResourceKindPolicy && request.Method == http.MethodDelete:
		if err := service.DeleteRatePolicy(request.Context(), resource.policyID); err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"deleted": true})
	case resource.kind == ratePolicyResourceKindBindings && request.Method == http.MethodGet:
		items, err := service.ListRatePolicyBindings(request.Context(), resource.policyID)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"items": items})
	case resource.kind == ratePolicyResourceKindBindings && request.Method == http.MethodPut:
		var payload RatePolicyBindingsUpdateRequest
		if err := httpx.DecodeJSONBody(request, &payload); err != nil {
			httpx.WriteError(writer, err)
			return
		}
		items, err := service.UpdateRatePolicyBindings(request.Context(), resource.policyID, payload)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"items": items})
	default:
		httpx.WriteMethodNotAllowed(writer)
	}
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

type ratePolicyResourceKind int

const (
	ratePolicyResourceKindPolicy ratePolicyResourceKind = iota + 1
	ratePolicyResourceKindBindings
)

type ratePolicyResource struct {
	kind     ratePolicyResourceKind
	policyID int64
}

func parseRatePolicyResource(path string) (ratePolicyResource, error) {
	const prefix = "/api/v1/rate-policies/"

	if !strings.HasPrefix(path, prefix) {
		return ratePolicyResource{}, &httpx.Error{Status: http.StatusNotFound, Message: "resource not found"}
	}

	remainder := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if remainder == "" {
		return ratePolicyResource{}, &httpx.Error{Status: http.StatusNotFound, Message: "resource not found"}
	}

	parts := strings.Split(remainder, "/")
	if len(parts) == 0 || len(parts) > 2 {
		return ratePolicyResource{}, &httpx.Error{Status: http.StatusNotFound, Message: "resource not found"}
	}

	policyID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || policyID <= 0 {
		return ratePolicyResource{}, &httpx.Error{Status: http.StatusNotFound, Message: "resource not found"}
	}

	if len(parts) == 1 {
		return ratePolicyResource{kind: ratePolicyResourceKindPolicy, policyID: policyID}, nil
	}
	if parts[1] != "bindings" {
		return ratePolicyResource{}, &httpx.Error{Status: http.StatusNotFound, Message: "resource not found"}
	}
	return ratePolicyResource{kind: ratePolicyResourceKindBindings, policyID: policyID}, nil
}
