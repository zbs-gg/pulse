package teamread

import (
	"context"
	"errors"
	"strings"

	"github.com/nkkmnk/pulse/internal/retrieve"
	"github.com/nkkmnk/pulse/internal/store"
	"github.com/nkkmnk/pulse/internal/teamauth"
)

var ErrInvalidRequest = errors.New("invalid team read request")

type ActiveContext struct {
	ProjectID string
	RepoID    string
	AgentID   string
	SessionID string
}

type Authorization struct {
	PrincipalID  string
	TeamID       string
	Capabilities []teamauth.Capability
}

type RecallRequest struct {
	Query          string
	ActiveContext  ActiveContext
	PrivacyCeiling string
	Retention      string
	Limit          int
}

type ContextRequest struct {
	Query          string
	ActiveContext  ActiveContext
	PrivacyCeiling string
	Retention      string
	Limit          int
	IncludeTrace   bool
	GraphMode      string
}

type ResumeRequest struct {
	ActiveContext ActiveContext
	ThreadID      string
	Limit         int
}

type Service struct {
	store  teamStore
	engine *retrieve.TeamRetrievalEngine
}

func New(teamStore *store.Store, engine *retrieve.TeamRetrievalEngine) *Service {
	return newService(teamStore, engine)
}

func newService(teamStore teamStore, engine *retrieve.TeamRetrievalEngine) *Service {
	return &Service{store: teamStore, engine: engine}
}

func (service *Service) Recall(
	ctx context.Context,
	authorization Authorization,
	request RecallRequest,
) (retrieve.TeamRetrievalResponse, error) {
	if service == nil || service.store == nil || service.engine == nil ||
		!validAuthorization(authorization) || strings.TrimSpace(request.Query) == "" ||
		request.Limit < 1 || request.Limit > 50 {
		return retrieve.TeamRetrievalResponse{}, ErrInvalidRequest
	}
	filter, err := service.buildFilter(ctx, authorization, request.ActiveContext,
		request.PrivacyCeiling, request.Retention)
	if err != nil {
		return retrieve.TeamRetrievalResponse{}, err
	}
	repository := &authorizedRepository{store: service.store, filter: filter}
	return service.engine.Retrieve(ctx, repository, retrieve.TeamRetrievalRequest{
		Query: request.Query, TopK: request.Limit,
	})
}

func (service *Service) Context(
	ctx context.Context,
	authorization Authorization,
	request ContextRequest,
) (retrieve.TeamContextResponse, error) {
	if service == nil || service.store == nil || service.engine == nil ||
		!validAuthorization(authorization) || strings.TrimSpace(request.Query) == "" ||
		request.Limit < 1 || request.Limit > 50 || !validGraphMode(request.GraphMode) {
		return retrieve.TeamContextResponse{}, ErrInvalidRequest
	}
	filter, err := service.buildFilter(ctx, authorization, request.ActiveContext,
		request.PrivacyCeiling, request.Retention)
	if err != nil {
		return retrieve.TeamContextResponse{}, err
	}
	repository := &authorizedRepository{store: service.store, filter: filter}
	return service.engine.Context(ctx, repository, retrieve.TeamContextRequest{
		Query: request.Query, TopK: request.Limit,
		GraphMode: retrieve.TeamGraphMode(request.GraphMode), IncludeTrace: request.IncludeTrace,
	})
}

func (service *Service) Resume(
	ctx context.Context,
	authorization Authorization,
	request ResumeRequest,
) (retrieve.TeamResumeResponse, error) {
	if service == nil || service.store == nil || service.engine == nil ||
		!validAuthorization(authorization) || request.Limit < 1 || request.Limit > 50 ||
		(strings.TrimSpace(request.ThreadID) == "" && request.ActiveContext.ProjectID == "" &&
			request.ActiveContext.SessionID == "") {
		return retrieve.TeamResumeResponse{}, ErrInvalidRequest
	}
	filter, err := service.buildFilter(
		ctx, authorization, request.ActiveContext, "normal", "",
	)
	if err != nil {
		return retrieve.TeamResumeResponse{}, err
	}
	repository := &authorizedRepository{store: service.store, filter: filter}
	return service.engine.Resume(ctx, repository, retrieve.TeamResumeRequest{
		ThreadID: request.ThreadID, ProjectID: request.ActiveContext.ProjectID,
		SessionID: request.ActiveContext.SessionID, Limit: request.Limit,
	})
}

func (service *Service) buildFilter(
	ctx context.Context,
	authorization Authorization,
	active ActiveContext,
	privacyCeiling, retention string,
) (store.AuthorizedCandidateFilter, error) {
	return service.store.BuildAuthorizedCandidateFilter(ctx, store.CandidateFilterRequest{
		PrincipalID:  authorization.PrincipalID,
		Capabilities: append([]teamauth.Capability(nil), authorization.Capabilities...),
		Context: teamauth.ActiveContext{
			TeamID: authorization.TeamID, ProjectID: active.ProjectID,
			RepoID: active.RepoID, AgentID: active.AgentID, SessionID: active.SessionID,
		},
		PrivacyCeiling: privacyCeiling,
		Retention:      retention,
	})
}

func validAuthorization(authorization Authorization) bool {
	return authorization.PrincipalID != "" && authorization.TeamID != "" &&
		len(authorization.Capabilities) > 0
}

func validGraphMode(mode string) bool {
	switch mode {
	case "off", "anchored", "walk":
		return true
	default:
		return false
	}
}
