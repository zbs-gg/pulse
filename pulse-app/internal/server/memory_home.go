package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/unassigned"
	"github.com/nkkmnk/pulse/internal/userpresence"
)

func (s *Server) buildMemoryHome(
	now time.Time,
	snapshot personalLiveReadinessSnapshot,
) (store.MemoryHomeData, error) {
	if s == nil || s.cfg.Store == nil {
		return store.MemoryHomeData{}, fmt.Errorf("Memory Home store is unavailable")
	}
	if err := validatePersonalLiveReadiness(snapshot); err != nil {
		return store.MemoryHomeData{}, fmt.Errorf("Memory Home readiness is invalid")
	}
	bindingDigest, repositoryID, ok := s.cfg.Store.ProductRuntimeBoundary()
	if !ok {
		return store.MemoryHomeData{}, fmt.Errorf("Memory Home product boundary is unavailable")
	}
	resume, err := s.cfg.Store.BuildResume(store.ResumeQuery{ThreadID: repositoryID, TokenBudget: 1200})
	if err != nil {
		return store.MemoryHomeData{}, err
	}
	objectIDs := append([]string(nil), resume.IncludedObjectIDs...)
	evidenceIDs := append([]string(nil), resume.IncludedEvidenceIDs...)
	sort.Strings(objectIDs)
	sort.Strings(evidenceIDs)
	digest := sha256.Sum256([]byte(resume.ResumeMarkdown))
	redactedResume := memoryHomeRedactedResume(resume)
	renderedBytes := len(redactedResume)
	preview := &store.MemoryHomeNextTaskPreview{
		Status: "preview_only", PayloadDigest: fmt.Sprintf("%x", digest[:]),
		ObjectIDs: objectIDs, EvidenceIDs: evidenceIDs, RedactedResume: redactedResume,
		MethodID: store.MemoryHomeCountMethodUTF8BytesDiv4Ceil, MethodVersion: "1",
		RenderedBytes: renderedBytes, PulseTokens: (renderedBytes + 3) / 4,
	}
	liveReadiness, checkedAt := s.memoryHomeLiveReadiness(snapshot, now)
	data, err := s.cfg.Store.BuildMemoryHomeData(store.MemoryHomeQuery{
		RepositoryID: repositoryID, BindingDigest: bindingDigest, GeneratedAt: now.UTC(),
		LiveReadiness:   liveReadiness,
		NextTaskPreview: preview,
	}, s.cfg.Store)
	if err != nil {
		return store.MemoryHomeData{}, err
	}
	if data.Readiness.ReasonCode == liveReadiness.ReasonCode {
		data.Readiness.CheckedAt = checkedAt
	}
	return data, nil
}

func (s *Server) memoryHomeLiveReadiness(
	snapshot personalLiveReadinessSnapshot,
	now time.Time,
) (store.MemoryHomeLiveReadiness, string) {
	if snapshot.Outcome != store.MemoryHomeReadinessReady {
		return store.MemoryHomeLiveReadiness{
			Outcome: snapshot.Outcome, ReasonCode: snapshot.ReasonCode, NextAction: snapshot.NextAction,
		}, snapshot.CheckedAt
	}
	checkedAt := now.UTC().Format(time.RFC3339Nano)
	if s.cfg.Retrieval == nil {
		downgraded := personalLiveReadinessForReason("full_retrieval_unavailable", checkedAt)
		return store.MemoryHomeLiveReadiness{
			Outcome: downgraded.Outcome, ReasonCode: downgraded.ReasonCode, NextAction: downgraded.NextAction,
		}, checkedAt
	}
	if !s.cfg.Retrieval.EmbedderReady() {
		downgraded := personalLiveReadinessForReason("local_embedder_warming", checkedAt)
		return store.MemoryHomeLiveReadiness{
			Outcome: downgraded.Outcome, ReasonCode: downgraded.ReasonCode, NextAction: downgraded.NextAction,
		}, checkedAt
	}
	return store.MemoryHomeLiveReadiness{
		Outcome: snapshot.Outcome, ReasonCode: snapshot.ReasonCode, NextAction: snapshot.NextAction,
	}, snapshot.CheckedAt
}

func memoryHomeRedactedResume(resume store.ResumeBlock) string {
	lines := []string{"Pulse next-task preview"}
	appendSection := func(label string, values []string, limit int) {
		if len(values) == 0 || limit < 1 {
			return
		}
		section := make([]string, 0, limit)
		for index, value := range values {
			if index >= limit {
				break
			}
			if value = strings.TrimSpace(value); value != "" {
				section = append(section, value)
			}
		}
		if len(section) > 0 {
			lines = append(lines, label+": "+strings.Join(section, "; "))
		}
	}
	appendSection("Where we left off", resume.Sections.WhereWeLeftOff, 2)
	appendSection("Active decisions", resume.Sections.ActiveDecisions, 3)
	appendSection("Open loops", resume.Sections.OpenLoops, 3)
	appendSection("Do not repeat", resume.Sections.DoNotRepeat, 2)
	appendSection("Suggested next step", resume.Sections.SuggestedNextStep, 1)
	return strings.Join(lines, " · ")
}

type memoryHomePendingCard struct {
	CandidateID   string
	Version       int
	Kind          string
	Summary       string
	CandidateJSON string
}

type memoryHomeUnassignedCard struct {
	ItemID        string
	ContentDigest string
	CreatedAt     string
	Host          string
	Kind          string
	Summary       string
}

type memoryHomeUnassignedActivity struct {
	ReceiptID     string
	ActionLabel   string
	Status        string
	CreatedAt     string
	ContentDigest string
}

type memoryHomePage struct {
	Data                    store.MemoryHomeData
	Pending                 []memoryHomePendingCard
	EnhancedPresenceProfile userpresence.EnhancedPresenceProfile
	UnassignedEnabled       bool
	UnassignedUnavailable   bool
	Unassigned              []memoryHomeUnassignedCard
	UnassignedActivity      []memoryHomeUnassignedActivity
	CSRFToken               string
}

type memoryHomeTemplateData struct {
	memoryHomePage
	StoreLabel            string
	StatusTitle           string
	StatusDetail          string
	StatusTone            string
	MemoryCount           string
	ContextState          string
	ContextHost           string
	EconomyValue          string
	EconomyDetail         string
	HasLatestMemory       bool
	HasNextPreview        bool
	HasPending            bool
	HasUnassigned         bool
	HasUnassignedActivity bool
	HasAttempts           bool
	HasContext            bool
	HasProtectedActions   bool
	CanProtectedWipe      bool
	EstimatedPercent      string
	Attempts              []store.MemoryHomeAttempt
}

func renderMemoryHomeHTML(page memoryHomePage) (string, error) {
	if page.EnhancedPresenceProfile.Schema == "" {
		page.EnhancedPresenceProfile = userpresence.NewUnavailableAuthorizer("enhanced_presence_unavailable").Profile()
	}
	attempts := memoryHomeBoundedAttempts(page.Data.Receipts.Attempts)
	view := memoryHomeTemplateData{
		memoryHomePage:        page,
		StoreLabel:            memoryHomeStoreLabel(page.Data.Boundary.StoreKind),
		MemoryCount:           fmt.Sprintf("%d", page.Data.Memories.ActiveCount),
		HasLatestMemory:       len(page.Data.Memories.LatestActive) > 0,
		HasNextPreview:        page.Data.NextTaskPreview != nil,
		HasPending:            len(page.Pending) > 0,
		HasUnassigned:         len(page.Unassigned) > 0,
		HasUnassignedActivity: len(page.UnassignedActivity) > 0,
		HasAttempts:           len(attempts) > 0,
		HasContext:            page.Data.Context.LatestDelivery != nil,
		HasProtectedActions:   profileHasValidProtectedActions(page.EnhancedPresenceProfile),
		CanProtectedWipe:      profileAuthorizes(page.EnhancedPresenceProfile, userpresence.ActionVaultWipe),
		Attempts:              attempts,
	}
	view.StatusTitle, view.StatusDetail, view.StatusTone = memoryHomeReadinessCopy(page.Data.Readiness)
	view.ContextState = memoryHomeContextCopy(page.Data.Context)
	if page.Data.Context.LatestDelivery != nil {
		view.ContextHost = page.Data.Context.LatestDelivery.Host
	}
	view.EconomyValue, view.EconomyDetail, view.EstimatedPercent = memoryHomeEconomyCopy(page.Data.Economy)
	var output bytes.Buffer
	if err := memoryHomeTemplate.Execute(&output, view); err != nil {
		return "", err
	}
	return output.String(), nil
}

func profileHasValidProtectedActions(profile userpresence.EnhancedPresenceProfile) bool {
	return profileAuthorizes(profile, userpresence.ActionBindingChange) ||
		profileAuthorizes(profile, userpresence.ActionVaultWipe)
}

func memoryHomeBoundedAttempts(attempts []store.MemoryHomeAttempt) []store.MemoryHomeAttempt {
	const limit = 10
	if len(attempts) > limit {
		attempts = attempts[:limit]
	}
	result := append([]store.MemoryHomeAttempt(nil), attempts...)
	for index := range result {
		const summaryLimit = 280
		runes := []rune(result[index].RedactedSummary)
		if len(runes) > summaryLimit {
			result[index].RedactedSummary = string(runes[:summaryLimit-1]) + "…"
		}
	}
	return result
}

func memoryHomeStoreLabel(kind string) string {
	switch store.StoreKind(kind) {
	case store.StoreKindPersonal:
		return "Personal memory"
	case store.StoreKindDesk:
		return "Desk memory"
	default:
		return "Local memory"
	}
}

func memoryHomeReadinessCopy(readiness store.MemoryHomeReadinessSnapshot) (title, detail, tone string) {
	switch readiness.ReasonCode {
	case "memory_continuity_ready":
		return "Pulse is remembering across tasks", "Canonical memory was offered to a fresh task and the host confirmed receipt.", "ready"
	case "host_observation_required":
		return "Pulse is remembering, but the harness has not confirmed receipt yet", "The context offer is recorded. Pulse will not call this fully ready without trustworthy host evidence.", "partial"
	case "full_retrieval_unavailable":
		return "Full retrieval is not enabled", "The local private store is available with fallback keyword recall. Pulse retrieval is not being claimed.", "partial"
	case "local_embedder_warming":
		return "Pulse is warming up", "The local memory engine is starting and no completion is being claimed yet.", "warming"
	case "codex_plugin_unavailable":
		return "Pulse Codex plugin needs repair", "Doctor could not verify the exact Pulse plugin generation. Home preserves that result.", "action"
	case "codex_hook_lifecycle_required":
		return "Pulse needs one verified Codex lifecycle", "Doctor has not observed one complete same-task lifecycle yet.", "action"
	case "codex_hook_trust_required":
		return "Pulse hooks need trust", "Codex has not trusted the exact Pulse hook bundle yet.", "action"
	case "codex_native_lifecycle_attestation_unavailable":
		return "Native lifecycle evidence is unavailable", "Use the explicit Pulse MCP tools until Codex exposes replayable lifecycle evidence.", "action"
	case "presence_required":
		return "Pulse presence trust needs repair", "Doctor could not verify the local OS presence helper.", "action"
	case "binding_repair_required":
		return "Pulse workspace binding needs repair", "Doctor could not verify this workspace's exact product binding.", "action"
	case "codex_activation_incomplete":
		return "Pulse Codex activation is incomplete", "Doctor could not verify the exact runtime and daemon activation pair.", "action"
	case "daemon_unavailable":
		return "Pulse daemon is unavailable", "Doctor could not verify the bound local vault through the live daemon.", "action"
	case "first_memory_required":
		return "Pulse is connected", "The local private store is available. Save the first structured memory to begin continuity proof.", "action"
	case "fresh_task_required":
		return "A fresh task is needed", "Canonical memory is available. Open a fresh task so Pulse can record an exact context offer.", "action"
	}
	switch readiness.Outcome {
	case store.MemoryHomeReadinessReady:
		return "Pulse retrieval is ready", "The live memory engine is ready. Cross-task continuity is shown only when its receipt proof is available.", "ready"
	case store.MemoryHomeReadinessPartial:
		return "Pulse needs one more proof", "The live check is partial. Follow the next action; no context delivery or host acknowledgement is being implied.", "partial"
	case store.MemoryHomeReadinessBlocked:
		return "Pulse needs attention", "The live product check is blocked. The action below is the shortest honest recovery path.", "blocked"
	case store.MemoryHomeReadinessWarming:
		return "Pulse is warming up", "The local memory engine is starting and no completion is being claimed yet.", "warming"
	default:
		return "Pulse is connected", "The local private store is available. Complete the next action to prove cross-task continuity.", "action"
	}
}

func memoryHomeContextCopy(context store.MemoryHomeContext) string {
	if context.LatestDelivery == nil {
		return "No context offer yet"
	}
	if context.LatestDelivery.Acknowledgement == store.MemoryHomeDeliveryHostObserved {
		return "Context observed"
	}
	return "Context offered"
}

func memoryHomeEconomyCopy(economy store.MemoryHomeEconomy) (value, detail, percent string) {
	switch economy.State {
	case store.MemoryHomeEconomyMeasured:
		if economy.MeasuredAvoidedTokens != nil {
			return fmt.Sprintf("%d", *economy.MeasuredAvoidedTokens), "Measured avoided input tokens", ""
		}
	case store.MemoryHomeEconomyEstimated:
		if economy.EstimatedAvoidedTokens != nil {
			return fmt.Sprintf("%d", *economy.EstimatedAvoidedTokens), "Estimated avoided tokens from comparable receipts", memoryHomePercent(economy.EstimatedReductionPercent)
		}
	case store.MemoryHomeEconomyUnavailable:
		return "Unavailable", "Receipts use incomparable methods; Pulse will not mix them.", ""
	case store.MemoryHomeEconomyCollectingBaseline:
		if offer := economy.LatestOffer; offer != nil && offer.PulseTokens > 0 && offer.RenderedBytes > 0 &&
			offer.MethodID != "" && offer.MethodVersion != "" {
			return "No estimate yet", fmt.Sprintf(
				"Latest offer: %d Pulse tokens · %d rendered bytes · %s v%s · Collecting a comparable baseline",
				offer.PulseTokens, offer.RenderedBytes, offer.MethodID, offer.MethodVersion,
			), ""
		}
	}
	return "No estimate yet", "Collecting a comparable baseline", ""
}

func memoryHomePercent(value *float64) string {
	if value == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", *value), "0"), ".") + "%"
}

func memoryHomePendingCards(candidates []store.MemoryTrayPendingCandidate) ([]memoryHomePendingCard, error) {
	cards := make([]memoryHomePendingCard, 0, len(candidates))
	for _, candidate := range candidates {
		kind, summary := memoryHomeCandidateDisplay(candidate.Candidate)
		encoded, err := json.MarshalIndent(candidate.Candidate, "", "  ")
		if err != nil {
			return nil, err
		}
		cards = append(cards, memoryHomePendingCard{
			CandidateID: candidate.CandidateID, Version: candidate.Version,
			Kind: kind, Summary: summary, CandidateJSON: string(encoded),
		})
	}
	return cards, nil
}

func memoryHomeUnassignedCards(cards []unassigned.Card) []memoryHomeUnassignedCard {
	result := make([]memoryHomeUnassignedCard, 0, len(cards))
	for _, card := range cards {
		result = append(result, memoryHomeUnassignedCard{
			ItemID: card.ItemID, ContentDigest: card.ContentDigest, CreatedAt: card.CreatedAt,
			Host: card.Host, Kind: card.Kind, Summary: card.Summary,
		})
	}
	return result
}

func memoryHomeUnassignedActivities(
	activity []unassigned.Activity,
	currentBindingDigest string,
) []memoryHomeUnassignedActivity {
	result := make([]memoryHomeUnassignedActivity, 0, len(activity))
	for _, value := range activity {
		label := "Deleted from Inbox"
		if value.Action == "assign" {
			label = "Moved to this project’s Tray"
			if value.BindingDigest != "" && value.BindingDigest != currentBindingDigest {
				label = "Moved to another project’s Tray"
			}
		}
		result = append(result, memoryHomeUnassignedActivity{
			ReceiptID: value.ReceiptID, ActionLabel: label, Status: value.Status,
			CreatedAt: value.CreatedAt, ContentDigest: value.ContentDigest,
		})
	}
	return result
}

func memoryHomeCandidateDisplay(candidate store.PrivateMemoryCandidate) (kind, summary string) {
	if candidate.Capsule != nil && len(candidate.Capsule.Items) == 1 {
		return candidate.Capsule.Items[0].Kind, candidate.Capsule.Items[0].RedactedSummary
	}
	if delta := candidate.SemanticDelta; delta != nil {
		if delta.Continuity != nil && delta.Continuity.Summary != "" {
			return "continuity", delta.Continuity.Summary
		}
		if len(delta.Events) > 0 {
			if delta.Events[0].Summary != "" {
				return "event", delta.Events[0].Summary
			}
			return "event", delta.Events[0].Title
		}
		if len(delta.Facts) > 0 {
			return "fact", delta.Facts[0].Text
		}
		if len(delta.Nodes) > 0 {
			return delta.Nodes[0].Kind, delta.Nodes[0].Summary
		}
	}
	return "memory", "Structured memory awaiting review"
}

var memoryHomeTemplate = template.Must(template.New("memory-home").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="data:,">
  <title>Pulse Memory Home</title>
  <style>
    :root { color-scheme:light; --bg:#f5f3ee; --paper:#fffdfa; --ink:#1d211f; --muted:#6b716c; --line:#deddd7; --green:#2f694f; --green-soft:#e4efe7; --amber:#8a6428; --amber-soft:#f6ecd8; --red:#8b4646; --red-soft:#f4e2e0; --blue:#355f76; --blue-soft:#e4eef3; font:15px/1.5 Inter,ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }
    * { box-sizing:border-box; } [hidden] { display:none!important; }
    body { margin:0; color:var(--ink); background:var(--bg); }
    main { width:min(1120px,100%); margin:0 auto; padding:28px clamp(18px,4vw,52px) 72px; }
    h1,h2,h3,p { margin:0; } h1 { font-size:clamp(32px,5vw,58px); line-height:1.02; letter-spacing:-.04em; max-width:780px; } h2 { font-size:20px; } h3 { font-size:16px; }
    p { color:var(--muted); } code,pre,textarea { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; }
    .topbar { display:flex; align-items:center; justify-content:space-between; gap:16px; margin-bottom:52px; }
    .brand { display:flex; align-items:center; gap:10px; font-weight:750; letter-spacing:-.02em; }
    .pulse-dot { width:10px; height:10px; border-radius:50%; background:var(--green); box-shadow:0 0 0 6px var(--green-soft); }
    .boundary { display:flex; flex-wrap:wrap; gap:8px; color:var(--muted); font-size:13px; }
    .pill { padding:5px 9px; border:1px solid var(--line); border-radius:999px; background:rgba(255,255,255,.58); }
    .hero { display:grid; grid-template-columns:minmax(0,1.55fr) minmax(260px,.75fr); gap:26px; align-items:end; padding-bottom:34px; border-bottom:1px solid var(--line); }
    .eyebrow { margin-bottom:14px; color:var(--green); font-weight:700; text-transform:uppercase; letter-spacing:.08em; font-size:12px; }
    .hero-copy { margin-top:17px; max-width:680px; font-size:17px; }
    .action { padding:20px; border-radius:18px; background:var(--paper); border:1px solid var(--line); }
    .action strong { display:block; margin-bottom:6px; }
    .status-ready { border-left:5px solid var(--green); } .status-partial,.status-warming { border-left:5px solid var(--amber); } .status-blocked { border-left:5px solid var(--red); } .status-action { border-left:5px solid var(--blue); }
    .metrics { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:1px; margin:30px 0 46px; background:var(--line); border:1px solid var(--line); border-radius:18px; overflow:hidden; }
    .metric { min-height:142px; padding:22px; background:var(--paper); } .metric .value { display:block; margin:11px 0 5px; font-size:34px; line-height:1; font-weight:750; letter-spacing:-.04em; } .metric small { color:var(--muted); }
    section { margin-top:46px; } .section-head { display:flex; justify-content:space-between; gap:18px; align-items:end; margin-bottom:16px; } .section-head p { max-width:590px; }
    .tray { display:grid; gap:14px; } .tray-card { padding:22px; border:1px solid #dbcba8; border-radius:18px; background:#fffaf0; }
    .tray-card header { display:flex; justify-content:space-between; gap:12px; margin-bottom:14px; } .kind { color:var(--amber); font-weight:700; text-transform:uppercase; font-size:12px; letter-spacing:.07em; }
    .summary { font-size:18px; color:var(--ink); }
    details { margin-top:16px; } summary { cursor:pointer; color:var(--muted); } textarea { width:100%; min-height:150px; margin-top:10px; padding:12px; border:1px solid var(--line); border-radius:12px; background:white; resize:vertical; }
    .controls { display:flex; flex-wrap:wrap; gap:9px; margin-top:12px; } button { min-height:38px; padding:8px 13px; border:1px solid var(--line); border-radius:999px; background:white; color:var(--ink); cursor:pointer; } button.primary { background:var(--ink); color:white; border-color:var(--ink); } button.danger { background:var(--red); color:white; border-color:var(--red); } button:disabled { cursor:not-allowed; opacity:.58; }
    .memory-list { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; } .memory { min-height:150px; padding:20px; border:1px solid var(--line); border-radius:16px; background:var(--paper); } .memory .meta { display:flex; justify-content:space-between; gap:12px; margin-bottom:12px; color:var(--muted); font-size:12px; }
    .preview { padding:22px; border:1px solid var(--line); border-radius:18px; background:var(--paper); } .preview pre { white-space:pre-wrap; overflow-wrap:anywhere; color:#3b413d; }
    .authority-actions { display:flex; flex-wrap:wrap; gap:8px; margin-top:14px; }
    .protected-wipe { margin-top:14px; padding:22px; border:1px solid #d7b4b0; border-radius:18px; background:var(--red-soft); }
    .protected-wipe h3 { margin-bottom:8px; } .protected-wipe .warning { margin-top:10px; color:var(--ink); }
    .protected-wipe-review,.protected-wipe-receipt { margin-top:16px; padding:16px; border:1px solid #ca9b96; border-radius:14px; background:var(--paper); outline-offset:4px; }
    .protected-wipe-receipt { border-color:#9ab8a7; background:var(--green-soft); }
    .sr-only { position:absolute!important; width:1px!important; height:1px!important; padding:0!important; margin:-1px!important; overflow:hidden!important; clip:rect(0,0,0,0)!important; white-space:nowrap!important; border:0!important; }
    .empty { padding:24px; border:1px dashed #c9c8c1; border-radius:16px; color:var(--muted); }
    .receipt { margin-top:14px; color:var(--muted); font-size:12px; overflow-wrap:anywhere; }
    .logout { margin:0; } .logout button { background:transparent; }
    @media (max-width:760px) {
      .topbar { display:grid; grid-template-columns:1fr auto; align-items:start; margin-bottom:40px; }
      .boundary { grid-column:1 / -1; grid-row:2; }
      .logout { grid-column:2; grid-row:1; }
      .hero,.metrics,.memory-list { grid-template-columns:1fr; }
      .section-head { flex-direction:column; align-items:flex-start; gap:8px; }
      .section-head p { max-width:100%; }
    }
    @media (prefers-reduced-motion:reduce) { * { scroll-behavior:auto!important; } }
  </style>
</head>
<body>
<main>
  <div class="topbar">
    <div class="brand"><span class="pulse-dot"></span>Pulse</div>
    <div class="boundary"><span class="pill">{{.StoreLabel}}</span><span class="pill">Current project <code>{{.Data.Boundary.RepositoryID}}</code></span><span class="pill">Device local</span><span class="pill">Private</span></div>
    {{if .CSRFToken}}<form class="logout" method="post" action="logout"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button>Lock Home</button></form>{{end}}
  </div>

  <div class="hero">
    <div>
      <div class="eyebrow">Memory status</div>
      <h1>{{.StatusTitle}}</h1>
      <p class="hero-copy">{{.StatusDetail}}</p>
      <p class="meta">Exact check: <code>{{.Data.Readiness.ReasonCode}}</code> · checked {{.Data.Readiness.CheckedAt}}</p>
    </div>
    <div class="action status-{{.StatusTone}}"><strong>Next action</strong><p>{{.Data.Readiness.NextAction.Label}}</p></div>
  </div>

  <div class="metrics" aria-label="Memory proof">
    <div class="metric"><span>Saved memories</span><span class="value">{{.MemoryCount}}</span><small>Canonical, active, private records</small></div>
    <div class="metric"><span>Continuity</span><span class="value" style="font-size:25px">{{.ContextState}}</span><small>{{if .HasContext}}Receipt-backed delivery{{if .ContextHost}} via {{.ContextHost}}{{end}}{{else}}Waiting for a fresh task{{end}}</small></div>
    <div class="metric"><span>Token economy</span><span class="value">{{.EconomyValue}}</span><small>{{.EconomyDetail}}{{if .EstimatedPercent}} · {{.EstimatedPercent}}{{end}}</small></div>
  </div>

  <section id="authority-profile">
    <div class="section-head"><div><div class="eyebrow">Security boundary</div><h2>Protected actions</h2></div><p>Ordinary memory and Memory Home do not require enhanced verification. This profile controls only destructive vault and binding actions.</p></div>
    <div class="preview">
      <p>Profile <code>{{.EnhancedPresenceProfile.Schema}}</code> · version {{.EnhancedPresenceProfile.Version}} · adapter <code>{{.EnhancedPresenceProfile.Kind}}</code> · {{if .EnhancedPresenceProfile.Available}}available{{else}}unavailable{{end}}{{if .EnhancedPresenceProfile.ReasonCode}} · reason <code>{{.EnhancedPresenceProfile.ReasonCode}}</code>{{end}}</p>
      {{if .HasProtectedActions}}<div class="authority-actions" aria-label="Protected actions this profile can authorize">{{range .EnhancedPresenceProfile.ProtectedActions}}<span class="pill"><code>{{.}}</code></span>{{end}}</div>{{else}}<div class="empty" style="margin-top:14px">No protected actions can be authorized on this machine.</div>{{end}}
    </div>
    {{if .CanProtectedWipe}}
    <article class="protected-wipe" data-protected-wipe>
      <h3 id="protected-wipe-title">Delete this project’s stored Pulse records</h3>
      <p>This control is available only because the enhanced profile can authorize <code>vault.wipe</code>.</p>
      <p class="warning"><strong>Warning:</strong> this permanently removes the exact project-bound record set you review. It cannot be undone.</p>
      <div data-protected-wipe-idle class="controls"><button type="button" data-protected-wipe-begin>Review exact stored records</button></div>
      <div class="protected-wipe-review" data-protected-wipe-review hidden tabindex="-1" role="group" aria-labelledby="protected-wipe-title" aria-describedby="protected-wipe-warning">
        <p id="protected-wipe-warning"><strong>Permanent deletion.</strong> Pulse will delete exactly <strong data-protected-wipe-count></strong> stored records across memory and continuity, including supporting local receipts and projections. Record contents stay hidden.</p>
        <p>Approval expires at <time data-protected-wipe-expiry></time> · <span data-protected-wipe-countdown aria-hidden="true"></span><span data-protected-wipe-countdown-live class="sr-only" role="status" aria-live="polite" aria-atomic="true"></span></p>
        <div class="controls"><button type="button" class="danger" data-protected-wipe-complete>Verify with this device and delete exact records</button><button type="button" data-protected-wipe-cancel>Cancel</button></div>
      </div>
      <div class="protected-wipe-receipt" data-protected-wipe-receipt hidden tabindex="-1" role="group" aria-label="Protected wipe receipt">
        <h3>Deletion receipt</h3>
        <p>Deleted exactly <strong data-protected-wipe-receipt-count></strong> stored records across memory and continuity, including supporting local receipts and projections, at <time data-protected-wipe-receipt-time></time>.</p>
        <p class="receipt">Receipt <code data-protected-wipe-receipt-schema></code> · snapshot digest <code data-protected-wipe-receipt-digest></code></p>
      </div>
      <p class="receipt" data-protected-wipe-status role="status" aria-live="polite" aria-atomic="true">No deletion is prepared.</p>
    </article>
    {{end}}
  </section>

  {{if .UnassignedEnabled}}
  <section id="unassigned-inbox">
    <div class="section-head"><div><div class="eyebrow">Not in any project yet</div><h2>Unassigned Inbox</h2></div><p>These structured records are local and non-retrievable. Choose this exact project to move one into its ordinary Memory Tray, or delete it.</p></div>
    {{if .UnassignedUnavailable}}<div class="empty">Inbox is unavailable. No queued content was read or moved. Repair its private local file, then refresh Home.</div>{{else if .HasUnassigned}}
    <div class="tray">
      {{range .Unassigned}}
      <article class="tray-card">
        <header><span class="kind">{{.Kind}}</span><span class="pill">Unassigned · {{.Host}}</span></header>
        <p class="summary">{{.Summary}}</p>
        <div class="controls">
          <form method="post" action="unassigned/{{.ItemID}}/assign" data-home-mutation data-home-pending-label="Moving the exact digest to this project’s Tray…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="content_digest" value="{{.ContentDigest}}"><input type="hidden" name="expected_binding_digest" value="{{$.Data.Boundary.BindingDigest}}"><button class="primary">Move to this project’s Tray</button></form>
          <form method="post" action="unassigned/{{.ItemID}}/delete" data-home-mutation data-home-confirm="Delete this unassigned memory?" data-home-pending-label="Deleting the exact Inbox card…"><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="content_digest" value="{{.ContentDigest}}"><input type="hidden" name="expected_binding_digest" value="{{$.Data.Boundary.BindingDigest}}"><button>Delete</button></form>
        </div>
        <p class="receipt">Not counted as memory · captured {{.CreatedAt}} · digest {{.ContentDigest}}</p>
      </article>
      {{end}}
    </div>
    {{else}}<div class="empty">No unassigned memories. Work inside a bound project, or explicitly assign any future Inbox card here.</div>{{end}}
    {{if .HasUnassignedActivity}}<div class="memory-list" style="margin-top:14px">{{range .UnassignedActivity}}<article class="memory attempt"><div class="meta"><span>{{.ActionLabel}}</span><span>{{.CreatedAt}}</span></div><p class="summary">{{.Status}}</p><div class="receipt">Inbox receipt {{.ReceiptID}} · digest {{.ContentDigest}}</div></article>{{end}}</div>{{end}}
  </section>
  {{end}}

  {{if .HasPending}}
  <section id="memory-tray">
    <div class="section-head"><div><div class="eyebrow">Before Pulse saves</div><h2>Memory Tray</h2></div><p>See every proposed record. Edit it, save it now, or cancel it during the visible delay.</p></div>
    <div class="tray">
      {{range .Pending}}
      <article class="tray-card" data-candidate-id="{{.CandidateID}}" data-candidate-version="{{.Version}}">
        <header><span class="kind">{{.Kind}}</span><span class="pill">Pending</span></header>
        <p class="summary">{{.Summary}}</p>
        <details><summary>Edit the exact structured record</summary>
          <form method="post" action="tray/{{.CandidateID}}/edit" data-home-mutation>
            <input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_version" value="{{.Version}}">
            <textarea name="candidate_json">{{.CandidateJSON}}</textarea>
            <div class="controls"><button class="primary" type="submit">Save edit</button></div>
          </form>
        </details>
        <div class="controls">
          <form method="post" action="tray/{{.CandidateID}}/commit" data-home-mutation><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_version" value="{{.Version}}"><button class="primary">Save after delay</button></form>
          <form method="post" action="tray/{{.CandidateID}}/cancel" data-home-mutation><input type="hidden" name="csrf_token" value="{{$.CSRFToken}}"><input type="hidden" name="expected_version" value="{{.Version}}"><button>Cancel</button></form>
        </div>
        <p class="receipt" data-home-status aria-live="polite">Visible delay starts after this card is shown.</p>
      </article>
      {{end}}
    </div>
  </section>
  {{end}}

  <section>
    <div class="section-head"><div><div class="eyebrow">Canonical memory</div><h2>Latest memories</h2></div><p>Only successfully committed records appear here. Attempts stay separate.</p></div>
    {{if .HasLatestMemory}}<div class="memory-list">{{range .Data.Memories.LatestActive}}<article class="memory"><div class="meta"><span>{{.Kind}}</span><span>{{.CreatedAt}}</span></div><p class="summary">{{.RedactedSummary}}</p><div class="receipt">Object {{.ObjectID}} · Host {{.Host}} · Session {{.SessionRef}}<br>Terminal receipt {{.TerminalReceiptID}} · Presentation receipt {{.PresentationReceiptID}}</div></article>{{end}}</div>{{else}}<div class="empty">No canonical memory yet. The first approved Tray card will appear here with its receipt.</div>{{end}}
  </section>

  {{if .HasAttempts}}
  <section id="memory-attempts">
    <div class="section-head"><div><div class="eyebrow">Non-canonical activity</div><h2>Recent save attempts</h2></div><p>Pending, canceled, failed, and rejected attempts stay separate from canonical memory.</p></div>
    <div class="memory-list">{{range .Attempts}}<article class="memory attempt"><div class="meta"><span>{{.Kind}} · {{.State}}</span><span>{{.CreatedAt}}</span></div><p class="summary">{{.RedactedSummary}}</p><div class="receipt">Attempt receipt {{.ReceiptID}} · Status {{.ReceiptStatus}}</div></article>{{end}}</div>
  </section>
  {{end}}

  <section>
    <div class="section-head"><div><div class="eyebrow">Cross-task continuity</div><h2>What the next task receives</h2></div><p>This is a bounded preview, not a promise that the host consumed it.</p></div>
    {{if .HasNextPreview}}<div class="preview"><pre>{{.Data.NextTaskPreview.RedactedResume}}</pre><div class="receipt">{{.Data.NextTaskPreview.PulseTokens}} Pulse tokens · preview only · digest {{.Data.NextTaskPreview.PayloadDigest}}</div></div>{{else}}<div class="empty">No safe preview is available yet. Pulse will not expose path-like, secret-like, or raw transcript content here.</div>{{end}}
  </section>
  <script src="assets/home.js" defer></script>
</main>
</body>
</html>`))
