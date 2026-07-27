package server

import (
	_ "embed"
	"encoding/json"
	"html"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/nkkmnk/pulse/internal/store"
)

//go:embed assets/anime.umd.min.js
var animeJS []byte

type checkpointResponse struct {
	OK bool `json:"ok"`
}

type observeResponse struct {
	OK bool `json:"ok"`
}

func (s *Server) handleContinuityResume(w http.ResponseWriter, r *http.Request) {
	var req store.ResumeQuery
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.cfg.Store.StoreKind() != store.StoreKindPersonal && s.cfg.Store.StoreKind() != store.StoreKindDesk {
		resume, err := s.cfg.Store.BuildResume(req)
		if err != nil {
			http.Error(w, "continuity resume error: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, resume)
		return
	}
	if s.cfg.ProductBindingVerifier == nil {
		resume, err := s.cfg.Store.BuildResume(req)
		if err != nil {
			http.Error(w, "continuity resume error: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, resume)
		return
	}
	authority, ok := s.requireProductBindingAuthority(w, r)
	if !ok {
		return
	}
	if err := s.cfg.Store.RegisterPersonalProjectLabel(
		authority.RepositoryID, filepath.Base(authority.Workspace),
	); err != nil {
		http.Error(w, "continuity resume authority unavailable", http.StatusForbidden)
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		scope, err := s.cfg.Store.PersonalMemoryScopeSnapshotForBinding(
			authority.BindingDigest, authority.RepositoryID,
		)
		if err != nil {
			http.Error(w, "continuity resume authority unavailable", http.StatusForbidden)
			return
		}
		resume, err := s.cfg.Store.BuildResumeForPersonalScope(req, scope)
		if err != nil {
			http.Error(w, "continuity resume error: "+err.Error(), http.StatusBadRequest)
			return
		}
		latest, err := s.cfg.Store.PersonalMemoryScopeSnapshotForBinding(
			authority.BindingDigest, authority.RepositoryID,
		)
		if err == nil && latest.EligibilityRevision == scope.EligibilityRevision {
			writeJSON(w, resume)
			return
		}
	}
	http.Error(w, "continuity eligibility changed; retry", http.StatusConflict)
}

func (s *Server) handleContinuityCheckpoint(w http.ResponseWriter, r *http.Request) {
	if !s.localAutoEnabled() {
		http.Error(w, "continuity checkpoint requires local-auto mode", http.StatusForbidden)
		return
	}
	var req store.ContinuityCheckpoint
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.cfg.Store.SaveCheckpoint(req); err != nil {
		http.Error(w, "continuity checkpoint error: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, checkpointResponse{OK: true})
}

func (s *Server) handleContinuityObserve(w http.ResponseWriter, r *http.Request) {
	if !s.localAutoEnabled() {
		http.Error(w, "continuity observe requires local-auto mode", http.StatusForbidden)
		return
	}
	var req store.ContinuityObservation
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.cfg.Store.SaveObservation(req, s.cfg.Billing.RawCaptureEnabled); err != nil {
		http.Error(w, "continuity observe error: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, observeResponse{OK: true})
}

func (s *Server) handleViewerData(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	budget, _ := strconv.Atoi(r.URL.Query().Get("token_budget"))
	data, err := s.cfg.Store.ViewerData(store.ResumeQuery{
		ThreadID:    r.URL.Query().Get("thread_id"),
		ProjectID:   r.URL.Query().Get("project_id"),
		SessionID:   r.URL.Query().Get("session_id"),
		Host:        r.URL.Query().Get("host"),
		TokenBudget: budget,
	})
	if err != nil {
		http.Error(w, "viewer data error: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, data)
}

func (s *Server) handleAnimeAsset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(animeJS)
}

func (s *Server) handleViewer(w http.ResponseWriter, r *http.Request) {
	key := html.EscapeString(r.URL.Query().Get("key"))
	thread := html.EscapeString(r.URL.Query().Get("thread_id"))
	if thread == "" {
		thread = "default"
	}
	host := r.URL.Query().Get("host")
	if host == "" {
		host = s.cfg.Billing.Host
	}
	hostName := viewerHostDisplayName(host)
	productDestructiveOpsLocked := s.cfg.Store.StoreKind() == store.StoreKindPersonal ||
		s.cfg.Store.StoreKind() == store.StoreKindDesk
	productDestructiveOpsLockedAttr := strconv.FormatBool(productDestructiveOpsLocked)
	firstRunAttr := "false"
	firstRunHTML := ""
	if r.URL.Query().Get("first_run") == "1" {
		firstRunAttr = "true"
		firstRunHTML = viewerFirstRunHTML(hostName, productDestructiveOpsLocked)
	}
	rawRefs := "disabled"
	if s.cfg.Billing.RawCaptureEnabled {
		rawRefs = "enabled"
	}
	backend := "off"
	if s.cfg.Billing.BackendLLMEnabled {
		backend = "on"
	}
	backendLine := "No backend model is running by default."
	if s.cfg.Billing.BackendLLMEnabled {
		backendLine = "Backend model calls are enabled for this local setup."
	}
	rawLine := "Raw transcript capture is off."
	if s.cfg.Billing.RawCaptureEnabled {
		rawLine = "Raw refs are enabled for this local setup."
	}
	wipeControlsHTML := `<div class="controls"><input id="wipe-confirm" placeholder='type: wipe pulse memory'><button id="wipe-button">Wipe</button></div>
        <div id="wipe-status" class="muted"></div>`
	deleteControlsHTML := `<div class="controls"><input id="delete-id" placeholder="pulse:id"><button id="delete-button">Delete</button></div>
        <div id="delete-status" class="muted"></div>`
	deleteScriptHTML := `document.getElementById("delete-button").addEventListener("click", async () => {
  const id = document.getElementById("delete-id").value.trim();
  if (!id) return;
  await postJSON("/memory/delete", { id });
  document.getElementById("delete-status").textContent = "Deleted " + id;
});`
	wipeScriptHTML := `document.getElementById("wipe-button").addEventListener("click", async () => {
  const confirm = document.getElementById("wipe-confirm").value.trim();
  if (confirm !== "wipe pulse memory") {
    document.getElementById("wipe-status").textContent = "Type the exact confirmation phrase.";
    return;
  }
  await postJSON("/memory/wipe", { confirm });
  document.getElementById("wipe-status").textContent = "Wiped Pulse memory and continuity.";
});`
	if productDestructiveOpsLocked {
		deleteControlsHTML = `<button id="delete-button" disabled>OS confirmation required</button>
        <div id="delete-status" class="muted">Product memory deletion stays locked until the privileged OS-backed Pulse surface is active.</div>`
		deleteScriptHTML = ""
		wipeControlsHTML = `<button id="wipe-button" disabled>OS confirmation required</button>
        <div id="wipe-status" class="muted">Product vault wipe stays locked until the privileged OS-backed Pulse surface is active.</div>`
		wipeScriptHTML = ""
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="data:,">
  <title>Pulse Home</title>
  <style>
    :root { color-scheme: light; --ink:#342f38; --muted:#746977; --faint:#9a8f9c; --line:rgba(86,70,86,.14); --glass:rgba(255,255,255,.62); --glass-strong:rgba(255,255,255,.78); --soft-rose:#f7d7d1; --soft-sky:#dcecf8; --soft-mint:#dff1e5; --soft-lilac:#ebe4fb; --accent:#8d6f83; --danger:#8a5961; --ok:#4f7463; --shadow:0 16px 44px rgba(72,58,74,.10); --radius:8px; font:15px/1.5 ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }
    * { box-sizing:border-box; }
    body { margin:0; min-height:100vh; color:var(--ink); background:linear-gradient(135deg,#fff9f8 0%,#f7fbff 42%,#fbf7ff 100%); }
    main { width:min(1320px,100%); margin:0 auto; padding:28px clamp(14px,3vw,38px) 44px; }
    h1, h2, h3, p { margin:0; letter-spacing:0; }
    h1 { max-width:720px; font-size:36px; line-height:1.08; font-weight:690; text-wrap:balance; }
    h2 { font-size:18px; line-height:1.2; font-weight:690; }
    h3 { font-size:15px; line-height:1.25; font-weight:680; }
    p { color:var(--muted); }
    code, pre { font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace; }
    button, input { font:inherit; }
    button { min-height:38px; padding:8px 12px; border-radius:999px; border:1px solid rgba(141,111,131,.22); background:rgba(255,255,255,.78); color:#5f4f5b; cursor:pointer; }
    button:hover { background:rgba(255,255,255,.94); }
    button:focus-visible, input:focus-visible, summary:focus-visible { outline:3px solid rgba(141,111,131,.24); outline-offset:2px; }
    input { width:100%; min-height:42px; padding:9px 12px; border-radius:999px; border:1px solid rgba(86,70,86,.16); background:rgba(255,255,255,.74); color:var(--ink); }
    ul { list-style:none; margin:0; padding:0; display:grid; gap:8px; }
    li { padding:9px 10px; border-radius:var(--radius); background:rgba(255,255,255,.58); border:1px solid rgba(86,70,86,.10); overflow-wrap:anywhere; }
    .surface, .mini-card, .person-card, .metric, details.surface { background:var(--glass); border:1px solid rgba(255,255,255,.72); border-radius:var(--radius); box-shadow:var(--shadow), inset 0 1px 0 rgba(255,255,255,.72); backdrop-filter:blur(18px) saturate(1.16); -webkit-backdrop-filter:blur(18px) saturate(1.16); }
    .surface { padding:18px; min-width:0; }
    .top { display:grid; grid-template-columns:minmax(0,1fr) minmax(340px,.82fr); gap:14px; align-items:start; }
	    .hero { min-height:240px; display:flex; flex-direction:column; justify-content:flex-start; background:linear-gradient(135deg,rgba(255,255,255,.74),rgba(247,215,209,.36)); }
	    .label { display:inline-flex; width:max-content; align-items:center; gap:7px; min-height:28px; padding:5px 9px; border-radius:999px; color:#695a66; background:rgba(255,255,255,.62); border:1px solid rgba(86,70,86,.10); font-size:13px; font-weight:640; }
	    .subtitle { margin-top:14px; max-width:720px; font-size:16px; line-height:1.55; }
	    .pulse-ecg { display:grid; grid-template-columns:108px minmax(0,1fr); gap:12px; align-items:center; margin-top:20px; padding:12px; border-radius:var(--radius); background:rgba(255,255,255,.58); border:1px solid rgba(86,70,86,.10); }
	    .pulse-ecg-track { position:relative; height:42px; overflow:hidden; border-radius:999px; background:linear-gradient(135deg,rgba(247,215,209,.62),rgba(235,228,251,.54)); }
	    .pulse-ecg-track svg { position:absolute; inset:0; width:100%; height:100%; }
	    .pulse-ecg-track polyline { fill:none; stroke:#8f5f73; stroke-width:3.2; stroke-linecap:round; stroke-linejoin:round; filter:drop-shadow(0 0 8px rgba(159,107,126,.36)); animation:pulseGlow 2.8s ease-in-out infinite; }
	    .pulse-ecg-track::after { content:""; position:absolute; top:50%; left:0; width:8px; height:8px; border-radius:999px; background:#9f6b7e; transform:translate(-8px,-50%); box-shadow:0 0 16px rgba(159,107,126,.54); animation:pulseSweep 2.8s ease-out infinite; }
	    .pulse-ecg-copy strong { display:block; color:#4f4650; }
	    .pulse-ecg-copy span { display:block; margin-top:3px; color:var(--muted); font-size:13px; }
	    .status-row { display:flex; flex-wrap:wrap; gap:8px; margin-top:22px; }
    .path-summary { color:var(--muted); font-size:13px; font-weight:560; margin-left:8px; }
    .onboarding-path { margin-top:14px; padding:0; overflow:hidden; }
    .onboarding-path summary, .import-wizard summary { cursor:pointer; padding:16px 18px; font-weight:690; }
    .path-groups { display:grid; grid-template-columns:minmax(0,.75fr) minmax(0,1.25fr); gap:12px; padding:0 18px 18px; }
    .path-group { padding:13px; border-radius:var(--radius); background:rgba(255,255,255,.46); border:1px solid rgba(86,70,86,.10); }
    .path-group h3 { margin-bottom:10px; }
    .stepper { display:grid; grid-template-columns:repeat(6,minmax(0,1fr)); gap:8px; margin-top:14px; }
    .step { min-height:72px; padding:11px; border-radius:var(--radius); background:rgba(255,255,255,.58); border:1px solid rgba(86,70,86,.10); }
    .step b { display:flex; align-items:center; gap:7px; color:#4f4650; font-size:13px; }
    .step b::before { content:attr(data-step); display:inline-flex; align-items:center; justify-content:center; width:22px; height:22px; border-radius:999px; background:rgba(141,111,131,.12); color:#6f5d69; font-size:12px; }
    .step span { display:block; margin-top:6px; color:var(--muted); font-size:12px; line-height:1.35; }
    .resume-summary { margin-top:12px; padding:14px; border-radius:var(--radius); border:1px solid rgba(86,70,86,.12); background:rgba(255,255,255,.66); color:#453d47; font-size:15px; line-height:1.5; }
    .harness-digest { margin-top:12px; padding:12px 14px; border-radius:var(--radius); border:1px solid rgba(86,70,86,.12); background:linear-gradient(135deg,rgba(247,215,209,.34),rgba(235,228,251,.30)); }
    .harness-digest-title { font-weight:680; color:#5f4f5b; font-size:14px; margin-bottom:6px; }
    .harness-digest-list { list-style:none; margin:0; padding:0; }
    .harness-digest-list li { padding:4px 0; color:#453d47; font-size:14px; line-height:1.45; opacity:0; transform:translateY(6px); animation:harnessReveal .5s cubic-bezier(.22,.61,.36,1) forwards; }
    @keyframes harnessReveal { to { opacity:1; transform:translateY(0); } }
    .raw-resume { margin-top:10px; border-radius:var(--radius); background:rgba(255,255,255,.46); border:1px solid rgba(86,70,86,.10); }
    .raw-resume summary { cursor:pointer; padding:11px 13px; color:#5f4f5b; font-weight:660; }
	    .resume-card { margin:0 12px 12px; min-height:160px; max-height:220px; overflow:auto; white-space:pre-wrap; overflow-wrap:anywhere; padding:14px; border-radius:var(--radius); border:1px solid rgba(86,70,86,.12); background:rgba(255,255,255,.66); color:#453d47; }
	    .first-run { margin-top:14px; background:linear-gradient(135deg,rgba(255,255,255,.72),rgba(235,228,251,.42)); }
	    .first-run-grid, .source-scan-grid, .state-signal-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:10px; }
	    .first-memory-card, .feeling-card, .source-scan-card, .state-signal-card, .context-heart { padding:14px; border-radius:var(--radius); background:rgba(255,255,255,.58); border:1px solid rgba(86,70,86,.10); }
	    .feeling-actions, .memory-actions, .state-actions { display:flex; flex-wrap:wrap; gap:7px; margin-top:12px; }
	    .emotion-status, .memory-action-status { margin-top:10px; color:var(--muted); font-size:13px; }
	    .context-heart { margin-top:10px; background:linear-gradient(135deg,rgba(255,255,255,.66),rgba(247,215,209,.32)); }
	    .source-scan-grid { grid-template-columns:repeat(3,minmax(0,1fr)); }
	    .panel-head { display:flex; justify-content:space-between; gap:12px; align-items:flex-start; margin-bottom:14px; }
    .panel-head p { margin-top:5px; font-size:13px; }
    .flow-title { margin:18px 0 10px; font-size:18px; }
    .journey, .action-grid, .activity-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:10px; margin:0 0 14px; }
    .mini-card { padding:13px; }
    .mini-card b { display:block; margin-bottom:5px; font-size:13px; color:#5b4d59; }
    .mini-card p { font-size:13px; line-height:1.42; }
    .metrics { display:grid; grid-template-columns:repeat(6,minmax(0,1fr)); gap:10px; margin:14px 0; }
    .metric { min-height:82px; padding:13px; }
    .metric b { display:block; font-size:26px; line-height:1; }
    .metric span { display:block; margin-top:8px; color:var(--muted); font-size:13px; }
    .primary-grid { display:grid; grid-template-columns:minmax(0,1.12fr) minmax(340px,.88fr); gap:14px; align-items:start; }
    .action-card { padding:16px; border-radius:var(--radius); background:rgba(255,255,255,.58); border:1px solid rgba(86,70,86,.10); }
    .action-card h3 { font-size:17px; }
    .action-card p { margin-top:7px; font-size:14px; }
	.tray-grid { display:grid; gap:10px; }
	.tray-card { padding:14px; border-radius:var(--radius); background:rgba(255,255,255,.62); border:1px solid rgba(86,70,86,.12); }
	.tray-card pre { margin:10px 0; padding:10px; max-height:220px; overflow:auto; white-space:pre-wrap; overflow-wrap:anywhere; border-radius:var(--radius); background:rgba(255,255,255,.72); }
	.tray-meta, .tray-actions { display:flex; flex-wrap:wrap; gap:8px; align-items:center; }
	.tray-actions button { margin-top:0; }
	    .action-card button, .action-card a { display:inline-flex; align-items:center; justify-content:center; min-height:40px; margin-top:12px; padding:8px 13px; border-radius:999px; border:1px solid rgba(141,111,131,.22); background:rgba(255,255,255,.84); color:#5f4f5b; text-decoration:none; }
	    .action-card.primary-action { background:linear-gradient(135deg,rgba(255,255,255,.72),rgba(223,241,229,.54)); }
	    .action-card.primary-action button { background:#6f5d69; border-color:#6f5d69; color:white; box-shadow:0 8px 14px rgba(111,93,105,.14); }
	    .action-card.secondary-action { background:rgba(255,255,255,.42); }
    .activity-row { display:grid; gap:8px; }
    .activity-row code { display:inline; padding:2px 5px; border-radius:6px; background:rgba(255,255,255,.7); }
    .proof-grid, .wizard-grid, .thread-grid, .decision-grid, .gate-grid { display:grid; gap:10px; }
    .proof-grid { grid-template-columns:repeat(3,minmax(0,1fr)); }
    .wizard-grid { grid-template-columns:repeat(5,minmax(0,1fr)); }
    .thread-grid { grid-template-columns:repeat(2,minmax(0,1fr)); }
    .decision-grid { grid-template-columns:repeat(2,minmax(0,1fr)); }
    .gate-grid { grid-template-columns:repeat(2,minmax(0,1fr)); }
    .source-option, .thread-card, .decision-card, .gate-card { padding:13px; border-radius:var(--radius); background:rgba(255,255,255,.58); border:1px solid rgba(86,70,86,.10); }
    .source-option .label { margin-top:10px; }
    .thread-card dl { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:8px; margin:12px 0 0; }
    .thread-card dt { color:var(--muted); font-size:11px; }
    .thread-card dd { margin:2px 0 0; font-weight:690; }
    .decision-actions { display:flex; flex-wrap:wrap; gap:7px; margin-top:12px; }
    .review-status { margin-top:10px; padding:9px 10px; border-radius:var(--radius); background:rgba(255,255,255,.56); border:1px solid rgba(86,70,86,.10); color:#6a5b67; font-size:13px; }
    .gate-card ul { margin-top:10px; }
    .person-cards { display:grid; grid-template-columns:repeat(auto-fit,minmax(240px,1fr)); gap:10px; }
    .person-card { padding:14px; }
    .person-card p { margin-top:6px; font-size:13px; }
    .person-card h4 { margin:12px 0 6px; color:var(--muted); font-size:12px; font-weight:680; }
    .meter { height:6px; margin:10px 0 12px; border-radius:999px; overflow:hidden; background:rgba(86,70,86,.10); }
    .meter span { display:block; height:100%; width:var(--weight,0%); background:linear-gradient(90deg,#d99a8f,#8bb79f); }
    .profile-actions { display:flex; justify-content:flex-end; margin-top:12px; }
    .soft-danger { color:var(--danger); border-color:rgba(138,89,97,.24); background:rgba(255,245,244,.72); }
    .restore-button { color:var(--ok); border-color:rgba(79,116,99,.24); background:rgba(241,250,246,.76); }
    .controls { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:9px; align-items:center; margin-bottom:10px; }
    .list-grid { display:grid; grid-template-columns:repeat(auto-fit,minmax(210px,1fr)); gap:10px; }
    .relationship-map { display:grid; gap:8px; }
    .relationship-link { padding:9px 10px; border-radius:var(--radius); background:rgba(255,255,255,.58); border:1px solid rgba(86,70,86,.10); overflow-wrap:anywhere; }
	    .graph-internals, .import-wizard { margin-top:14px; padding:0; overflow:hidden; }
	    .graph-internals summary, .source-scan-details summary, .state-signal-details summary { cursor:pointer; padding:16px 18px; font-weight:690; }
	    .source-scan-details, .state-signal-details { margin-top:14px; padding:0; overflow:hidden; }
	    .details-body { padding:0 18px 18px; display:grid; gap:12px; }
    .graph-canvas { position:relative; min-height:260px; border-radius:var(--radius); background:linear-gradient(135deg,rgba(255,255,255,.62),rgba(220,236,248,.40)); border:1px solid rgba(86,70,86,.10); overflow:hidden; }
    .entity-chip { position:absolute; min-width:112px; max-width:190px; padding:9px 10px; border-radius:var(--radius); background:rgba(255,255,255,.72); border:1px solid rgba(86,70,86,.12); box-shadow:0 8px 26px rgba(72,58,74,.08); }
    .entity-chip strong { display:block; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
    .entity-chip span { display:block; color:var(--muted); font-size:12px; margin-top:2px; }
    .relationship-svg { position:absolute; inset:0; width:100%; height:100%; pointer-events:none; opacity:.5; }
    .relationship-svg path { fill:none; stroke:#a88ea0; stroke-width:1.4; stroke-linecap:round; stroke-dasharray:5 8; }
    .connect-grid, .trust-tools { display:grid; grid-template-columns:repeat(auto-fit,minmax(220px,1fr)); gap:10px; }
    .source-card { padding:13px; border-radius:var(--radius); background:rgba(255,255,255,.56); border:1px solid rgba(86,70,86,.10); }
    .source-card p { margin-top:6px; min-height:38px; font-size:13px; }
    .copy-command { width:100%; margin-top:8px; font-size:13px; }
    .hidden-row { display:flex; justify-content:space-between; align-items:center; gap:10px; }
    .danger { margin-top:14px; border-color:rgba(138,89,97,.20); background:rgba(255,246,244,.62); }
    .stack { display:grid; gap:10px; }
	    .empty, .muted { color:var(--muted); }
	    .section-gap { margin-top:14px; }
	    @keyframes pulseGlow { 0%,100% { opacity:.68; } 35% { opacity:1; } }
	    @keyframes pulseSweep { 0% { transform:translate(-8px,-50%); opacity:0; } 12% { opacity:1; } 70% { transform:translate(108px,-50%); opacity:1; } 100% { transform:translate(116px,-50%); opacity:0; } }
	    @media (prefers-reduced-motion: reduce) { *, *::before, *::after { scroll-behavior:auto !important; transition:none !important; animation:none !important; } }
		    @media (max-width:980px) { .top, .primary-grid, .path-groups, .first-run-grid { grid-template-columns:1fr; } .stepper { grid-template-columns:repeat(3,minmax(0,1fr)); } .journey, .action-grid, .activity-grid, .metrics, .proof-grid, .thread-grid, .decision-grid, .gate-grid { grid-template-columns:repeat(2,minmax(0,1fr)); } .wizard-grid, .source-scan-grid { grid-template-columns:repeat(3,minmax(0,1fr)); } }
		    @media (max-width:620px) { main { padding:14px 12px 32px; } .surface { padding:16px; } h1 { font-size:28px; } .subtitle { margin-top:10px; font-size:15px; } .pulse-ecg { grid-template-columns:1fr; } .status-row { gap:6px; margin-top:16px; } .hero { min-height:auto; } .panel-head { display:block; } .panel-head .label { margin-top:10px; } .path-summary { display:block; margin:5px 0 0; } .resume-card { min-height:150px; max-height:150px; padding:12px; } .stepper, .journey, .action-grid, .activity-grid, .metrics, .controls, .proof-grid, .wizard-grid, .thread-grid, .decision-grid, .gate-grid, .source-scan-grid, .state-signal-grid { grid-template-columns:1fr; } }
	  </style>
	</head>
	<body data-design="pulse-first-run-home-v0" data-flow="pulse-onboarding-v2" data-first-run="` + firstRunAttr + `" data-destructive-ops-locked="` + productDestructiveOpsLockedAttr + `">
	<main>
  <header class="top">
	    <section class="surface hero">
	      <div>
	        <span class="label">Pulse Home</span>
	        <h1>Pulse keeps the thread</h1>
	        <p class="subtitle">Pulse is working locally. Inspect the next bounded continuity pack, then start a live memory proof. Old chat import can wait.</p>
	      </div>
	      <div class="pulse-ecg" data-motion="pulse-ecg" aria-label="Pulse is breathing locally.">
	        <div class="pulse-ecg-track" aria-hidden="true">
	          <svg viewBox="0 0 108 42" preserveAspectRatio="none">
	            <polyline points="0,24 19,24 26,16 31,30 37,7 44,34 51,24 108,24"></polyline>
	          </svg>
	        </div>
	        <p class="pulse-ecg-copy"><strong>Pulse is breathing locally.</strong><span>` + backendLine + ` ` + rawLine + `</span></p>
	      </div>
	      <div class="status-row" aria-label="Pulse trust status">
        <span class="label"><strong>Pulse is alive</strong></span>
		<span class="label">bound local vault</span>
		<span class="label">harness readiness: see doctor</span>
        <span class="label">raw refs ` + rawRefs + `</span>
        <span class="label">backend LLM ` + backend + `</span>
        <span class="label">local SQLite</span>
        <span class="label">thread ` + thread + `</span>
      </div>
    </section>
    <section class="surface">
      <div class="panel-head">
        <div>
		  <h2>What Pulse will provide next</h2>
          <p>You can inspect this before every new session.</p>
        </div>
        <span class="label" id="resume-budget">bounded</span>
      </div>
	  <div id="resume-summary" class="resume-summary">Pulse will provide: No resume block yet. Start with one proof memory before importing archives.</div>
      <div id="harness-digest-panel" class="harness-digest" hidden>
        <div class="harness-digest-title">🌱 Across your harnesses</div>
        <ul id="harness-digest" class="harness-digest-list"></ul>
      </div>
      <details class="raw-resume">
        <summary>View raw resume block</summary>
        <pre id="resume" class="resume-card">No resume block yet.
	Start a connected harness session or import one small source.
Pulse will show the next resume block here before it is injected.</pre>
      </details>
	    </section>
	  </header>
` + firstRunHTML + `

	<section class="surface section-gap" id="memory-tray-panel">
	  <div class="panel-head"><div><h2>Memory Tray</h2><p>Exact private candidates appear here before canonical commit. Destination is fixed by workspace binding.</p></div><span class="label" id="memory-tray-count">0 candidates</span></div>
	  <div id="memory-tray" class="tray-grid"><p>No private writes are waiting.</p></div>
	</section>

	  <details class="surface onboarding-path">
	    <summary>Onboarding path <span class="path-summary">Bound vault -> Verify harness -> Try first memory -> Import later</span></summary>
    <div class="path-groups">
      <section class="path-group"><h3>Now</h3><div class="stepper" aria-label="Now">
	        <div class="step"><b data-step="1">Bound vault</b><span>Pulse is serving this local vault. Run doctor to verify the active harness MCP and hooks.</span></div>
        <div class="step"><b data-step="2">Proof</b><span>Save one decision and recall it in a fresh session.</span></div>
      </div></section>
      <section class="path-group"><h3>Later</h3><div class="stepper" aria-label="Later">
        <div class="step"><b data-step="3">Import</b><span>Optional old chats, always previewed first.</span></div>
        <div class="step"><b data-step="4">Preview</b><span>Threads, decisions, open loops, and resume size.</span></div>
        <div class="step"><b data-step="5">Review</b><span>Confirm, edit, ignore, mark private, or merge.</span></div>
		<div class="step"><b data-step="6">Resume</b><span>Inspect what the next bound session will receive.</span></div>
      </div></section>
    </div>
  </details>

  <section class="surface section-gap">
	    <div class="panel-head"><div><h2>Choose next step</h2><p>Start with one memory first. Old chats can wait. The fastest proof is one remembered decision across two sessions.</p></div></div>
	    <div class="action-grid">
	      <article class="action-card primary-action" data-primary-action="first-memory-proof">
	        <span class="label">Try first memory</span>
	        <h3 class="section-gap">Start first memory proof</h3>
	        <p>Save one decision, open a fresh bound session, and see Pulse resume it.</p>
	        <button type="button" class="copy-command" data-copy="Remember this in Pulse: Atlas must not own the People Graph; Pulse owns portable continuity memory.">Copy proof prompt</button>
	      </article>
	      <article class="action-card secondary-action" data-secondary-action="import-later">
	        <h3>Import old chats later</h3>
        <p>Optional. Pulse previews threads first and imports nothing until you confirm.</p>
        <button type="button" class="jump-to-import">Open import wizard</button>
      </article>
    </div>
  </section>

  <section class="surface section-gap">
    <div class="panel-head"><div><h2>First memory proof</h2><p>Use this before archive migration. It proves Pulse is useful without importing anything scary.</p></div></div>
    <h3>Proof checklist</h3>
    <div class="proof-grid section-gap">
	  <article class="mini-card"><b>Remember one decision</b><p>Ask the connected harness to save a small project decision into Pulse.</p></article>
	  <article class="mini-card"><b>Open a fresh session</b><p>Start a new bound session without re-explaining the project.</p></article>
      <article class="mini-card"><b>Confirm recall</b><p>Ask what Pulse remembers and compare it with the resume block above.</p></article>
    </div>
    <div class="journey" aria-label="First memory proof">
	      <article class="mini-card"><b>Save one decision</b><p>Ask the connected harness: “Remember that Atlas must not own People Graph.”</p></article>
	      <article class="mini-card"><b>Start fresh</b><p>Open a new bound harness session and ask what Pulse remembers about Atlas and People Graph.</p></article>
    </div>
  </section>

	  <section class="surface section-gap">
	    <div class="panel-head"><div><h2>Activity</h2><p id="activity-summary">No Pulse activity yet. After your first memory proof, this will show what Pulse did.</p></div></div>
	    <div class="activity-grid">
	      <article class="mini-card activity-row"><b>Recent Pulse actions</b><p id="activity-mcp-summary">No tool calls yet. After your first memory proof, you will see memory, resume, and recall actions here.</p></article>
	      <article class="mini-card activity-row"><b>Token economy</b><p>Raw source: not imported yet. Pulse resume: <span id="resume-token-economy">0 tokens</span>. Saved tokens appear after the first source or checkpoint.</p></article>
	    </div>
	    <ul id="activity-log"></ul>
	  </section>

  <details class="surface import-wizard" id="import-wizard">
    <summary>Open import wizard</summary>
    <div class="details-body">
    <div class="panel-head"><div><h2>Import wizard</h2><p>Import stays optional. Pulse shows threads and decisions before anything is committed.</p></div><span class="label">private by default</span></div>
    <section class="section-gap">
      <h3>What should Pulse learn from?</h3>
      <div class="wizard-grid section-gap">
		<article class="source-option"><h3>Claude Code sessions</h3><p>Connector available. Run doctor for actual connection status; sessions are not scanned yet.</p><span class="label">preview later</span></article>
        <article class="source-option"><h3>Codex sessions</h3><p>Connector available. Sessions are not scanned yet.</p><span class="label">preview later</span></article>
        <article class="source-option"><h3>ChatGPT export</h3><p>Archive zip from settings export, never raw-imported.</p><span class="label">waiting</span></article>
        <article class="source-option"><h3>Claude export</h3><p>Downloaded archive, reviewed locally before commit.</p><span class="label">waiting</span></article>
        <article class="source-option"><h3>Manual capsule</h3><p>Paste one structured capsule when no archive exists.</p><span class="label">manual</span></article>
      </div>
    </section>
    <section class="section-gap">
      <h3>Example threads after preview</h3>
      <div class="thread-grid section-gap">
        <article class="thread-card"><h3>Pulse onboarding</h3><p>Sources: Claude Code + Codex. Estimated resume: 840 tokens.</p><dl><div><dt>Decisions</dt><dd>4</dd></div><div><dt>Open loops</dt><dd>2</dd></div><div><dt>Do-not-repeat</dt><dd>1</dd></div><div><dt>Privacy</dt><dd>private</dd></div></dl></article>
        <article class="thread-card"><h3>Garden demo UX</h3><p>Sources: Codex. Estimated resume: 620 tokens.</p><dl><div><dt>Decisions</dt><dd>2</dd></div><div><dt>Open loops</dt><dd>1</dd></div><div><dt>Review</dt><dd>1</dd></div><div><dt>Privacy</dt><dd>private</dd></div></dl></article>
      </div>
    </section>
    <section class="section-gap">
      <h3>Needs your decision</h3>
      <div class="decision-grid section-gap">
        <article class="decision-card"><h3>Pulse found “Cartographer”</h3><p>Suggested: component. Why: appears near map, profile, Garden, and Pulse.</p><p class="review-status" data-review-status>Review actions update this page only.</p><div class="decision-actions"><button data-review-action="confirm" data-review-result="Confirmed as component.">Confirm</button><button data-review-action="edit" data-review-result="Marked for editing before import.">Edit</button><button data-review-action="ignore" data-review-result="Ignored in this preview.">Ignore</button><button data-review-action="merge" data-review-result="Marked for merge.">Merge</button></div></article>
        <article class="decision-card"><h3>Sensitive relationship thread</h3><p>Suggested privacy: private. Keep it available for resume, hidden from public export.</p><p class="review-status" data-review-status>Review actions update this page only.</p><div class="decision-actions"><button data-review-action="private" data-review-result="Marked private in this preview.">Mark private</button><button data-review-action="edit" data-review-result="Marked for editing before import.">Edit</button><button data-review-action="ignore" data-review-result="Ignored in this preview.">Ignore</button></div></article>
      </div>
    </section>
    <section class="section-gap">
      <h3>Import gate preview</h3>
      <div class="gate-grid section-gap">
        <article class="gate-card"><h3>Will save</h3><ul><li>2 threads</li><li>6 decisions</li><li>3 open loops</li><li>1 do-not-repeat warning</li></ul></article>
        <article class="gate-card"><h3>Will not save</h3><ul><li>raw transcript</li><li>local paths</li><li>secrets or tokens</li><li>unreviewed ambiguous people</li></ul><p class="section-gap"><strong>Default privacy: private</strong></p></article>
      </div>
    </section>
    </div>
  </details>

  <details class="surface graph-internals">
    <summary>Thread map and source details</summary>
    <div class="details-body">
      <div class="metrics" aria-label="Source detail counts">
        <div class="metric"><b id="graph-count-profiles">0</b><span>trusted people</span></div>
        <div class="metric"><b id="graph-count-people">0</b><span>people found</span></div>
        <div class="metric"><b id="graph-count-memories">0</b><span>saved memory</span></div>
        <div class="metric"><b id="graph-count-emotions">0</b><span>emotional anchors</span></div>
        <div class="metric"><b id="graph-count-relationships">0</b><span>relationships</span></div>
        <div class="metric"><b id="graph-count-notes">0</b><span>low-stakes notes</span></div>
      </div>
  <div class="primary-grid">
    <section class="surface">
      <div class="panel-head">
        <div>
          <h2>People found in reviewed sources</h2>
          <p>These profiles are built from structured graph deltas. Raw transcript is not shown.</p>
        </div>
      </div>
      <div class="controls">
        <input id="graph-filter" placeholder="Filter people, emotions, memories, or relationships">
        <button id="graph-filter-clear">Clear</button>
      </div>
      <div id="graph-filter-status" class="muted"></div>
      <div id="graph-profile-cards" class="person-cards section-gap"></div>
    </section>

    <section class="surface">
      <div class="panel-head">
        <div>
          <h2>Continuity</h2>
          <p>Where the thread left off, what remains active, and what should not be repeated.</p>
        </div>
      </div>
      <div class="list-grid">
        <section><h3>Open loops</h3><ul id="open-loops"></ul></section>
        <section><h3>Saved decisions</h3><ul id="decisions"></ul></section>
        <section><h3>Emotional anchors</h3><ul id="anchors"></ul></section>
        <section><h3>Recent sessions</h3><ul id="sessions"></ul></section>
      </div>
    </section>
  </div>

      <section>
        <div class="panel-head">
          <div>
            <h2>Import old chats</h2>
            <p>Preview archives first. Pulse imports structured graph memory only after an explicit commit command.</p>
          </div>
          <span class="label" id="copy-status">ready</span>
        </div>
        <div class="connect-grid">
          <article class="source-card" id="connect-chatgpt"><h3>ChatGPT</h3><p>Open an archive preview, then decide what is safe to import.</p><button class="copy-command" data-copy="pulse migrate request chatgpt --open">Copy command</button></article>
          <article class="source-card" id="connect-claude"><h3>Claude</h3><p>Review exported chats locally before Pulse commits graph memory.</p><button class="copy-command" data-copy="pulse migrate request claude --open">Copy command</button></article>
          <article class="source-card" id="connect-codex"><h3>Codex</h3><p>Bring coding sessions into the same reviewed memory flow.</p><button class="copy-command" data-copy="pulse migrate request codex --open">Copy command</button></article>
          <article class="source-card" id="connect-claude-code"><h3>Claude Code</h3><p>Preview local session memory before it becomes continuity.</p><button class="copy-command" data-copy="pulse migrate request claude-code --open">Copy command</button></article>
          <article class="source-card"><h3>Commit preview</h3><p>Import only after the preview looks human and clean.</p><button class="copy-command" data-copy='pulse migrate commit pulse-preview.json --confirm "import pulse graph" --open'>Copy command</button></article>
        </div>
      </section>

      <section>
        <div class="panel-head">
          <div>
            <h2>Relationship map</h2>
            <p>Readable relationships from committed graph memory.</p>
          </div>
        </div>
        <div id="relationship-map" class="relationship-map"></div>
      </section>

      <section>
        <div class="panel-head">
          <div>
            <h2>Thread map</h2>
            <p>A compact map for the selected thread/source after review.</p>
          </div>
        </div>
        <div class="graph-canvas" id="event-graph">
          <svg class="relationship-svg" viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
            <path d="M15 24 C32 6, 58 8, 82 28"></path>
            <path d="M12 70 C34 48, 64 76, 88 48"></path>
            <path d="M26 42 C44 62, 62 30, 78 72"></path>
          </svg>
        </div>
      </section>

      <section>
        <div class="panel-head">
          <div>
            <h2>Source details</h2>
            <p>Reviewed source objects after viewer cleanup. Use this for inspection, not first-read trust.</p>
          </div>
        </div>
        <div class="list-grid">
          <section><h3>People found in reviewed sources</h3><ul id="graph-people"></ul></section>
          <section><h3>Saved memory</h3><ul id="graph-memories"></ul></section>
          <section><h3>Emotional anchors</h3><ul id="graph-emotions"></ul></section>
          <section><h3>Low-stakes notes</h3><ul id="graph-notes"></ul></section>
        </div>
      </section>
    </div>
  </details>

  <section class="surface section-gap">
    <div class="panel-head"><div><h2>Hidden profiles</h2><p>Profiles removed from cards and relationships. Restore brings them back to this viewer.</p></div></div>
    <ul id="hidden-profiles"></ul>
  </section>

  <section class="surface section-gap">
    <div class="panel-head"><div><h2>Search memory</h2><p>Scoped recall from stored Pulse memory.</p></div></div>
    <div class="controls">
      <input id="search-query" placeholder="Search saved memory">
      <button id="search-button">Search</button>
    </div>
    <ul id="search-results"></ul>
  </section>

  <section class="surface danger">
    <div class="panel-head"><div><h2>Trust controls</h2><p>Delete one memory or wipe the local store. Both actions are deliberate.</p></div></div>
    <div class="trust-tools">
      <div class="stack">
        <h3>Delete memory</h3>
		` + deleteControlsHTML + `
      </div>
      <div class="stack">
        <h3>Wipe memory</h3>
        ` + wipeControlsHTML + `
      </div>
    </div>
  </section>
</main>
<script>
const key = "` + key + `";
const thread = "` + thread + `";
let viewerData = null;
let currentFirstMemoryID = "";
function postJSON(path, body) {
  return fetch(path, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Pulse-Key": key },
    body: JSON.stringify(body)
  }).then(async (resp) => {
    if (!resp.ok) throw new Error(await resp.text());
    if (resp.status === 204) return { ok: true };
    return resp.json();
  });
}
function safeArray(items) {
  return Array.isArray(items) ? items : [];
}
function escapeHTML(value) {
  return String(value || "").replace(/[&<>"']/g, ch => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[ch]));
}
function list(id, items, render = x => x, emptyText = "None recorded.") {
  const el = document.getElementById(id);
  el.innerHTML = "";
  const values = items && items.length > 0 ? items : [emptyText];
  for (const item of values) {
    const li = document.createElement("li");
    li.textContent = String(render(item) || emptyText);
    li.className = item === emptyText ? "empty" : "";
    el.appendChild(li);
  }
}
function filterStrings(items, query) {
  const values = safeArray(items);
  if (!query) return values;
  return values.filter(item => String(item || "").toLowerCase().includes(query));
}
function profileMatches(profile, query) {
  if (!query) return true;
  return [
    profile?.name,
    profile?.summary,
    ...safeArray(profile?.aliases),
    ...safeArray(profile?.facts),
    ...safeArray(profile?.relationships),
    ...safeArray(profile?.memories),
  ].join(" ").toLowerCase().includes(query);
}
function setCount(id, value) {
  document.getElementById(id).textContent = String(value);
}
function sessionLabel(session) {
  if (!session || typeof session !== "object") return "No sessions yet.";
  const bits = [session.status, session.thread_id, session.started_at].filter(Boolean);
  return bits.length > 0 ? bits.join(" / ") : "Session captured by Pulse.";
}
function formatActivityTime(value) {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return " · " + new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit"
  }).format(date);
}
function activityLabel(item) {
  if (!item || typeof item !== "object") return "No activity yet.";
  const title = item.title || "Pulse activity";
  const summary = item.summary || "Raw details are hidden.";
  return title + ": " + summary + formatActivityTime(item.created_at);
}
function updateActivityState(data) {
  const items = safeArray(data?.activity);
  const summary = document.getElementById("activity-summary");
  const actionSummary = document.getElementById("activity-mcp-summary");
  if (!summary || !actionSummary) return;
  if (items.length === 0) {
    summary.textContent = "No Pulse activity yet. After your first memory proof, this will show what Pulse did.";
    actionSummary.textContent = "No tool calls yet. After your first memory proof, you will see memory, resume, and recall actions here.";
    return;
  }
  const plural = items.length === 1 ? "action" : "actions";
  summary.textContent = "Pulse recorded " + items.length + " local " + plural + ". Raw prompts, transcripts, and tool payloads stay hidden.";
  const names = items.slice(0, 3).map(item => item.title || "Pulse activity").join(" · ");
  actionSummary.textContent = names + (items.length > 3 ? " · and more" : "");
}
function firstText(items, fallback) {
  return Array.isArray(items) && items.length > 0 ? items[0] : fallback;
}
function resumeSummary(data) {
  const sections = data?.next_resume?.sections || {};
  const where = firstText(sections.where_we_left_off, "Start with one proof memory before importing archives.");
  const decision = firstText(sections.active_decisions, "Import is optional and private by default.");
	return "Pulse will provide: " + where + " " + decision;
}
function renderHarnessDigest(data) {
  const items = asArray(data?.next_resume?.sections?.harness_activity);
  const panel = document.getElementById("harness-digest-panel");
  const ul = document.getElementById("harness-digest");
  if (!panel || !ul) return;
  ul.innerHTML = "";
  if (items.length === 0) { panel.hidden = true; return; }
  panel.hidden = false;
  items.forEach((item, i) => {
    const li = document.createElement("li");
    li.textContent = String(item);
    li.style.animationDelay = (i * 90) + "ms"; // staggered reveal
    ul.appendChild(li);
  });
}
function formatApproxTokens(value) {
  const n = Number(value || 0);
  if (!Number.isFinite(n) || n <= 0) return "0";
  if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1).replace(/\.0$/, "") + "k";
  return String(Math.round(n));
}
function updateTokenHeart(data) {
  const el = document.getElementById("context-heart-copy");
  if (!el) return;
  const economy = data?.next_resume?.token_economy || {};
	const offered = Number(economy.pulse_tokens || data?.next_resume?.token_estimate || 0);
	if (offered > 0) {
		el.textContent = "♥ Local Pulse context: " + formatApproxTokens(offered) + " tokens by " + String(economy.method_id || "local count") + ". Savings need comparable delivery receipts.";
    return;
  }
	el.textContent = "Token economy is collecting a comparable baseline.";
}
function updateFirstMemory(data) {
  const title = document.querySelector("[data-first-memory-title]");
  if (!title) return;
  const summary = document.querySelector("[data-first-memory-summary]");
  const id = document.querySelector("[data-first-memory-id]");
  const status = document.querySelector("[data-memory-action-status]");
  const forget = document.querySelector('[data-first-memory-action="forget"]');
	const destructiveOpsLocked = document.body.dataset.destructiveOpsLocked === "true";
  const firstMemory = data?.first_memory || {};
  if (firstMemory.status === "saved" && firstMemory.id) {
    currentFirstMemoryID = firstMemory.id;
    title.textContent = "Your first memory is saved";
    if (summary) summary.textContent = "“" + (firstMemory.summary || "No saved structured memory is loaded yet.") + "”";
    if (id) {
      id.hidden = false;
      id.textContent = "Memory ID: " + firstMemory.id;
    }
	if (status) status.textContent = destructiveOpsLocked
	  ? "Saved locally. Forgetting requires the privileged OS-backed Pulse surface."
	  : "Saved locally. You can forget this memory from here.";
	if (forget && !destructiveOpsLocked) forget.textContent = "Forget";
    return;
  }
  currentFirstMemoryID = "";
  title.textContent = "Your first memory starts here";
  if (id) {
    id.hidden = true;
    id.textContent = "";
  }
	if (status) status.textContent = "Nothing is pending until a harness submits a visible Memory Tray candidate.";
	if (forget && !destructiveOpsLocked) forget.textContent = "Hide in this preview";
}
function emotionLabel(value) {
  switch (value) {
    case "curious": return "curious";
    case "relieved": return "relieved";
    case "skeptical": return "skeptical";
    case "annoyed": return "annoyed";
    case "something_else": return "something else";
    default: return "this feeling";
  }
}
function emotionTag(value) {
  return "emotion_" + String(value || "feedback").replace(/[^a-z0-9_]+/gi, "_").toLowerCase() + "_confirmed";
}
async function saveEmotionFeedback(value) {
  const status = document.querySelector("[data-emotion-status]");
  if (!status) return;
  if (value === "skip") {
    status.textContent = "Skipped. No emotion was stored.";
    return;
  }
  const label = emotionLabel(value);
  status.textContent = "Saving confirmed feedback locally...";
  try {
    const result = await postJSON("/memory/remember", {
      schema: "pulse.memory_capsule.v1",
      source: {
        host: "pulse-cli",
        conversation_scope: "current_turn",
        timestamp: new Date().toISOString()
      },
      items: [{
        kind: "state_signal",
        redacted_summary: "User marked first Pulse install feeling as " + label + ".",
        confidence: 1,
        evidence_hint: "user_confirmed",
        privacy_tier: "private",
        retention: "long_term",
        tags: ["user_feedback", "onboarding", emotionTag(value)]
      }],
      raw_input_included: false
    });
    const pending = safeArray(result?.receipts).find(receipt => receipt.status === "pending");
    status.textContent = pending
      ? "Visible in Memory Tray and not saved yet. Receipt " + pending.receipt_id + "."
      : "Saved as emotional feedback. You can edit or delete it anytime.";
  } catch (err) {
    status.textContent = "Could not persist yet. This stays local to the preview until Pulse accepts feedback.";
  }
}
function appendCardList(card, title, items) {
  const h = document.createElement("h4");
  h.textContent = title;
  card.appendChild(h);
  const ul = document.createElement("ul");
  const values = items && items.length > 0 ? items : ["None recorded."];
  for (const item of values) {
    const li = document.createElement("li");
    li.textContent = item;
    li.className = item === "None recorded." ? "empty" : "";
    ul.appendChild(li);
  }
  card.appendChild(ul);
}
function renderPersonProfiles(profiles) {
  const el = document.getElementById("graph-profile-cards");
  el.innerHTML = "";
  if (!profiles || profiles.length === 0) {
    const empty = document.createElement("section");
    empty.className = "person-card empty";
    empty.textContent = "No trusted people yet. Import a reviewed preview or let host-extracted memory build up.";
    el.appendChild(empty);
    return;
  }
  for (const profile of profiles) {
    const card = document.createElement("section");
    card.className = "person-card";
    const title = document.createElement("h3");
    title.textContent = profile.name || "Unknown person";
    card.appendChild(title);
    if (profile.aliases && profile.aliases.length > 0) {
      const aliases = document.createElement("p");
      aliases.textContent = "Also seen as " + profile.aliases.slice(0, 6).join(", ");
      card.appendChild(aliases);
    }
    if (profile.summary) {
      const summary = document.createElement("p");
      summary.textContent = profile.summary;
      card.appendChild(summary);
    }
    const meter = document.createElement("div");
    meter.className = "meter";
    const fill = document.createElement("span");
    const weight = Math.max(0, Math.min(1, Number(profile.emotional_weight || 0)));
    fill.style.setProperty("--weight", Math.round(weight * 100) + "%");
    meter.appendChild(fill);
    card.appendChild(meter);
    appendCardList(card, "Facts", profile.facts);
    appendCardList(card, "Relationships", profile.relationships);
    appendCardList(card, "Memories", profile.memories);
    if (profile.id) {
      const actions = document.createElement("div");
      actions.className = "profile-actions";
      const hide = document.createElement("button");
      hide.className = "soft-danger";
      hide.type = "button";
      hide.textContent = "Hide profile";
      hide.addEventListener("click", async () => {
        const name = profile.name || "this profile";
        if (!window.confirm("Hide " + name + " from the Pulse viewer? The local record stays stored, but cards and relationships stop showing it.")) {
          return;
        }
        await postJSON("/graph/entity/hide", { id: profile.id, confirm: "hide graph entity" });
        await loadViewerData();
      });
      actions.appendChild(hide);
      card.appendChild(actions);
    }
    el.appendChild(card);
  }
}
function renderRelationshipMap(relationships) {
  const el = document.getElementById("relationship-map");
  el.innerHTML = "";
  const values = relationships.length > 0 ? relationships : ["No readable relationships imported yet."];
  for (const item of values.slice(0, 12)) {
    const row = document.createElement("div");
    row.className = "relationship-link" + (relationships.length === 0 ? " empty" : "");
    row.textContent = item;
    el.appendChild(row);
  }
}
function renderEventGraph(people, profiles, memories) {
  const el = document.getElementById("event-graph");
  for (const old of Array.from(el.querySelectorAll(".entity-chip"))) old.remove();
  const labels = [...people.slice(0, 7), ...memories.slice(0, 3)].slice(0, 10);
  if (labels.length === 0) labels.push("Preview archive", "Review profiles", "Commit graph");
  const positions = [[8,16],[58,10],[34,34],[72,36],[14,62],[50,64],[76,74],[28,78],[42,14],[62,56]];
  labels.forEach((label, index) => {
    const chip = document.createElement("div");
    chip.className = "entity-chip";
    chip.style.left = positions[index % positions.length][0] + "%";
    chip.style.top = positions[index % positions.length][1] + "%";
    const profile = profiles.find(p => p.name === label);
    chip.innerHTML = "<strong>" + escapeHTML(label) + "</strong><span>" + escapeHTML(profile ? "person" : (index < people.length ? "entity" : "event")) + "</span>";
    el.appendChild(chip);
  });
}
function renderHiddenEntities(hidden) {
  const el = document.getElementById("hidden-profiles");
  el.innerHTML = "";
  const values = safeArray(hidden);
  if (values.length === 0) {
    const empty = document.createElement("li");
    empty.className = "empty";
    empty.textContent = "No hidden profiles.";
    el.appendChild(empty);
    return;
  }
  for (const item of values) {
    const row = document.createElement("li");
    row.className = "hidden-row";
    const label = document.createElement("span");
    label.textContent = (item.name || "Hidden profile") + (item.kind ? " / " + item.kind : "");
    const restore = document.createElement("button");
    restore.className = "restore-button";
    restore.type = "button";
    restore.textContent = "Restore";
    restore.addEventListener("click", async () => {
      await postJSON("/graph/entity/restore", { id: item.id, confirm: "restore graph entity" });
      await loadViewerData();
    });
    row.appendChild(label);
    row.appendChild(restore);
    el.appendChild(row);
  }
}
function renderGraphProfile(data) {
  if (!data) return;
  const query = document.getElementById("graph-filter").value.trim().toLowerCase();
  const graph = data.graph_profile || {};
  const people = filterStrings(graph.people, query);
  const memories = filterStrings(graph.memories, query);
  const emotions = filterStrings(graph.emotions, query);
  const relationships = filterStrings(graph.relationships, query);
  const funFacts = filterStrings(graph.fun_facts, query);
  const profiles = safeArray(graph.person_profiles).filter(profile => profileMatches(profile, query));
  setCount("graph-count-profiles", profiles.length);
  setCount("graph-count-people", people.length);
  setCount("graph-count-memories", memories.length);
  setCount("graph-count-emotions", emotions.length);
  setCount("graph-count-relationships", relationships.length);
  setCount("graph-count-notes", funFacts.length);
  list("graph-people", people, x => x, query ? "No people match." : "None recorded.");
  list("graph-memories", memories, x => x, query ? "No memories match." : "None recorded.");
  list("graph-emotions", emotions, x => x, query ? "No emotions match." : "None recorded.");
  list("graph-notes", funFacts, x => x, query ? "No low-stakes notes match." : "None recorded.");
  renderRelationshipMap(relationships);
  renderEventGraph(people, profiles, memories);
  renderPersonProfiles(profiles);
  const status = document.getElementById("graph-filter-status");
  status.textContent = query ? 'Filtered graph memory by "' + query + '".' : "Showing reviewed graph memory.";
}
function renderMemoryTray(candidates) {
  const root = document.getElementById("memory-tray");
  const count = document.getElementById("memory-tray-count");
  const items = safeArray(candidates);
  count.textContent = String(items.length) + (items.length === 1 ? " candidate" : " candidates");
  root.replaceChildren();
  if (items.length === 0) {
    const empty = document.createElement("p");
    empty.textContent = "No private writes are waiting.";
    root.appendChild(empty);
    return;
  }
  for (const item of items) {
    const card = document.createElement("article");
    card.className = "tray-card";
    const meta = document.createElement("div");
    meta.className = "tray-meta";
	    for (const text of [item.latest_receipt?.status || item.state, item.operation, item.current ? "current" : "historical", item.destination_class, item.projection_status, "v" + item.version, item.latest_receipt?.receipt_id || "no receipt"]) {
      const tag = document.createElement("span");
      tag.className = "label";
      tag.textContent = text;
      meta.appendChild(tag);
    }
    const exact = document.createElement("pre");
    exact.textContent = JSON.stringify(item.candidate, null, 2);
    card.append(meta, exact);
    const history = safeArray(item.receipt_history);
    const historyDetails = document.createElement("details");
    const historySummary = document.createElement("summary");
    historySummary.textContent = "Receipt history (" + history.length + ")";
    const historyList = document.createElement("ul");
    for (const receipt of history) {
      const row = document.createElement("li");
      row.textContent = [receipt.status, receipt.receipt_id, receipt.object_id || "no object", receipt.reason_code || ""].filter(Boolean).join(" / ");
      historyList.appendChild(row);
    }
    historyDetails.append(historySummary, historyList);
    card.appendChild(historyDetails);
    if (item.state === "pending") {
      const actions = document.createElement("div");
      actions.className = "tray-actions";
	      const edit = document.createElement("button");
	      edit.type = "button";
	      edit.textContent = "Edit exact candidate";
	      edit.dataset.trayEdit = item.candidate_id;
	      edit.dataset.version = String(item.version);
      const cancel = document.createElement("button");
      cancel.type = "button";
      cancel.textContent = "Cancel";
      cancel.dataset.trayCancel = item.candidate_id;
      cancel.dataset.version = String(item.version);
	      actions.appendChild(edit);
	      actions.appendChild(cancel);
      card.appendChild(actions);
    }
	    if (item.state === "committed" && item.current && item.latest_receipt?.object_id && item.latest_receipt?.reason_code !== "user_deleted" && (item.candidate?.capsule || item.candidate?.semantic_delta)) {
	      const actions = document.createElement("div");
	      actions.className = "tray-actions";
	      const correct = document.createElement("button");
	      correct.type = "button";
	      correct.textContent = "Correct committed memory";
	      correct.dataset.memoryCorrect = item.latest_receipt.object_id;
	      correct.dataset.candidateId = item.candidate_id;
	      actions.appendChild(correct);
	      card.appendChild(actions);
	    }
    root.appendChild(card);
  }
}
let viewerLoadInFlight = false;
async function loadViewerData() {
  if (viewerLoadInFlight) return;
  viewerLoadInFlight = true;
  try {
    const resp = await fetch("/viewer/data?key=" + encodeURIComponent(key) + "&thread_id=" + encodeURIComponent(thread));
    if (!resp.ok) throw new Error(await resp.text());
    const data = await resp.json();
    viewerData = data;
    const estimate = data.next_resume?.token_estimate || 0;
	document.getElementById("resume").textContent = data.next_resume?.resume_markdown || "No resume block yet.\nStart a connected harness session or import one small source.\nPulse will show the next resume block here before it is injected.";
    document.getElementById("resume-summary").textContent = resumeSummary(data);
    renderHarnessDigest(data);
    document.getElementById("resume-budget").textContent = String(estimate) + " tokens";
		const economy = data.next_resume?.token_economy || {};
		document.getElementById("resume-token-economy").textContent = String(economy.pulse_tokens || estimate) + " local tokens";
    updateTokenHeart(data);
    updateFirstMemory(data);
    list("open-loops", data.open_loops);
    list("decisions", data.saved_decisions);
	    list("anchors", data.emotional_anchors);
	    list("sessions", data.recent_sessions, sessionLabel, "No sessions yet.");
	    list("activity-log", data.activity, activityLabel, "No activity yet. Save one memory or start a new session to see what Pulse did.");
	    updateActivityState(data);
	    renderGraphProfile(data);
    renderMemoryTray(data.memory_tray);
    renderHiddenEntities(data.hidden_entities);
  } catch (err) {
    document.getElementById("resume").textContent = "Viewer error: " + err.message;
  } finally {
    viewerLoadInFlight = false;
  }
}
loadViewerData();
const viewerRefreshTimer = window.setInterval(() => {
  if (!document.hidden) loadViewerData();
}, 1000);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) loadViewerData();
});
window.addEventListener("beforeunload", () => window.clearInterval(viewerRefreshTimer));
document.getElementById("memory-tray").addEventListener("click", async event => {
  const button = event.target.closest("button");
  if (!button) return;
	if (button.dataset.memoryCorrect) {
	  const item = safeArray(viewerData?.memory_tray).find(value => value.candidate_id === button.dataset.candidateId);
	  if (!item) return;
	  const currentJSON = JSON.stringify(item.candidate, null, 2);
	  const replacementJSON = window.prompt("Correct the exact structured memory. It returns to the Tray for 10 seconds before replacing the same object ID.", currentJSON);
	  if (replacementJSON === null || replacementJSON === currentJSON) return;
	  let candidate;
	  try { candidate = JSON.parse(replacementJSON); } catch { window.alert("Correction must be valid JSON."); return; }
	  await postJSON("/memory/" + encodeURIComponent(button.dataset.memoryCorrect) + "/correct", { candidate });
	  await loadViewerData();
	  return;
	}
  const candidateID = button.dataset.trayCancel || button.dataset.trayEdit;
  const version = Number(button.dataset.version || 0);
  if (!candidateID || version < 1) return;
  if (button.dataset.trayCancel) {
    await postJSON("/memory/tray/" + encodeURIComponent(candidateID) + "/cancel", { expected_version: version });
    await loadViewerData();
    return;
  }
  const item = safeArray(viewerData?.memory_tray).find(value => value.candidate_id === candidateID);
  if (!item) return;
	let candidate;
	if (item.candidate?.capsule) {
	  const current = item.candidate.capsule.items?.[0]?.redacted_summary;
	  if (typeof current !== "string") return;
	  const replacement = window.prompt("Edit the exact redacted summary. The 10 second grace period restarts.", current);
	  if (replacement === null || replacement === current) return;
	  candidate = JSON.parse(JSON.stringify(item.candidate));
	  candidate.capsule.items[0].redacted_summary = replacement;
	} else {
	  const currentJSON = JSON.stringify(item.candidate, null, 2);
	  const replacementJSON = window.prompt("Edit the exact structured semantic candidate. The 10 second grace period restarts.", currentJSON);
	  if (replacementJSON === null || replacementJSON === currentJSON) return;
	  try { candidate = JSON.parse(replacementJSON); } catch { window.alert("Candidate must be valid JSON."); return; }
	}
  await postJSON("/memory/tray/" + encodeURIComponent(candidateID) + "/edit", { expected_version: version, candidate });
  await loadViewerData();
});
document.getElementById("graph-filter").addEventListener("input", () => renderGraphProfile(viewerData));
document.getElementById("graph-filter-clear").addEventListener("click", () => {
  document.getElementById("graph-filter").value = "";
  renderGraphProfile(viewerData);
});
document.getElementById("search-button").addEventListener("click", async () => {
  const query = document.getElementById("search-query").value.trim();
  if (!query) return;
  const out = await postJSON("/memory/recall", { query, limit: 10, privacy_ceiling: "private" });
  list("search-results", out.items || [], item => item.id + " / " + item.kind + " / " + item.summary);
});
` + deleteScriptHTML + `
` + wipeScriptHTML + `
for (const button of Array.from(document.querySelectorAll(".copy-command"))) {
  button.addEventListener("click", async () => {
    const command = button.getAttribute("data-copy") || "";
    if (!command) return;
    try {
      await navigator.clipboard.writeText(command);
      document.getElementById("copy-status").textContent = "copied";
    } catch {
      document.getElementById("copy-status").textContent = command;
    }
  });
}
for (const button of Array.from(document.querySelectorAll(".jump-to-import"))) {
  button.addEventListener("click", () => {
    const target = document.getElementById("import-wizard");
    if (!target) return;
    target.open = true;
    target.scrollIntoView({ behavior: "smooth", block: "start" });
  });
}
for (const button of Array.from(document.querySelectorAll("[data-review-action]"))) {
  button.addEventListener("click", () => {
    const card = button.closest(".decision-card");
    const status = card?.querySelector("[data-review-status]");
    if (!status) return;
    status.textContent = button.getAttribute("data-review-result") || "Updated in this preview.";
    card.setAttribute("data-review-state", button.getAttribute("data-review-action") || "updated");
  });
}
for (const button of Array.from(document.querySelectorAll("[data-emotion]"))) {
  button.addEventListener("click", () => {
    saveEmotionFeedback(button.getAttribute("data-emotion"));
  });
}
for (const button of Array.from(document.querySelectorAll("[data-first-memory-action]"))) {
  button.addEventListener("click", async () => {
    const status = document.querySelector("[data-memory-action-status]");
    if (!status) return;
    const action = button.getAttribute("data-first-memory-action");
    if (action === "forget") {
      if (!currentFirstMemoryID) {
        status.textContent = "Hidden in this local preview. Nothing was deleted because no saved memory ID is loaded yet.";
        return;
      }
      try {
        await postJSON("/memory/delete", { id: currentFirstMemoryID });
        status.textContent = "Forgotten locally. This memory will not appear in future resume previews.";
        currentFirstMemoryID = "";
        const title = document.querySelector("[data-first-memory-title]");
        const id = document.querySelector("[data-first-memory-id]");
        if (title) title.textContent = "First memory forgotten";
        if (id) {
          id.hidden = true;
          id.textContent = "";
        }
      } catch (err) {
        status.textContent = "Could not delete yet: " + err.message;
      }
    } else if (action === "important") {
      status.textContent = "Preview only: marked important on this screen, not saved to Pulse yet.";
    } else if (action === "emotion") {
      status.textContent = "Choose a feeling above. No emotion is stored until you confirm.";
    } else {
      status.textContent = "Preview only: editing is not persisted in this build yet.";
    }
  });
}
for (const button of Array.from(document.querySelectorAll("[data-state-action]"))) {
  button.addEventListener("click", () => {
    const card = button.closest(".state-signal-card");
    if (!card) return;
    let status = card.querySelector("[data-state-status]");
    if (!status) {
      status = document.createElement("p");
      status.className = "emotion-status";
      status.setAttribute("data-state-status", "");
      card.appendChild(status);
    }
    const action = button.getAttribute("data-state-action") || "updated";
    status.textContent = action + " in this private preview. No diagnosis is stored as fact.";
  });
}
</script>
</body>
</html>`))
}

func viewerHostDisplayName(host string) string {
	switch host {
	case "codex":
		return "Codex"
	case "claude-code":
		return "Claude Code"
	case "gemini-cli":
		return "Gemini CLI"
	case "cursor":
		return "Cursor"
	case "claude", "claude-chat":
		return "Claude"
	default:
		return "Pulse product"
	}
}

func viewerFirstRunHTML(hostName string, productDestructiveOpsLocked bool) string {
	hostName = html.EscapeString(hostName)
	forgetButton := `<button type="button" data-first-memory-action="forget">Hide in this preview</button>`
	forgetPromise := "Forget deletes only when a saved memory ID is loaded."
	if productDestructiveOpsLocked {
		forgetButton = `<button type="button" data-first-memory-action="forget" disabled>OS confirmation required to forget</button>`
		forgetPromise = "Forgetting product memory stays locked until the privileged OS-backed Pulse surface is active."
	}
	return `
  <section class="surface first-run" id="first-run">
    <div class="panel-head">
      <div>
        <h2>Welcome to Pulse.</h2>
        <p>Pulse helps remember what actually mattered. It works quietly even if you never touch this screen.</p>
      </div>
      <span class="label">Stored locally. Yours.</span>
    </div>
    <div class="first-run-grid">
      <article class="first-memory-card">
        <span class="label">Memory</span>
        <h3 data-first-memory-title>Your first memory starts here</h3>
        <p data-first-memory-summary>“No saved structured memory is loaded for ` + hostName + ` yet.”</p>
        <p class="memory-action-status" data-first-memory-id hidden></p>
        <h3 class="section-gap">Why it matters</h3>
	        <p>A saved candidate will prove what Pulse can carry into the next session.</p>
	        <p class="memory-action-status" data-memory-action-status>Nothing is pending until a harness submits a visible Memory Tray candidate.</p>
        <div class="memory-actions">
          <button type="button" data-first-memory-action="edit">Edit preview only</button>
          <button type="button" data-first-memory-action="important">Mark important preview only</button>
          <button type="button" data-first-memory-action="emotion">Change emotion feedback</button>
	          ` + forgetButton + `
	        </div>
	        <p class="emotion-status">Edit preview only and Mark important preview only do not persist yet. Change emotion saves only after you choose a feeling. ` + forgetPromise + `</p>
      </article>
      <article class="feeling-card">
        <h3>How did that feel?</h3>
        <p>Pulse guess: maybe curiosity? Change this before anything emotional is saved.</p>
        <p class="emotion-status">No emotion is stored until you choose one. Silence is not consent.</p>
        <button type="button" data-first-memory-action="emotion">Change this</button>
        <div class="feeling-actions" aria-label="First Pulse feeling feedback">
          <button type="button" data-emotion="curious">Curious</button>
          <button type="button" data-emotion="relieved">Relieved</button>
          <button type="button" data-emotion="skeptical">Skeptical</button>
          <button type="button" data-emotion="annoyed">Annoyed</button>
          <button type="button" data-emotion="something_else">Something else</button>
          <button type="button" data-emotion="skip">Skip</button>
        </div>
        <p class="emotion-status" data-emotion-status>No emotion is stored until you choose one.</p>
      </article>
    </div>
    <div class="context-heart">
	      <h3>♥ Context continuity</h3>
      <p id="context-heart-copy">Token savings appear after your first resume.</p>
    </div>
    <section class="section-gap">
      <h3>Feedback makes Pulse more personal</h3>
      <p>Pulse works quietly even if you never touch this screen. But it becomes more personal when you mark “this mattered”, “wrong tone”, “too strong”, “remember this”, or “forget this”.</p>
    </section>
    <details class="surface source-scan-details">
      <summary>Available sources <span class="path-summary">Source scanner · Preview sources after proof</span></summary>
      <div class="details-body">
      <h3>Source scanner</h3>
      <p>Preview sources after proof. Import stays optional and private by default.</p>
      <p>Preview first. Import later.</p>
      <div class="source-scan-grid section-gap">
		<article class="source-scan-card"><h3>Claude Code sessions</h3><p>Connector: available. Run doctor for actual MCP and hook readiness. Sessions: not scanned yet.</p><button type="button" class="jump-to-import">Preview sessions</button></article>
        <article class="source-scan-card"><h3>Codex sessions</h3><p>Connector: available. Source: not scanned yet. Sessions: not scanned yet.</p><button type="button" class="jump-to-import">Preview</button></article>
        <article class="source-scan-card"><h3>Gemini CLI</h3><p>Connector: available. Source: not connected yet. Sessions: not scanned yet.</p><button type="button">Connect later</button></article>
        <article class="source-scan-card"><h3>ChatGPT export</h3><p>Connector: archive import. Source: choose an archive first. Sessions: not scanned yet.</p><button type="button" class="jump-to-import">Choose file</button></article>
        <article class="source-scan-card"><h3>Claude app export</h3><p>Connector: archive import. Source: choose an archive first. Sessions: not scanned yet.</p><button type="button" class="jump-to-import">Choose file</button></article>
        <article class="source-scan-card"><h3>Manual capsule</h3><p>Connector: available. Source: user-written capsule. Sessions: not applicable.</p><button type="button" class="copy-command" data-copy="Remember this in Pulse: Atlas must not own the People Graph; Pulse owns portable continuity memory.">Paste capsule</button></article>
      </div>
      </div>
    </details>
    <details class="surface state-signal-details">
      <summary>Possible state signals <span class="path-summary">optional, private, editable</span></summary>
      <div class="details-body">
      <h3>Possible state signals</h3>
      <p>private by default, editable, no diagnosis, and never exported by default.</p>
      <div class="state-signal-grid section-gap">
        <article class="state-signal-card"><h3>curiosity</h3><p>source: first install response · confidence: low · status: needs confirmation</p><div class="state-actions"><button type="button" data-state-action="confirm">confirm</button><button type="button" data-state-action="change">change</button><button type="button" data-state-action="ignore">ignore</button></div></article>
      </div>
      </div>
    </details>
  </section>`
}

func (s *Server) localAutoEnabled() bool {
	return s.cfg.Billing.Mode == "local-auto"
}
