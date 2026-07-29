package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCSRFProtection(t *testing.T) {
	handler := csrfProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, csrfToken(r))
	}))

	getReq := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	getRes := httptest.NewRecorder()
	handler.ServeHTTP(getRes, getReq)

	if getRes.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getRes.Code, http.StatusOK)
	}
	cookies := getRes.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want 1", len(cookies))
	}
	token := getRes.Body.String()

	form := url.Values{"_csrf": {token}}
	postReq := httptest.NewRequest(http.MethodPost, "http://localhost/", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(cookies[0])
	postRes := httptest.NewRecorder()
	handler.ServeHTTP(postRes, postReq)

	if postRes.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want %d", postRes.Code, http.StatusOK)
	}
}

func TestCSRFProtectionRejectsMissingToken(t *testing.T) {
	handler := csrfProtection(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("protected handler was called")
	}))

	req := httptest.NewRequest(http.MethodPost, "http://localhost/", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusForbidden)
	}
}

func TestCSRFProtectionReplacesInvalidCookie(t *testing.T) {
	handler := csrfProtection(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, csrfToken(r))
	}))

	getReq := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	getReq.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "not-a-valid-token"})
	getRes := httptest.NewRecorder()
	handler.ServeHTTP(getRes, getReq)

	if getRes.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", getRes.Code, http.StatusOK)
	}
	cookies := getRes.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != csrfCookieName {
		t.Fatalf("replacement cookies = %#v", cookies)
	}
	if !validCSRFToken(cookies[0].Value) {
		t.Fatalf("replacement token %q is invalid", cookies[0].Value)
	}
	if bodyToken := getRes.Body.String(); bodyToken != cookies[0].Value {
		t.Fatalf("context token = %q, cookie token = %q", bodyToken, cookies[0].Value)
	}

	form := url.Values{"_csrf": {cookies[0].Value}}
	postReq := httptest.NewRequest(http.MethodPost, "http://localhost/", strings.NewReader(form.Encode()))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.AddCookie(cookies[0])
	postRes := httptest.NewRecorder()
	handler.ServeHTTP(postRes, postReq)
	if postRes.Code != http.StatusOK {
		t.Fatalf("POST with replacement cookie status = %d, want %d", postRes.Code, http.StatusOK)
	}
}

func TestHostGuard(t *testing.T) {
	allowed := map[string]struct{}{"localhost": {}}
	handler := hostGuard(allowed, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://evil.example/", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}
