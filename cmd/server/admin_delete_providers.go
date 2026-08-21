package main

import "github.com/aero-vault/aero-vault/internal/api/rest"

const defaultAdminDeleteProviderName = "admin-matrix"

// adminDeleteProviders is the composition-root registry for the REST
// administrative delete boundary. Keeping name resolution here lets a future
// policy adapter be selected without coupling the protocol package to it.
var adminDeleteProviders = map[string]rest.AuthorizationProvider{
	defaultAdminDeleteProviderName: rest.AdminMatrixProvider{},
}

func configuredAdminDeleteProvider() rest.AuthorizationProvider {
	return adminDeleteProviders[defaultAdminDeleteProviderName]
}
