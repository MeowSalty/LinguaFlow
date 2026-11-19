package service

import responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"

func (g *DatabaseGlossary) ResponsePublicationDomain(scope responsecontract.PublicationDomain) (responsecontract.PublicationDomain, error) {
	domain := responsecontract.PublicationDomain{Medium: responsecontract.PublicationSQL, Authority: responsecontract.RegisterPublicationAuthority(g.client), JobID: g.executionJobID, ProjectID: g.projectID}
	return domain, responsecontract.ValidatePublication(scope, domain)
}
func (g *executionGlossary) ResponsePublicationDomain(scope responsecontract.PublicationDomain) (responsecontract.PublicationDomain, error) {
	if g.database != nil {
		return g.database.ResponsePublicationDomain(scope)
	}
	if scope.Medium != responsecontract.PublicationMemory || scope.Authority == nil {
		return responsecontract.PublicationDomain{}, responsecontract.ErrPublicationDomain
	}
	return scope, nil
}
