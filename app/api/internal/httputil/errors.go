package httputil

import (
	"encoding/json"
	"errors"
	"net/http"
)

type AppError struct {
	Status  int
	Message string
}

func (err AppError) Error() string {
	return err.Message
}

func NewAppError(status int, message string) AppError {
	return AppError{Status: status, Message: message}
}

type errorResponse struct {
	Error string `json:"error"`
}

func WriteJSONError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error: message,
	})
}

func WriteAppError(w http.ResponseWriter, err error) {
	var appErr AppError
	if errors.As(err, &appErr) {
		WriteJSONError(w, appErr.Status, appErr.Message)
		return
	}
	WriteJSONError(w, http.StatusInternalServerError, err.Error())
}
