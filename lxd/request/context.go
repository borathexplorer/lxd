package request

import (
	"context"
	"fmt"
	"net"
	"net/http"
)

// Info represents the request information that are stored in the request
// context, which is passed around.
type Info struct {
	// SourceAddress is the request's source address.
	SourceAddress string

	// Address represents the final destination address of the request.
	Address string

	// Username used for the original connection.
	Username string

	// Protocol used for the original connection.
	Protocol string

	// IdentityProviderGroups represent identity provider groups defined by the
	// identity provider if the identity authenticated with OIDC.
	IdentityProviderGroups []string

	// ForwardedAddress represents an address of a cluster member from where the request was forwarded.
	ForwardedAddress string

	// ForwardedUsername represents username used on another cluster member.
	ForwardedUsername string

	// ForwardedProtocol represents protocol used on another cluster member.
	ForwardedProtocol string

	// ForwardedIdentityProviderGroups represents identity provider groups defined by
	// the identity provider if the identity authenticated with OIDC on another cluster
	// member.
	ForwardedIdentityProviderGroups []string

	// EffectiveProjectName is used to indicate that the effective project of a resource is different from the project
	// specified in the URL. (For example, if a project has `features.networks=false`, any networks in this project actually
	// belong to the default project).
	EffectiveProjectName string

	// Trusted indicates whether the request was authenticated or not.
	Trusted bool

	// Conn represents the request connection.
	Conn net.Conn
}

// InitContextInfo sets an empty Info in the request context.
func InitContextInfo(r *http.Request) *Info {
	info := &Info{}
	SetContextValue(r, CtxRequestInfo, info)
	return info
}

// GetContextInfo gets the request information from the request context.
func GetContextInfo(ctx context.Context) *Info {
	info, ok := ctx.Value(CtxRequestInfo).(*Info)
	if !ok {
		return nil
	}

	return info
}

// IsRequestContext checks if the given context is a request context.
// This is determined by checking the presence of the request information in the context.
func IsRequestContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}

	return GetContextInfo(ctx) != nil
}

// GetContextValue gets a value of type T from the context using the given key.
func GetContextValue[T any](ctx context.Context, key CtxKey) (T, error) {
	var empty T
	valueAny := ctx.Value(key)
	if valueAny == nil {
		return empty, fmt.Errorf("Failed to get expected value %q from context", key)
	}

	value, ok := valueAny.(T)
	if !ok {
		return empty, fmt.Errorf("Value for context key %q has incorrect type (expected %T, got %T)", key, empty, valueAny)
	}

	return value, nil
}

// SetContextValue sets the given value in the request context with the given key.
func SetContextValue(r *http.Request, key CtxKey, value any) {
	rWithCtx := r.WithContext(context.WithValue(r.Context(), key, value))
	*r = *rWithCtx
}
