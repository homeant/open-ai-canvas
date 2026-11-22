package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yingce/backend/internal/service"

	"github.com/gin-gonic/gin"
)

func TestRegisterCanvasAPIExposesOpenAPIAndProjects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterCanvasAPI(router.Group("/api"), &service.Service{})

	wanted := map[string]bool{
		"GET /api/openapi.yaml":                                             false,
		"GET /api/auth/settings":                                            false,
		"POST /api/auth/login":                                              false,
		"POST /api/auth/register":                                           false,
		"GET /api/admin/settings/turnstile":                                 false,
		"PATCH /api/admin/settings/turnstile":                               false,
		"GET /api/workbench/tools":                                          false,
		"GET /api/workbench/tasks":                                          false,
		"POST /api/workbench/tasks":                                         false,
		"GET /api/projects":                                                 false,
		"GET /api/teams":                                                    false,
		"GET /api/teams/credit-overview":                                    false,
		"POST /api/teams/:teamId/groups":                                    false,
		"POST /api/team-invitations/:id/accept":                             false,
		"GET /api/teams/:teamId/wallet":                                     false,
		"POST /api/teams/:teamId/asset-publications":                        false,
		"POST /api/teams/:teamId/workspace/tasks/quote":                     false,
		"PATCH /api/teams/:teamId/workspace/canvas-projects/:id/operations": false,
		"GET /api/teams/:teamId/workspace/assets":                           false,
		"POST /api/tasks":                                                   false,
		"POST /api/tasks/:id/recover-media":                                 false,
		"GET /api/resources":                                                false,
		"GET /api/skills/presets":                                           false,
		"GET /api/agent/skills/usage":                                       false,
		"GET /api/chatgpt-plan":                                             false,
		"POST /api/chatgpt-plan/start":                                      false,
		"GET /api/chatgpt-plan/models":                                      false,
		"PUT /api/chatgpt-plan/models":                                      false,
		"POST /api/chatgpt-plan/disconnect":                                 false,
	}
	for _, route := range router.Routes() {
		if strings.HasPrefix(route.Path, "/api/admin/chatgpt-plan") {
			t.Errorf("personal authorization route remains under public administration: %s", route.Path)
		}
		key := route.Method + " " + route.Path
		if _, exists := wanted[key]; exists {
			wanted[key] = true
		}
	}
	for route, found := range wanted {
		if !found {
			t.Errorf("route %s is not registered", route)
		}
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "openapi: 3.0.3") || !strings.Contains(recorder.Body.String(), "url: /api") {
		t.Fatalf("openapi.yaml status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "/admin/chatgpt-plan") || !strings.Contains(recorder.Body.String(), "  /chatgpt-plan:") {
		t.Fatal("OpenAPI must publish personal authorization routes and remove the old admin contract")
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/skills/presets", nil))
	if recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), `"presets"`) {
		t.Fatalf("skills presets status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
