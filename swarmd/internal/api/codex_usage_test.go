package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/provider/codex"
)

type fakeCodexAccountClient struct {
	consume      codex.ConsumeResetCreditRequest
	usageErr     error
	resetCredits codex.ResetCredits
}

func (f *fakeCodexAccountClient) GetAccountUsage(context.Context) (codex.AccountUsage, error) {
	return codex.AccountUsage{PlanType: "plus"}, f.usageErr
}
func (f *fakeCodexAccountClient) GetResetCredits(context.Context) (codex.ResetCredits, error) {
	if f.resetCredits.Credits == nil {
		return codex.ResetCredits{AvailableCount: 1}, nil
	}
	return f.resetCredits, nil
}
func (f *fakeCodexAccountClient) ConsumeResetCredit(_ context.Context, req codex.ConsumeResetCreditRequest) (codex.ConsumeResetCreditResponse, error) {
	f.consume = req
	return codex.ConsumeResetCreditResponse{Code: "reset", WindowsReset: 2}, nil
}

func TestCodexConsumeResetCreditRequiresIdentityAndFields(t *testing.T) {
	fake := &fakeCodexAccountClient{}
	server := &Server{codexAccount: fake}

	missingIdentity := httptest.NewRecorder()
	server.handleCodexConsumeResetCredit(missingIdentity, httptest.NewRequest(http.MethodPost, "/v1/codex/account/reset-credits/consume", strings.NewReader(`{"credit_id":"credit-1","idempotency_key":"request-1"}`)))
	if missingIdentity.Code != http.StatusUnauthorized {
		t.Fatalf("missing identity status = %d", missingIdentity.Code)
	}

	missingKey := httptest.NewRecorder()
	req := requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/codex/account/reset-credits/consume", strings.NewReader(`{"credit_id":"credit-1"}`)))
	server.handleCodexConsumeResetCredit(missingKey, req)
	if missingKey.Code != http.StatusBadRequest {
		t.Fatalf("missing key status = %d", missingKey.Code)
	}

	ok := httptest.NewRecorder()
	req = requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/codex/account/reset-credits/consume", strings.NewReader(`{"credit_id":"credit-1","idempotency_key":"request-1"}`)))
	server.handleCodexConsumeResetCredit(ok, req)
	if ok.Code != http.StatusOK || fake.consume.CreditID != "credit-1" || fake.consume.RedeemRequestID != "request-1" {
		t.Fatalf("status=%d consume=%+v body=%s", ok.Code, fake.consume, ok.Body.String())
	}
}

func TestCodexAccountReadHandlersRequireIdentityAndMethods(t *testing.T) {
	fake := &fakeCodexAccountClient{resetCredits: codex.ResetCredits{Credits: []codex.ResetCredit{{ID: "credit-1"}}, AvailableCount: 1}}
	server := &Server{codexAccount: fake}

	unauthorized := httptest.NewRecorder()
	server.handleCodexAccountUsage(unauthorized, httptest.NewRequest(http.MethodGet, "/v1/codex/account/usage", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing identity status = %d", unauthorized.Code)
	}

	method := httptest.NewRecorder()
	server.handleCodexResetCredits(method, requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/codex/account/reset-credits", nil)))
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d", method.Code)
	}

	ok := httptest.NewRecorder()
	server.handleCodexResetCredits(ok, requestWithTestPrincipal(httptest.NewRequest(http.MethodGet, "/v1/codex/account/reset-credits", nil)))
	if ok.Code != http.StatusOK || !strings.Contains(ok.Body.String(), `"id":"credit-1"`) {
		t.Fatalf("status=%d body=%s", ok.Code, ok.Body.String())
	}
}

func TestCodexConsumeResetCreditRejectsMalformedAndOversizedFields(t *testing.T) {
	server := &Server{codexAccount: &fakeCodexAccountClient{}}
	cases := []string{
		`{"credit_id":"credit-1","idempotency_key":"request-1"} trailing`,
		`{"credit_id":"","idempotency_key":"request-1"}`,
		`{"credit_id":"credit-1","idempotency_key":"` + strings.Repeat("x", 257) + `"}`,
	}
	for _, body := range cases {
		recorder := httptest.NewRecorder()
		server.handleCodexConsumeResetCredit(recorder, requestWithTestPrincipal(httptest.NewRequest(http.MethodPost, "/v1/codex/account/reset-credits/consume", strings.NewReader(body))))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d", body, recorder.Code)
		}
	}
}

func TestCodexAccountErrorsAreSanitized(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeCodexAccountError(recorder, &codex.AccountAPIError{StatusCode: http.StatusUnauthorized})
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "token") || strings.Contains(recorder.Body.String(), "401") {
		t.Fatalf("response leaked upstream details: %s", recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	writeCodexAccountError(recorder, errors.New("codex auth not configured"))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("auth status = %d", recorder.Code)
	}
}
