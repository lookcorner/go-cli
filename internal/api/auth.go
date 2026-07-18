package api

import (
	"context"
	"io"
	"net/http"
)

type TokenProvider func(context.Context, string) (string, error)

func sendAuthenticated(
	ctx context.Context,
	httpClient *http.Client,
	staticToken string,
	provider TokenProvider,
	build func(string) (*http.Request, error),
) (*http.Response, error) {
	token := staticToken
	if provider != nil {
		var err error
		token, err = provider(ctx, "")
		if err != nil {
			return nil, err
		}
	}
	request, err := build(token)
	if err != nil {
		return nil, err
	}
	response, err := httpClient.Do(request)
	if err != nil || response.StatusCode != http.StatusUnauthorized || provider == nil {
		return response, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	response.Body.Close()
	token, err = provider(ctx, token)
	if err != nil {
		return nil, err
	}
	request, err = build(token)
	if err != nil {
		return nil, err
	}
	return httpClient.Do(request)
}
