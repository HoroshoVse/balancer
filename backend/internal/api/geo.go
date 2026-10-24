package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/balancer/backend/internal/db"
	"github.com/balancer/backend/internal/engine"
	"github.com/balancer/backend/internal/geo"
)

// SetGeo provides the country database the /geo endpoints report on and refresh.
func (s *Server) SetGeo(m *geo.Manager) { s.geo = m }

func (s *Server) geoManager(w http.ResponseWriter) *geo.Manager {
	if s.geo == nil {
		http.Error(w, "The country database is not available", http.StatusServiceUnavailable)
		return nil
	}
	return s.geo
}

type geoResponse struct {
	geo.Status
	AutoUpdate    bool     `json:"auto_update"`
	IntervalHours int      `json:"interval_hours"`
	CountryCodes  []string `json:"country_codes"` // codes that have data, for the country pickers
}

func (s *Server) geoStatus(m *geo.Manager) geoResponse {
	auto, every := db.GeoSchedule(s.db)
	codes := m.Countries()
	if codes == nil {
		codes = []string{}
	}
	return geoResponse{Status: m.Status(), AutoUpdate: auto, IntervalHours: int(every / time.Hour), CountryCodes: codes}
}

// GET /api/v1/geo
func (s *Server) getGeo(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	if m := s.geoManager(w); m != nil {
		writeJSON(w, http.StatusOK, s.geoStatus(m))
	}
}

// POST /api/v1/geo/update starts a download in the background.
func (s *Server) updateGeo(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost) {
		return
	}
	m := s.geoManager(w)
	if m == nil {
		return
	}
	if m.Status().Updating {
		http.Error(w, "An update is already running", http.StatusConflict)
		return
	}
	go func() {
		if err := m.Update(context.Background()); err != nil && !errors.Is(err, geo.ErrBusy) {
			engineLog("Country database update failed: " + err.Error())
		}
	}()
	// Give the update a moment to mark itself as running so the answer shows it
	for i := 0; i < 20 && !m.Status().Updating; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	writeJSON(w, http.StatusAccepted, s.geoStatus(m))
}

func engineLog(msg string) { engine.Logger.Warn(msg) }

// POST /api/v1/geo/settings
func (s *Server) updateGeoSettings(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodPost, http.MethodPut) {
		return
	}
	m := s.geoManager(w)
	if m == nil {
		return
	}
	var in struct {
		AutoUpdate    bool `json:"auto_update"`
		IntervalHours int  `json:"interval_hours"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.IntervalHours < db.MinGeoUpdateHours || in.IntervalHours > db.MaxGeoUpdateHours {
		http.Error(w, "The interval must be between "+strconv.Itoa(db.MinGeoUpdateHours)+" hours and 30 days", http.StatusBadRequest)
		return
	}
	settings, err := db.LoadSettings(s.db)
	if err != nil {
		writeError(w, err)
		return
	}
	settings.GeoUpdateOff = !in.AutoUpdate
	settings.GeoUpdateHours = in.IntervalHours
	if err := s.db.Model(settings).Select("geo_update_off", "geo_update_hours").Updates(settings).Error; err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.geoStatus(m))
}

type lookupResponse struct {
	IP       string                 `json:"ip"`
	You      bool                   `json:"you,omitempty"` // no address was given: this is the caller's
	Country  string                 `json:"country"`       // "" when no registry lists the address
	Loaded   bool                   `json:"loaded"`        // false: the database is not complete, the country is unknown
	Internal bool                   `json:"internal,omitempty"`
	Decision *engine.AccessDecision `json:"decision,omitempty"` // what the balancer given in ?lb= would do
}

// GET /api/v1/geo/lookup?ip=1.2.3.4[&lb=ID]; without ip it describes the caller.
func (s *Server) lookupGeo(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	m := s.geoManager(w)
	if m == nil {
		return
	}
	res := lookupResponse{}
	raw := strings.TrimSpace(r.URL.Query().Get("ip"))
	if raw == "" {
		res.You = true
		raw = r.Header.Get("X-Real-IP")
		if raw == "" {
			raw, _, _ = net.SplitHostPort(r.RemoteAddr)
		}
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		http.Error(w, "Not an IP address: "+raw, http.StatusBadRequest)
		return
	}
	addr = addr.Unmap()
	res.IP = addr.String()
	res.Country, res.Loaded = m.Country(addr)
	res.Internal = addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
	if id := r.URL.Query().Get("lb"); id != "" {
		n, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			http.Error(w, "Bad balancer id", http.StatusBadRequest)
			return
		}
		d, ok := s.engine.CheckAccess(uint(n), addr)
		if !ok {
			http.Error(w, "Balancer not found or not running", http.StatusNotFound)
			return
		}
		res.Decision = &d
	}
	writeJSON(w, http.StatusOK, res)
}
