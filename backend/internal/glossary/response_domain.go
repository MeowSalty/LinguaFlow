package glossary

import responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"

func (g *FileGlossary) ResponsePublicationDomain(scope responsecontract.PublicationDomain) (responsecontract.PublicationDomain, error) {
	if scope.Medium != responsecontract.PublicationMemory || scope.Authority == nil {
		return responsecontract.PublicationDomain{}, responsecontract.ErrPublicationDomain
	}
	return scope, nil
}
