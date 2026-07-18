package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

func TestCoreCastHTTPConfirmationGateAndFoundationPublish(t *testing.T) {
	server := NewServer(testWebConfig(t), assets.Load("default"), filepath.Join(testTempDir(t), "runtime"))
	defer server.Close()
	manifest, err := server.store.CreateProject("Core Cast Gate")
	if err != nil {
		t.Fatal(err)
	}
	fake := installFakeSession(t, server, manifest)
	session, _, err := server.sessions.Open(manifest.ID)
	if err != nil {
		t.Fatal(err)
	}
	session.cocreate = readyCoreCastWebSession()

	candidate := completeWebCoreCast()
	update := coreCastRequest(t, server, http.MethodPut, manifest.ID, "cocreate/core-cast", map[string]any{
		"expected_revision": 0,
		"core_cast":         candidate,
	})
	if update.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", update.Code, update.Body.String())
	}
	state := decodeCoreCastState(t, update)
	if state.CanStart || state.CastConfirmed || state.CoreCast == nil {
		t.Fatalf("unconfirmed state = %+v", state)
	}
	unknown := coreCastRequest(t, server, http.MethodPut, manifest.ID, "cocreate/core-cast", map[string]any{
		"expected_revision": state.CoreCast.Revision,
		"core_cast":         candidate,
		"can_start":         true,
	})
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown can_start field status=%d body=%s", unknown.Code, unknown.Body.String())
	}

	stale := coreCastRequest(t, server, http.MethodPost, manifest.ID, "cocreate/core-cast/confirm", map[string]any{
		"expected_revision": state.CoreCast.Revision - 1,
		"content_signature": state.CastSignature,
	})
	if stale.Code != http.StatusConflict || !bytes.Contains(stale.Body.Bytes(), []byte(`"code":"core_cast_revision_conflict"`)) {
		t.Fatalf("stale confirm status=%d body=%s", stale.Code, stale.Body.String())
	}

	confirmed := coreCastRequest(t, server, http.MethodPost, manifest.ID, "cocreate/core-cast/confirm", map[string]any{
		"expected_revision": state.CoreCast.Revision,
		"content_signature": state.CastSignature,
	})
	if confirmed.Code != http.StatusOK {
		t.Fatalf("confirm status=%d body=%s", confirmed.Code, confirmed.Body.String())
	}
	state = decodeCoreCastState(t, confirmed)
	if !state.CanStart || !state.CastConfirmed {
		t.Fatalf("confirmed state = %+v", state)
	}

	candidate.Members[0].Character.Arc = "a changed arc"
	changed := coreCastRequest(t, server, http.MethodPut, manifest.ID, "cocreate/core-cast", map[string]any{
		"expected_revision": state.CoreCast.Revision,
		"core_cast":         candidate,
	})
	if changed.Code != http.StatusOK {
		t.Fatalf("changed update status=%d body=%s", changed.Code, changed.Body.String())
	}
	state = decodeCoreCastState(t, changed)
	if state.CanStart || state.CastConfirmed {
		t.Fatal("semantic edit did not invalidate confirmation")
	}

	bypass := coreCastRequest(t, server, http.MethodPost, manifest.ID, "cocreate/commit", map[string]any{"can_start": true})
	if bypass.Code == http.StatusOK || fake.prepareRulesCalls != 0 {
		t.Fatalf("forged commit bypassed gate: status=%d calls=%d body=%s", bypass.Code, fake.prepareRulesCalls, bypass.Body.String())
	}

	confirmed = coreCastRequest(t, server, http.MethodPost, manifest.ID, "cocreate/core-cast/confirm", map[string]any{
		"expected_revision": state.CoreCast.Revision,
		"content_signature": state.CastSignature,
	})
	state = decodeCoreCastState(t, confirmed)
	commit := coreCastRequest(t, server, http.MethodPost, manifest.ID, "cocreate/commit", map[string]any{})
	if commit.Code != http.StatusOK {
		t.Fatalf("commit status=%d body=%s", commit.Code, commit.Body.String())
	}
	formal, err := storepkg.NewStore(manifest.OutputDir).Foundation.Load()
	if err != nil || len(formal.Characters) != 1 || formal.Characters[0].Arc != "a changed arc" {
		t.Fatalf("published foundation=%+v err=%v", formal, err)
	}
}

func TestOldCheckpointWithoutCoreCastRestoresBlocked(t *testing.T) {
	checkpoint := webCoCreateCheckpoint{
		Version: webCoCreateCheckpointVersion,
		Kind:    webCoCreateKindNormal,
		Session: startup.CoCreateSnapshot{
			History:     []host.CoCreateMessage{{Role: "user", Content: "idea"}, {Role: "assistant", Content: "<reply>ok</reply><draft>draft</draft><ready>true</ready><suggestions></suggestions>"}},
			DraftPrompt: "draft", DraftHistoryLen: 2, Ready: true,
		},
	}
	state, err := webCoCreateSessionFromCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	api := state.apiState()
	if api.CanStart || api.CastConfirmed || api.CastCompletion.Complete {
		t.Fatalf("legacy checkpoint bypassed core cast gate: %+v", api)
	}
}

func readyCoreCastWebSession() *webCoCreateSession {
	return &webCoCreateSession{
		kind: webCoCreateKindNormal,
		session: startup.NewCoCreateSessionFromSnapshot(startup.CoCreateSnapshot{
			History:     []host.CoCreateMessage{{Role: "user", Content: "idea"}, {Role: "assistant", Content: "<reply>ok</reply><draft>draft</draft><ready>true</ready><suggestions></suggestions>"}},
			DraftPrompt: "draft", DraftHistoryLen: 2, Ready: true,
		}),
	}
}

func completeWebCoreCast() domain.CoreCastContract {
	return domain.CoreCastContract{
		Version: domain.CoreCastContractVersion, Mode: domain.CoreCastModeNormal,
		Members: []domain.CoreCastMember{{
			Character:  domain.Character{ID: "lin", Name: "Lin", Role: "hero", Goal: "save home", Motivation: "duty", Conflict: "fear", Arc: "accept leadership", Traits: []string{"brave"}, Constraints: []string{"will not betray friends"}},
			Importance: domain.CoreCastImportanceProtagonist, Origin: domain.CoreCastOriginOriginal, MainlineFunction: "drives the central conflict", NoCoreRelationships: true,
		}},
	}
}

func coreCastRequest(t *testing.T, server *Server, method, projectID, action string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "/api/projects/"+projectID+"/"+action, bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeCoreCastState(t *testing.T, rec *httptest.ResponseRecorder) webCoCreateState {
	t.Helper()
	var response struct {
		CoCreate webCoCreateState `json:"cocreate"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	return response.CoCreate
}
