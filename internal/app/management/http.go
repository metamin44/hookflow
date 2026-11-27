package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/metamin44/hookflow/internal/modules/apikey"
	"github.com/metamin44/hookflow/internal/modules/endpoint"
	"github.com/metamin44/hookflow/internal/modules/orgsubscription"
	"github.com/metamin44/hookflow/internal/modules/subscription"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

const (
	organizationsPath             = "/v1/organizations/{organization_id}"
	endpointsPath                 = organizationsPath + "/endpoints"
	endpointPath                  = endpointsPath + "/{endpoint_id}"
	rotatePath                    = endpointPath + "/secret/rotate"
	subscriptionsPath             = endpointPath + "/subscriptions"
	subscriptionPath              = subscriptionsPath + "/{subscription_id}"
	organizationSubscriptionPath  = organizationsPath + "/subscription"
	organizationSubscriptionsPath = organizationsPath + "/subscriptions"
)

// Handler mounts the management routes on an http.ServeMux.
type Handler struct {
	service *Service
	auth    *Authenticator
	events  EventAcceptor
	logger  *slog.Logger
	mux     *http.ServeMux
}

// NewHandler builds the management HTTP handler.
func NewHandler(service *Service, auth *Authenticator, events EventAcceptor, logger *slog.Logger) (*Handler, error) {
	if service == nil {
		return nil, errors.New("management: service is required")
	}
	if auth == nil {
		return nil, errors.New("management: authenticator is required")
	}
	if events == nil {
		return nil, errors.New("management: event acceptor is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{service: service, auth: auth, events: events, logger: logger, mux: http.NewServeMux()}
	h.routes()
	return h, nil
}

// Register mounts the management routes on an existing mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("/v1/", h)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) routes() {
	h.mux.HandleFunc("POST "+eventsPath, h.ingestEvent)
	h.mux.HandleFunc("GET "+eventsPath, h.listEvents)
	h.mux.HandleFunc("GET "+eventPath, h.getEvent)
	h.mux.HandleFunc("GET "+deliveriesPath, h.listDeliveries)
	h.mux.HandleFunc("GET "+deliveryPath, h.getDelivery)
	h.mux.HandleFunc("GET "+attemptsPath, h.listDeliveryAttempts)
	h.mux.HandleFunc("POST "+retryPath, h.retryDelivery)

	h.mux.HandleFunc("POST "+endpointsPath, h.createEndpoint)
	h.mux.HandleFunc("GET "+endpointsPath, h.listEndpoints)
	h.mux.HandleFunc("GET "+endpointPath, h.getEndpoint)
	h.mux.HandleFunc("PATCH "+endpointPath, h.patchEndpoint)
	h.mux.HandleFunc("DELETE "+endpointPath, h.deleteEndpoint)
	h.mux.HandleFunc("POST "+rotatePath, h.rotateSecret)
	h.mux.HandleFunc("GET "+subscriptionsPath, h.listSubscriptions)
	h.mux.HandleFunc("POST "+subscriptionsPath, h.createSubscription)
	h.mux.HandleFunc("DELETE "+subscriptionPath, h.deleteSubscription)

	h.mux.HandleFunc("GET "+organizationSubscriptionPath, h.getOrganizationSubscription)
	h.mux.HandleFunc("PUT "+organizationSubscriptionPath, h.putOrganizationSubscription)
	h.mux.HandleFunc("DELETE "+organizationSubscriptionPath, h.cancelOrganizationSubscription)
	h.mux.HandleFunc("GET "+organizationSubscriptionsPath, h.listOrganizationSubscriptions)

	h.mux.HandleFunc("GET "+operationsOverviewPath, h.getOperationalOverview)
	h.mux.HandleFunc("GET "+operationsEndpointsPath, h.getOperationalEndpointHealth)

	h.mux.HandleFunc(endpointsPath, h.methodNotAllowed("GET, POST"))
	h.mux.HandleFunc(endpointPath, h.methodNotAllowed("GET, PATCH, DELETE"))
	h.mux.HandleFunc(rotatePath, h.methodNotAllowed("POST"))
	h.mux.HandleFunc(subscriptionsPath, h.methodNotAllowed("GET, POST"))
	h.mux.HandleFunc(subscriptionPath, h.methodNotAllowed("DELETE"))
	h.mux.HandleFunc(organizationSubscriptionPath, h.methodNotAllowed("GET, PUT, DELETE"))
	h.mux.HandleFunc(organizationSubscriptionsPath, h.methodNotAllowed("GET"))
	h.mux.HandleFunc(operationsOverviewPath, h.methodNotAllowed("GET"))
	h.mux.HandleFunc(operationsEndpointsPath, h.methodNotAllowed("GET"))
	h.mux.HandleFunc(eventsPath, h.methodNotAllowed("GET, POST"))
	h.mux.HandleFunc(eventPath, h.methodNotAllowed("GET"))
	h.mux.HandleFunc(deliveriesPath, h.methodNotAllowed("GET"))
	h.mux.HandleFunc(deliveryPath, h.methodNotAllowed("GET"))
	h.mux.HandleFunc(attemptsPath, h.methodNotAllowed("GET"))
	h.mux.HandleFunc(retryPath, h.methodNotAllowed("POST"))
	h.mux.HandleFunc("/", h.unknownRoute)
}

func (h *Handler) createEndpoint(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsEndpointWrite() {
		h.writeError(w, r, insufficientScope())
		return
	}
	document, appErr := decodeObject(w, r)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	decoded, appErr := decodeCreate(document)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	record, secret, err := h.service.CreateEndpoint(r.Context(), identity, requestMeta(r), CreateInput{
		Name:        decoded.Name,
		Description: decoded.Description,
		URL:         decoded.URL,
		Headers:     decoded.Headers,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	fullRepresentation := record.Represent()
	setETag(w, fullRepresentation, record.SecretEncrypted)
	w.Header().Set("Location", endpointLocation(record.OrganizationID, record.ID))
	writeJSON(w, http.StatusCreated, createEndpointResponse{Representation: record.RedactedRepresent(), Secret: secret})
}

func (h *Handler) listEndpoints(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsEndpointRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	records, err := h.service.ListEndpoints(r.Context(), identity.OrganizationID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	documents := make([]endpoint.Representation, 0, len(records))
	for _, record := range records {
		documents = append(documents, record.RedactedRepresent())
	}
	writeJSON(w, http.StatusOK, endpointsResponse{Endpoints: documents})
}

func (h *Handler) getEndpoint(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsEndpointRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	record, err := h.service.GetEndpoint(r.Context(), identity.OrganizationID, r.PathValue("endpoint_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	fullRepresentation := record.Represent()
	setETag(w, fullRepresentation, record.SecretEncrypted)
	writeJSON(w, http.StatusOK, record.RedactedRepresent())
}

func (h *Handler) patchEndpoint(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsEndpointWrite() {
		h.writeError(w, r, insufficientScope())
		return
	}
	document, appErr := decodeObject(w, r)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	decoded, appErr := decodePatch(document)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	result, err := h.service.PatchEndpoint(r.Context(), identity, requestMeta(r), r.PathValue("endpoint_id"),
		PatchInput(decoded), ifMatchHeader(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	fullRepresentation := result.Record.Represent()
	setETag(w, fullRepresentation, result.Record.SecretEncrypted)
	writeJSON(w, http.StatusOK, result.Record.RedactedRepresent())
}

func (h *Handler) deleteEndpoint(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsEndpointWrite() {
		h.writeError(w, r, insufficientScope())
		return
	}
	if err := h.service.DeleteEndpoint(r.Context(), identity, requestMeta(r), r.PathValue("endpoint_id")); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) rotateSecret(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsSecretRotate() {
		h.writeError(w, r, insufficientScope())
		return
	}
	record, secret, err := h.service.RotateSecret(r.Context(), identity, requestMeta(r),
		r.PathValue("endpoint_id"), ifMatchHeader(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	setETag(w, record.Represent(), record.SecretEncrypted)
	writeJSON(w, http.StatusOK, secretResponse{Secret: secret})
}

func (h *Handler) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsEndpointRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	records, err := h.service.ListSubscriptions(r.Context(), identity.OrganizationID, r.PathValue("endpoint_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	documents := make([]subscription.Representation, 0, len(records))
	for _, record := range records {
		documents = append(documents, record.Represent())
	}
	writeJSON(w, http.StatusOK, subscriptionsResponse{Subscriptions: documents})
}

func (h *Handler) createSubscription(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsEndpointWrite() {
		h.writeError(w, r, insufficientScope())
		return
	}
	document, appErr := decodeObject(w, r)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	eventType, appErr := decodeEventType(document)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	record, created, err := h.service.CreateSubscription(r.Context(), identity, requestMeta(r),
		r.PathValue("endpoint_id"), eventType)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", subscriptionLocation(identity.OrganizationID, record.EndpointID, record.ID))
	}
	writeJSON(w, status, record.Represent())
}

func (h *Handler) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsEndpointWrite() {
		h.writeError(w, r, insufficientScope())
		return
	}
	record, _, err := h.service.DeactivateSubscription(r.Context(), identity, requestMeta(r),
		r.PathValue("endpoint_id"), r.PathValue("subscription_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, record.Represent())
}

func (h *Handler) getOrganizationSubscription(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsBillingRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	record, err := h.service.GetOrganizationSubscription(r.Context(), identity.OrganizationID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	setOrganizationSubscriptionETag(w, record)
	writeJSON(w, http.StatusOK, record.Represent())
}

func (h *Handler) listOrganizationSubscriptions(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsBillingRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	records, err := h.service.ListOrganizationSubscriptions(r.Context(), identity.OrganizationID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	documents := make([]orgsubscription.Representation, 0, len(records))
	for _, record := range records {
		documents = append(documents, record.Represent())
	}
	writeJSON(w, http.StatusOK, organizationSubscriptionsResponse{Subscriptions: documents})
}

func (h *Handler) putOrganizationSubscription(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsBillingWrite() {
		h.writeError(w, r, insufficientScope())
		return
	}
	document, appErr := decodeObject(w, r)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	decoded, appErr := decodeOrganizationSubscription(document)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	result, err := h.service.PutOrganizationSubscription(r.Context(), identity, requestMeta(r),
		OrganizationSubscriptionInput(decoded), ifMatchHeader(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	setOrganizationSubscriptionETag(w, result.Record)
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
		w.Header().Set("Location", "/v1/organizations/"+identity.OrganizationID+"/subscription")
	}
	writeJSON(w, status, result.Record.Represent())
}

func (h *Handler) cancelOrganizationSubscription(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsBillingWrite() {
		h.writeError(w, r, insufficientScope())
		return
	}
	record, err := h.service.CancelOrganizationSubscription(r.Context(), identity, requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	setOrganizationSubscriptionETag(w, record)
	writeJSON(w, http.StatusOK, record.Represent())
}

func (h *Handler) methodNotAllowed(allow string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.writeError(w, r, methodNotAllowed(allow))
	}
}

func (h *Handler) unknownRoute(w http.ResponseWriter, r *http.Request) {
	h.writeError(w, r, notFound())
}

func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (Identity, apikey.Set, bool) {
	identity, scopes, err := h.auth.Authenticate(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		h.writeError(w, r, err)
		return Identity{}, apikey.Set{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return identity, scopes, true
}

// sameTenant rejects any path organization that is not the authenticated
// tenant, and any syntactically invalid resource identifier, with 404. It never
// reveals whether another organization owns the resource.
func (h *Handler) sameTenant(w http.ResponseWriter, r *http.Request, identity Identity) bool {
	pathOrg, err := secrets.ParseUUID(r.PathValue("organization_id"))
	if err != nil {
		h.writeError(w, r, notFound())
		return false
	}
	own, err := secrets.ParseUUID(identity.OrganizationID)
	if err != nil || own != pathOrg {
		h.writeError(w, r, notFound())
		return false
	}
	for _, name := range []string{"endpoint_id", "subscription_id"} {
		if value := r.PathValue(name); value != "" {
			if _, err := secrets.ParseUUID(value); err != nil {
				h.writeError(w, r, notFound())
				return false
			}
		}
	}
	return true
}

func insufficientScope() *HTTPError {
	return forbidden("insufficient_scope", "the API key does not grant the required scope")
}

func ifMatchHeader(r *http.Request) string {
	values := r.Header.Values("If-Match")
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, ",")
}

func setETag(w http.ResponseWriter, representation endpoint.Representation, secretEncrypted string) {
	w.Header().Set("ETag", endpoint.QuotedHeader(endpoint.StrongTag(representation, secretEncrypted)))
}

func setOrganizationSubscriptionETag(w http.ResponseWriter, record orgsubscription.Record) {
	w.Header().Set("ETag", endpoint.QuotedHeader(orgsubscription.StrongTag(record.Represent())))
}

func endpointLocation(organizationID, endpointID string) string {
	return "/v1/organizations/" + organizationID + "/endpoints/" + endpointID
}

func subscriptionLocation(organizationID, endpointID, subscriptionID string) string {
	return endpointLocation(organizationID, endpointID) + "/subscriptions/" + subscriptionID
}

type endpointsResponse struct {
	Endpoints []endpoint.Representation `json:"endpoints"`
}

type subscriptionsResponse struct {
	Subscriptions []subscription.Representation `json:"subscriptions"`
}

type organizationSubscriptionsResponse struct {
	Subscriptions []orgsubscription.Representation `json:"subscriptions"`
}

type createEndpointResponse struct {
	endpoint.Representation
	Secret string `json:"secret"`
}

type secretResponse struct {
	Secret string `json:"secret"`
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
	Field  string `json:"field,omitempty"`
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		h.logger.Error("management request failed",
			"method", r.Method, "path", r.URL.Path, "error_type", fmt.Sprintf("%T", err))
		httpErr = internalError()
	}
	for name, value := range httpErr.Headers {
		w.Header().Set(name, value)
	}
	if httpErr.ETag != "" {
		w.Header().Set("ETag", endpoint.QuotedHeader(httpErr.ETag))
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(httpErr.Status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:   "about:blank",
		Title:  http.StatusText(httpErr.Status),
		Status: httpErr.Status,
		Detail: httpErr.Detail,
		Code:   httpErr.Code,
		Field:  httpErr.Field,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

const maxUserAgentBytes = 512

func requestMeta(r *http.Request) RequestMeta {
	var meta RequestMeta
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			value := ip.String()
			meta.IP = &value
		}
	} else if ip := net.ParseIP(r.RemoteAddr); ip != nil {
		value := ip.String()
		meta.IP = &value
	}
	if agent := truncateUTF8(r.UserAgent(), maxUserAgentBytes); agent != "" {
		meta.UserAgent = &agent
	}
	return meta
}

// truncateUTF8 bounds a string by bytes without splitting a rune.
func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && (value[cut]&0xC0) == 0x80 {
		cut--
	}
	return value[:cut]
}

func decodeEventType(document map[string]json.RawMessage) (string, *HTTPError) {
	for key := range document {
		if key != "event_type" {
			return "", unknownField(key)
		}
	}
	raw, present := document["event_type"]
	if !present {
		return "", fieldRejection("event_type", "invalid_event_type", "event_type is required")
	}
	var eventType string
	if err := json.Unmarshal(raw, &eventType); err != nil {
		return "", fieldRejection("event_type", "invalid_event_type", "event_type must be a string")
	}
	if !subscription.ValidateEventType(eventType) {
		return "", fieldRejection("event_type", "invalid_event_type",
			"event_type must be 1 to 160 lowercase characters from a-z, 0-9, '_', '.' and '-'")
	}
	return eventType, nil
}
