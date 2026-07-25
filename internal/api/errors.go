package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

type HTTPError struct {
	Service    string
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s returned %s: %s", e.Service, e.Status, e.Body)
}

func readHTTPError(service string, response *http.Response) error {
	limited, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return &HTTPError{
		Service: service, StatusCode: response.StatusCode,
		Status: response.Status, Body: strings.TrimSpace(string(limited)),
	}
}
