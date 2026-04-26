package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type ResponseError interface {
	error
	HTTPStatus() int
	HTTPCode() string
	HTTPDetails() any
}

type Error struct {
	Status  int
	Message string
	Code    string
	Details any
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *Error) HTTPStatus() int {
	if e == nil || e.Status == 0 {
		return http.StatusInternalServerError
	}
	return e.Status
}

func (e *Error) HTTPCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func (e *Error) HTTPDetails() any {
	if e == nil {
		return nil
	}
	return e.Details
}

func ParseResourcePath(path, prefix string) (int64, string, error) {
	if !strings.HasPrefix(path, prefix) {
		return 0, "", &Error{Status: http.StatusNotFound, Message: "resource not found"}
	}

	remainder := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if remainder == "" {
		return 0, "", &Error{Status: http.StatusNotFound, Message: "resource not found"}
	}

	parts := strings.Split(remainder, "/")
	if len(parts) > 2 {
		return 0, "", &Error{Status: http.StatusNotFound, Message: "resource not found"}
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", &Error{Status: http.StatusNotFound, Message: "resource not found"}
	}

	if len(parts) == 1 {
		return id, "", nil
	}

	return id, parts[1], nil
}

func DecodeJSONBody(request *http.Request, target any) error {
	if request.Body == nil {
		return &Error{Status: http.StatusBadRequest, Message: "request body is required"}
	}
	defer request.Body.Close()

	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return &Error{Status: http.StatusBadRequest, Message: "request body is required"}
		}
		return &Error{Status: http.StatusBadRequest, Message: fmt.Sprintf("invalid json body: %v", err)}
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return &Error{Status: http.StatusBadRequest, Message: "request body must contain a single JSON object"}
	}

	return nil
}

func WriteJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func WriteError(writer http.ResponseWriter, err error) {
	var typedErr ResponseError
	if errors.As(err, &typedErr) {
		payload := map[string]any{"error": err.Error()}
		if code := strings.TrimSpace(typedErr.HTTPCode()); code != "" {
			payload["error_code"] = code
		}
		if details := typedErr.HTTPDetails(); details != nil {
			payload["details"] = details
		}
		WriteJSON(writer, typedErr.HTTPStatus(), payload)
		return
	}

	WriteJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
}

func WriteMethodNotAllowed(writer http.ResponseWriter) {
	WriteError(writer, &Error{Status: http.StatusMethodNotAllowed, Message: "method not allowed"})
}

func IsUniqueConstraintError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "duplicate entry") ||
		strings.Contains(message, "duplicate key")
}
