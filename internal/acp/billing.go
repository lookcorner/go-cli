package acp

import (
	"context"
	"errors"

	"github.com/lookcorner/go-cli/internal/billing"
)

func (s *Server) handleBilling(ctx context.Context, incoming message) {
	service := billing.Service{
		AuthPath: s.Auth.Path, AuthScope: s.Auth.Scope, BaseURL: s.Auth.ProxyBaseURL,
		HTTP: s.Auth.HTTP, TokenProvider: s.Auth.TokenProvider, Metadata: s.BillingMeta,
	}
	if incoming.Method == "x.ai/auto-topup-rule" {
		result, err := service.FetchAutoTopup(ctx)
		if err != nil {
			s.respondBillingError(incoming, err)
			return
		}
		s.respond(incoming.ID, result)
		return
	}
	result, err := service.FetchBilling(ctx)
	if err != nil {
		s.respondBillingError(incoming, err)
		return
	}
	s.respond(incoming.ID, result)
}

func (s *Server) respondBillingError(incoming message, err error) {
	var billingErr *billing.Error
	if errors.As(err, &billingErr) && billingErr.Authentication {
		s.respondErrorData(incoming.ID, -32000, "Authentication required", err.Error())
		return
	}
	s.respondErrorData(incoming.ID, -32603, "Internal error", err.Error())
}
