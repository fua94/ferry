package ferry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/max1874/ferry/internal/webui"
)

const (
	maxJSONBody       = 512 << 10
	multipartOverhead = 1 << 20
)

type server struct {
	store    *Store
	logger   *log.Logger
	accessMu sync.Mutex
	now      func() time.Time
	links    downloadLinks
}

type HandlerOptions struct {
	AllowLANHosts bool
	// TrustedOrigin is the browser origin of a reverse proxy in front of Ferry,
	// such as "https://ferry.example.com". Empty disables proxy access.
	TrustedOrigin string
	Logger        *log.Logger
}

func NewHandler(store *Store, options HandlerOptions) http.Handler {
	return newHandler(store, options, time.Now)
}

func newHandler(store *Store, options HandlerOptions, now func() time.Time) http.Handler {
	if options.Logger == nil {
		options.Logger = log.Default()
	}
	boundary := boundaryPolicy{allowLANHosts: options.AllowLANHosts}
	if options.TrustedOrigin != "" {
		origin, err := ParseTrustedOrigin(options.TrustedOrigin)
		if err != nil {
			panic(err)
		}
		boundary.trusted = &origin
	}
	s := &server{store: store, logger: options.Logger, now: now, links: downloadLinks{entries: map[[32]byte]downloadLink{}}}
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/session", s.session)
	api.HandleFunc("GET /api/v1/messages", s.listMessages)
	api.HandleFunc("GET /api/v1/channels", s.listChannels)
	api.HandleFunc("POST /api/v1/channels", s.createChannel)
	api.HandleFunc("POST /api/v1/channels/join", s.joinChannel)
	api.HandleFunc("DELETE /api/v1/messages/{message_id}", s.retractMessage)
	api.HandleFunc("POST /api/v1/messages/text", s.createText)
	api.HandleFunc("POST /api/v1/messages/file", s.createFile)
	api.HandleFunc("GET /api/v1/files/{message_id}", s.downloadFile)
	api.HandleFunc("POST /api/v1/files/{message_id}/link", s.createDownloadLink)
	api.HandleFunc("GET /api/v1/devices", s.listDevices)
	api.HandleFunc("DELETE /api/v1/devices/{device_id}", s.deleteDevice)
	api.HandleFunc("GET /api/v1/settings/access", s.accessSettings)
	api.HandleFunc("PUT /api/v1/settings/access", s.updateAccessSettings)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/access", s.accessStatus)
	mux.HandleFunc("POST /api/v1/access/join", s.join)
	mux.HandleFunc("GET /api/v1/downloads/{ticket}", s.downloadByLink)
	mux.Handle("/api/v1/", s.requireDevice(api))
	mux.Handle("/", webui.Handler())
	return requestBoundary(securityHeaders(mux), boundary)
}

// TrustedOrigin is a reverse-proxy origin reduced to its scheme and to a host
// with the scheme's default port removed, so it compares equal to Host and
// Origin headers however the proxy or browser spells the port.
type TrustedOrigin struct {
	scheme string
	host   string
}

func (o TrustedOrigin) String() string {
	return o.scheme + "://" + o.host
}

// ParseTrustedOrigin accepts only a bare http or https origin: no credentials,
// path, query or fragment, because browsers never send those in Origin.
func ParseTrustedOrigin(value string) (TrustedOrigin, error) {
	invalid := errors.New("trusted origin must look like https://ferry.example.com")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return TrustedOrigin{}, invalid
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" || parsed.Hostname() == "" {
		return TrustedOrigin{}, invalid
	}
	if portText := parsed.Port(); portText != "" {
		if port, err := strconv.Atoi(portText); err != nil || port < 1 || port > 65535 {
			return TrustedOrigin{}, invalid
		}
	}
	return TrustedOrigin{scheme: scheme, host: canonicalHost(parsed.Host, scheme)}, nil
}

// canonicalHost lowercases a Host-style value and drops the scheme's default port.
func canonicalHost(hostPort, scheme string) string {
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		host, port = strings.Trim(hostPort, "[]"), ""
	}
	host = strings.ToLower(host)
	if scheme == "http" && port == "80" || scheme == "https" && port == "443" {
		port = ""
	}
	if port != "" {
		return net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

type boundaryPolicy struct {
	allowLANHosts bool
	trusted       *TrustedOrigin
}

func (p boundaryPolicy) isTrustedHost(hostPort string) bool {
	return p.trusted != nil && hostPort != "" && canonicalHost(hostPort, p.trusted.scheme) == p.trusted.host
}

// isTrustedOriginWrite admits a browser write that a TLS-terminating proxy
// forwarded over HTTP: the single Origin and the Host must both name the
// configured proxy origin.
func (p boundaryPolicy) isTrustedOriginWrite(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if p.trusted == nil || len(origins) != 1 || !p.isTrustedHost(r.Host) {
		return false
	}
	parsed, err := url.Parse(origins[0])
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" {
		return false
	}
	scheme := strings.ToLower(parsed.Scheme)
	return scheme == p.trusted.scheme && canonicalHost(parsed.Host, scheme) == p.trusted.host
}

func requestBoundary(next http.Handler, policy boundaryPolicy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedHost(r.Host, policy.allowLANHosts) && !policy.isTrustedHost(r.Host) {
			writeError(w, http.StatusMisdirectedRequest, "invalid_host", "Host must be localhost, an allowed IP address, or the trusted origin")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !sameOriginOrNative(r) && !policy.isTrustedOriginWrite(r) {
			writeError(w, http.StatusForbidden, "cross_origin_denied", "cross-origin writes are not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isAllowedHost(hostPort string, allowLANHosts bool) bool {
	host := hostPort
	if parsedHost, _, err := net.SplitHostPort(hostPort); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	if address == nil {
		return false
	}
	return address.IsLoopback() || allowLANHosts && (address.IsPrivate() || address.IsLinkLocalUnicast())
}

func sameOriginOrNative(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	parsed, err := url.Parse(origins[0])
	expectedScheme := "http"
	if r.TLS != nil {
		expectedScheme = "https"
	}
	return err == nil && parsed.Scheme == expectedScheme && parsed.Host != "" && strings.EqualFold(parsed.Host, r.Host)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type deviceContextKey struct{}

func (s *server) requireDevice(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := requestDeviceToken(r)
		if !ok {
			s.unauthorized(w)
			return
		}
		hash, ok := hashDeviceToken(token)
		if !ok {
			s.unauthorized(w)
			return
		}
		device, err := s.store.AuthenticateDevice(r.Context(), hash)
		if errors.Is(err, ErrNotFound) {
			s.unauthorized(w)
			return
		}
		if err != nil {
			s.internalError(w, err)
			return
		}
		ctx := context.WithValue(r.Context(), deviceContextKey{}, device)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestDeviceToken(r *http.Request) (string, bool) {
	authorizationValues := r.Header.Values("Authorization")
	if len(authorizationValues) != 1 {
		return "", false
	}
	scheme, token, found := strings.Cut(authorizationValues[0], " ")
	token = strings.TrimLeft(token, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return token, token != "" && !strings.ContainsAny(token, " \t\r\n,")
}

func currentDevice(r *http.Request) Device {
	return r.Context().Value(deviceContextKey{}).(Device)
}

// channelFromQuery reads the optional `channel` query parameter. Absent or
// empty keeps the pre-channel contract: everything happens in "default".
func channelFromQuery(r *http.Request) (string, error) {
	value := r.URL.Query().Get("channel")
	if value == "" {
		return DefaultChannelID, nil
	}
	return normalizeChannelID(value)
}

// authorizedChannel resolves the request's channel and proves the current
// device belongs to it. A malformed slug is a 400; an unknown channel and one
// the device is not in are the same 404, so neither reveals the other.
func (s *server) authorizedChannel(w http.ResponseWriter, r *http.Request) (string, bool) {
	channelID, err := channelFromQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return "", false
	}
	member, err := s.store.IsChannelMember(r.Context(), currentDevice(r).ID, channelID)
	if err != nil {
		s.internalError(w, err)
		return "", false
	}
	if !member {
		writeError(w, http.StatusNotFound, "not_found", "channel was not found")
		return "", false
	}
	return channelID, true
}

func (s *server) unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "unauthorized", "connect this device to Ferry")
}

func (s *server) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]Device{"device": currentDevice(r)})
}

type accessJoinRequest struct {
	DeviceName strictString
	Password   strictString
	// Channel is optional; an absent or empty value means the default channel.
	Channel strictString
}

func (s *server) accessStatus(w http.ResponseWriter, r *http.Request) {
	verifier, err := s.store.AccessPassword(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"password_required": verifier != nil})
}

func (s *server) join(w http.ResponseWriter, r *http.Request) {
	if !hasMediaType(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	request, err := decodeAccessJoinRequest(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain only device_name and password")
		return
	}
	name, err := normalizeSenderName(string(request.DeviceName))
	if err != nil {
		s.domainError(w, err)
		return
	}
	deviceKind, err := requestDeviceKind(r, name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "X-Ferry-Device-Kind must be iphone, ipad, mac, android, windows, or browser")
		return
	}
	password := string(request.Password)
	if password != "" && !validAccessPassword(password) {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("password must be valid UTF-8 and at most %d bytes", maxAccessPasswordBytes))
		return
	}
	channelID := DefaultChannelID
	if string(request.Channel) != "" {
		channelID, err = normalizeChannelID(string(request.Channel))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	verifier, err := s.store.AccessPassword(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	if verifier != nil && !verifyAccessPassword(password, *verifier) {
		writeError(w, http.StatusUnauthorized, "invalid_password", "password is incorrect")
		return
	}
	token, tokenHash, err := newDeviceToken()
	if err != nil {
		s.internalError(w, err)
		return
	}
	device, err := s.store.CreateDeviceWithKindInChannel(r.Context(), name, deviceKind, tokenHash, channelID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "channel was not found")
		return
	}
	if err != nil {
		s.domainError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"device": device, "token": token})
}

func requestDeviceKind(r *http.Request, deviceName string) (DeviceKind, error) {
	values := r.Header.Values("X-Ferry-Device-Kind")
	if len(values) == 0 {
		return inferDeviceKind(deviceName), nil
	}
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] || strings.Contains(values[0], ",") {
		return "", ErrInvalid
	}
	return normalizeDeviceKind(values[0], deviceName)
}

func (s *server) accessSettings(w http.ResponseWriter, r *http.Request) {
	s.accessStatus(w, r)
}

type accessSettingsRequest struct {
	Password strictString
}

func (s *server) updateAccessSettings(w http.ResponseWriter, r *http.Request) {
	if !hasMediaType(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	request, err := decodeAccessSettingsRequest(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain only password")
		return
	}
	password := string(request.Password)
	var verifier *AccessPasswordVerifier
	if password != "" {
		derived, err := newAccessPasswordVerifier(password)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		verifier = &derived
	}
	s.accessMu.Lock()
	err = s.store.SetAccessPasswordForDevice(r.Context(), currentDevice(r).ID, verifier)
	s.accessMu.Unlock()
	if errors.Is(err, ErrUnauthorized) {
		s.unauthorized(w)
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"password_required": verifier != nil})
}

func (s *server) listDevices(w http.ResponseWriter, r *http.Request) {
	channelID, ok := s.authorizedChannel(w, r)
	if !ok {
		return
	}
	devices, err := s.store.ListDevices(r.Context(), channelID)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]Device{"devices": devices})
}

func (s *server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("device_id")
	channelID, ok := s.authorizedChannel(w, r)
	if !ok {
		return
	}
	err := s.store.DeleteDevice(r.Context(), currentDevice(r).ID, channelID, id)
	if errors.Is(err, ErrUnauthorized) {
		s.unauthorized(w)
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "device was not found")
		return
	}
	if errors.Is(err, ErrInvalid) {
		writeError(w, http.StatusConflict, "cannot_revoke_device", "the current or last device cannot be revoked")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listMessages(w http.ResponseWriter, r *http.Request) {
	channelID, ok := s.authorizedChannel(w, r)
	if !ok {
		return
	}
	after, err := parseNonNegativeInt64(r.URL.Query().Get("after"), 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "after must be a non-negative integer")
		return
	}
	limit, err := parseBoundedInt(r.URL.Query().Get("limit"), 100, 1, 200)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "limit must be an integer from 1 to 200")
		return
	}
	afterRetraction, err := parseNonNegativeInt64(r.URL.Query().Get("after_retraction"), 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "after_retraction must be a non-negative integer")
		return
	}
	messages, nextCursor, err := s.store.ListMessages(r.Context(), channelID, after, limit)
	if err != nil {
		s.internalError(w, err)
		return
	}
	retractedIDs, nextRetractionCursor, err := s.store.ListRetractions(r.Context(), channelID, afterRetraction, limit)
	if err != nil {
		s.internalError(w, err)
		return
	}
	deviceID := currentDevice(r).ID
	for index := range messages {
		messages[index].IsCurrentDevice = messages[index].SenderDeviceID != "" && messages[index].SenderDeviceID == deviceID
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"messages":               messages,
		"next_cursor":            nextCursor,
		"retracted_ids":          retractedIDs,
		"next_retraction_cursor": nextRetractionCursor,
	})
}

func (s *server) retractMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("message_id")
	channelID, err := s.store.ChannelOfMessage(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "message was not found")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	// A device outside the channel gets the same answer as one asking about a
	// message that never existed.
	member, err := s.store.IsChannelMember(r.Context(), currentDevice(r).ID, channelID)
	if err != nil {
		s.internalError(w, err)
		return
	}
	if !member {
		writeError(w, http.StatusNotFound, "not_found", "message was not found")
		return
	}
	err = s.store.RetractMessage(r.Context(), currentDevice(r).ID, channelID, id)
	if errors.Is(err, ErrUnauthorized) {
		s.unauthorized(w)
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "message was not found")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"retracted_id": id})
}

func (s *server) listChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.store.ListChannels(r.Context(), currentDevice(r).ID)
	if errors.Is(err, ErrUnauthorized) {
		s.unauthorized(w)
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string][]Channel{"channels": channels})
}

type createChannelRequest struct {
	Name strictString
}

func (s *server) createChannel(w http.ResponseWriter, r *http.Request) {
	if !hasMediaType(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	request, err := decodeCreateChannelRequest(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain only name")
		return
	}
	channelID, err := normalizeChannelID(string(request.Name))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	channel, err := s.store.CreateChannel(r.Context(), channelID)
	if errors.Is(err, ErrConflict) {
		writeError(w, http.StatusConflict, "channel_exists", "channel already exists")
		return
	}
	if err != nil {
		s.domainError(w, err)
		return
	}
	if err := s.store.JoinChannel(r.Context(), currentDevice(r).ID, channel.ID); err != nil {
		s.internalError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	channel.Joined = true
	writeJSON(w, http.StatusCreated, channel)
}

type joinChannelRequest struct {
	Channel strictString
}

func decodeJoinChannelRequest(source io.Reader) (joinChannelRequest, error) {
	var request joinChannelRequest
	err := decodeStrictObject(source, map[string]func(*json.Decoder) error{
		"channel": func(decoder *json.Decoder) error { return decoder.Decode(&request.Channel) },
	})
	return request, err
}

// joinChannel adds the current device to a channel that already exists, so a
// device that already holds a token can read a second timeline without
// registering again (and without a second token). An unknown channel is the
// same 404 a non-member gets elsewhere, so it never confirms what exists.
func (s *server) joinChannel(w http.ResponseWriter, r *http.Request) {
	if !hasMediaType(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	request, err := decodeJoinChannelRequest(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain only channel")
		return
	}
	channelID, err := normalizeChannelID(string(request.Channel))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	err = s.store.JoinChannel(r.Context(), currentDevice(r).ID, channelID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "channel was not found")
		return
	}
	if errors.Is(err, ErrUnauthorized) {
		s.unauthorized(w)
		return
	}
	if err != nil {
		s.domainError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) createText(w http.ResponseWriter, r *http.Request) {
	if !hasMediaType(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	channelID, ok := s.authorizedChannel(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	request, err := decodeTextRequest(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "request body is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain only text")
		return
	}
	message, err := s.store.CreateTextForDevice(r.Context(), currentDevice(r), channelID, string(request.Text))
	if errors.Is(err, ErrUnauthorized) {
		s.unauthorized(w)
		return
	}
	if err != nil {
		s.domainError(w, err)
		return
	}
	message.IsCurrentDevice = true
	writeJSON(w, http.StatusCreated, message)
}

func (s *server) createFile(w http.ResponseWriter, r *http.Request) {
	channelID, ok := s.authorizedChannel(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxFileBytes+multipartOverhead)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "file upload is too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "request must be multipart form data")
		return
	}
	defer r.MultipartForm.RemoveAll()
	if !validMultipartShape(r) {
		writeError(w, http.StatusBadRequest, "invalid_request", "multipart body must contain exactly one file")
		return
	}
	source, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "file is required")
		return
	}
	defer source.Close()
	mediaType := header.Header.Get("Content-Type")
	message, err := s.store.CreateFileForDevice(r.Context(), currentDevice(r), channelID, header.Filename, mediaType, source)
	if errors.Is(err, ErrUnauthorized) {
		s.unauthorized(w)
		return
	}
	if err != nil {
		s.domainError(w, err)
		return
	}
	message.IsCurrentDevice = true
	writeJSON(w, http.StatusCreated, message)
}

func validMultipartShape(r *http.Request) bool {
	form := r.MultipartForm
	if form == nil || len(form.Value) != 0 || len(form.File) != 1 {
		return false
	}
	return len(form.File["file"]) == 1
}

func (s *server) downloadFile(w http.ResponseWriter, r *http.Request) {
	channelID, ok := s.authorizedChannel(w, r)
	if !ok {
		return
	}
	s.serveFile(w, r, channelID, r.PathValue("message_id"))
}

func (s *server) serveFile(w http.ResponseWriter, r *http.Request, channelID, messageID string) {
	message, file, err := s.store.OpenFile(r.Context(), channelID, messageID)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "file message was not found")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", message.File.MediaType)
	w.Header().Set("Content-Length", strconv.FormatInt(message.File.Size, 10))
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": message.File.Name})
	w.Header().Set("Content-Disposition", disposition)
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, file); err != nil {
		s.logger.Printf("stream file %s: %v", message.ID, err)
	}
}

func (s *server) domainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large", err.Error())
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		s.internalError(w, err)
	}
}

func (s *server) internalError(w http.ResponseWriter, err error) {
	s.logger.Printf("request failed: %v", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "the server could not complete the request")
}

func parseBoundedInt(value string, fallback, minimum, maximum int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("integer is outside bounds")
	}
	return parsed, nil
}

func parseNonNegativeInt64(value string, fallback int64) (int64, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("integer is outside bounds")
	}
	return parsed, nil
}

func hasMediaType(value, expected string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == expected
}

type textRequest struct {
	Text strictString
}

func decodeTextRequest(source io.Reader) (textRequest, error) {
	var request textRequest
	err := decodeStrictObject(source, map[string]func(*json.Decoder) error{
		"text": func(decoder *json.Decoder) error { return decoder.Decode(&request.Text) },
	})
	return request, err
}

func decodeAccessJoinRequest(source io.Reader) (accessJoinRequest, error) {
	var request accessJoinRequest
	err := decodeStrictFields(source, map[string]func(*json.Decoder) error{
		"device_name": func(decoder *json.Decoder) error { return decoder.Decode(&request.DeviceName) },
		"password":    func(decoder *json.Decoder) error { return decoder.Decode(&request.Password) },
		"channel":     func(decoder *json.Decoder) error { return decoder.Decode(&request.Channel) },
	}, []string{"device_name", "password"})
	return request, err
}

func decodeCreateChannelRequest(source io.Reader) (createChannelRequest, error) {
	var request createChannelRequest
	err := decodeStrictObject(source, map[string]func(*json.Decoder) error{
		"name": func(decoder *json.Decoder) error { return decoder.Decode(&request.Name) },
	})
	return request, err
}

func decodeAccessSettingsRequest(source io.Reader) (accessSettingsRequest, error) {
	var request accessSettingsRequest
	err := decodeStrictObject(source, map[string]func(*json.Decoder) error{
		"password": func(decoder *json.Decoder) error { return decoder.Decode(&request.Password) },
	})
	return request, err
}

func decodeStrictObject(source io.Reader, fields map[string]func(*json.Decoder) error) error {
	required := make([]string, 0, len(fields))
	for key := range fields {
		required = append(required, key)
	}
	return decodeStrictFields(source, fields, required)
}

// decodeStrictFields is decodeStrictObject with a required subset, so a key
// like join's optional "channel" can be accepted without weakening the
// duplicate, unknown-field and exact-shape checks for the rest.
func decodeStrictFields(source io.Reader, fields map[string]func(*json.Decoder) error, required []string) error {
	decoder := json.NewDecoder(source)
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return fmt.Errorf("request must be a JSON object")
	}
	seen := make(map[string]bool, len(fields))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return fmt.Errorf("request contains a duplicate field")
		}
		seen[key] = true
		decode, exists := fields[key]
		if !exists {
			return fmt.Errorf("request contains unknown field %q", key)
		}
		if err := decode(decoder); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return fmt.Errorf("request is not a complete JSON object")
	}
	for _, key := range required {
		if !seen[key] {
			return fmt.Errorf("request does not contain the exact required fields")
		}
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

type strictString string

func (s *strictString) UnmarshalJSON(data []byte) error {
	if !validJSONStringEncoding(data) {
		return fmt.Errorf("JSON string contains invalid Unicode")
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*s = strictString(value)
	return nil
}

func validJSONStringEncoding(data []byte) bool {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return false
	}
	for index := 1; index < len(data)-1; {
		if data[index] != '\\' {
			_, size := utf8.DecodeRune(data[index : len(data)-1])
			if size == 1 && data[index] >= utf8.RuneSelf {
				return false
			}
			index += size
			continue
		}
		if index+1 >= len(data)-1 || data[index+1] != 'u' {
			index += 2
			continue
		}
		code, ok := parseHex4(data, index+2)
		if !ok || code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			low, paired := parseHex4(data, index+8)
			if index+7 >= len(data)-1 || data[index+6] != '\\' || data[index+7] != 'u' || !paired || low < 0xdc00 || low > 0xdfff {
				return false
			}
			index += 12
			continue
		}
		index += 6
	}
	return true
}

func parseHex4(data []byte, start int) (uint64, bool) {
	if start+4 > len(data)-1 {
		return 0, false
	}
	value, err := strconv.ParseUint(string(data[start:start+4]), 16, 16)
	return value, err == nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": strings.TrimSpace(message),
		},
	})
}
