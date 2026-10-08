package api

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"net/http"
)

// Routes for the repeater we run, distinct from /api/companions/{name}/repeaters/* which drive remote ones.

// repeaterDTO is the secret-redacted read shape: privateKey and passwords become *Set booleans.
type repeaterDTO struct {
	Configured          bool                `json:"configured"`
	Running             bool                `json:"running"`
	Name                string              `json:"name"`
	PubKey              string              `json:"pubkey"`
	PrivateKeySet       bool                `json:"privateKeySet"`
	Latitude            *float64            `json:"latitude"`
	Longitude           *float64            `json:"longitude"`
	AdvertInterval      *int                `json:"advertInterval"`
	FloodAdvertInterval *int                `json:"floodAdvertInterval"`
	DisableFwd          *bool               `json:"disableFwd"`
	FloodMax            *int                `json:"floodMax"`
	FloodMaxUnscoped    *int                `json:"floodMaxUnscoped"`
	FloodMaxAdvert      *int                `json:"floodMaxAdvert"`
	LoopDetect          *string             `json:"loopDetect"`
	PathHashSize        *int                `json:"pathHashSize"`
	TxDelayFactor       *float64            `json:"txDelayFactor"`
	DirectTxDelayFactor *float64            `json:"directTxDelayFactor"`
	RxDelayBase         *float64            `json:"rxDelayBase"`
	MultiAcks           *int                `json:"multiAcks"`
	FloodScope          string              `json:"floodScope"`
	AdminPasswordSet    bool                `json:"adminPasswordSet"`
	GuestPasswordSet    bool                `json:"guestPasswordSet"`
	OwnerInfo           string              `json:"ownerInfo"`
	Regions             []repeaterRegionDTO `json:"regions"`
	HomeRegion          string              `json:"homeRegion"` // "*" when none is set, as the firmware reports it
}

type repeaterRegionDTO struct {
	Name      string `json:"name"`
	Parent    string `json:"parent"` // "*" for the top; "" only on "*" itself
	DenyFlood bool   `json:"denyFlood"`
}

func (s *Server) handleGetRepeater(w http.ResponseWriter, r *http.Request) {
	running := false
	if b := s.backendRef(); b != nil {
		_, running = b.RepeaterNode()
	}

	rep, err := s.store.Repeater.Get(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, repeaterDTO{Configured: false, Running: running})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read repeater")
		return
	}

	regions := make([]repeaterRegionDTO, 0, len(rep.Regions))
	for _, rg := range rep.Regions {
		regions = append(regions, repeaterRegionDTO{Name: rg.Name, Parent: rg.Parent, DenyFlood: rg.DenyFlood})
	}

	writeJSON(w, http.StatusOK, repeaterDTO{
		Configured:          true,
		Running:             running,
		Name:                rep.Name,
		PubKey:              rep.PubKey,
		PrivateKeySet:       rep.PrivateKey != "",
		Latitude:            rep.Latitude,
		Longitude:           rep.Longitude,
		AdvertInterval:      rep.AdvertInterval,
		FloodAdvertInterval: rep.FloodAdvertInterval,
		DisableFwd:          rep.DisableFwd,
		FloodMax:            rep.FloodMax,
		FloodMaxUnscoped:    rep.FloodMaxUnscoped,
		FloodMaxAdvert:      rep.FloodMaxAdvert,
		LoopDetect:          rep.LoopDetect,
		PathHashSize:        rep.PathHashSize,
		TxDelayFactor:       rep.TxDelayFactor,
		DirectTxDelayFactor: rep.DirectTxDelayFactor,
		RxDelayBase:         rep.RxDelayBase,
		MultiAcks:           rep.MultiAcks,
		FloodScope:          rep.FloodScope,
		AdminPasswordSet:    rep.AdminPassword != "",
		GuestPasswordSet:    rep.GuestPassword != "",
		OwnerInfo:           rep.OwnerInfo,
		Regions:             regions,
		HomeRegion:          cmp.Or(rep.HomeRegion, "*"),
	})
}

// repeaterSection writes 204 on success, 400 on bad JSON, 422 on validation or not-configured failure.
func repeaterSection[T any](s *Server, w http.ResponseWriter, r *http.Request, fn func(Backend, context.Context, T) error) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	var in T
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := fn(b, r.Context(), in); err != nil {
		writeConfigError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeConfigError answers a refused config change: its own status when it carries one, else 422.
func writeConfigError(w http.ResponseWriter, err error) {
	status := http.StatusUnprocessableEntity
	if serr := (*StatusError)(nil); errors.As(err, &serr) {
		status = serr.Status
	}
	writeError(w, status, err.Error())
}

func (s *Server) handleCreateRepeater(w http.ResponseWriter, r *http.Request) {
	repeaterSection(s, w, r, Backend.CreateRepeater)
}

func (s *Server) handleUpdateRepeaterNode(w http.ResponseWriter, r *http.Request) {
	repeaterSection(s, w, r, Backend.UpdateRepeaterNode)
}

func (s *Server) handleUpdateRepeaterRelay(w http.ResponseWriter, r *http.Request) {
	repeaterSection(s, w, r, Backend.UpdateRepeaterRelay)
}

func (s *Server) handleUpdateRepeaterAdmin(w http.ResponseWriter, r *http.Request) {
	repeaterSection(s, w, r, Backend.UpdateRepeaterAdmin)
}

func (s *Server) handleSetRepeaterScope(w http.ResponseWriter, r *http.Request) {
	repeaterSection(s, w, r, Backend.SetRepeaterFloodScope)
}

func (s *Server) handleSetRepeaterHome(w http.ResponseWriter, r *http.Request) {
	repeaterSection(s, w, r, Backend.SetRepeaterHome)
}

func (s *Server) handleAddRepeaterRegion(w http.ResponseWriter, r *http.Request) {
	repeaterSection(s, w, r, Backend.AddRepeaterRegion)
}

// handlePatchRepeaterRegion takes the region name percent-decoded from the path, so "*" arrives as %2A.
func (s *Server) handlePatchRepeaterRegion(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	var in struct {
		DenyFlood *bool   `json:"denyFlood"`
		Parent    *string `json:"parent"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	name := r.PathValue("name")
	var err error
	switch {
	case (in.DenyFlood == nil) == (in.Parent == nil):
		writeError(w, http.StatusBadRequest, "send one of denyFlood or parent")
		return
	case in.Parent != nil:
		err = b.MoveRepeaterRegion(r.Context(), name, *in.Parent)
	default:
		err = b.SetRepeaterRegionFlood(r.Context(), name, *in.DenyFlood)
	}
	if err != nil {
		writeConfigError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteRepeaterRegion(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	if err := b.RemoveRepeaterRegion(r.Context(), r.PathValue("name")); err != nil {
		writeConfigError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteRepeater(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	if err := b.DeleteRepeater(r.Context()); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// repeaterNodeOps writes 404 when no repeater is running, 503 when the backend isn't wired yet.
func (s *Server) repeaterNodeOps(w http.ResponseWriter) (*RepeaterNodeOps, bool) {
	b := s.backendRef()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "not available")
		return nil, false
	}
	ops, ok := b.RepeaterNode()
	if !ok {
		writeError(w, http.StatusNotFound, "no repeater running")
		return nil, false
	}
	return ops, true
}

func (s *Server) handleRepeaterNodeStatus(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterNodeOps(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, ops.Stats())
}

func (s *Server) handleRepeaterNodeNeighbors(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterNodeOps(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, ops.Neighbors())
}

func (s *Server) handleRepeaterNodeACL(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterNodeOps(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, ops.ACL())
}

// handleRepeaterNodeRevoke drops a client from the repeater's ACL.
func (s *Server) handleRepeaterNodeRevoke(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterNodeOps(w)
	if !ok {
		return
	}
	if err := ops.RevokeACL(r.PathValue("pubkey")); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRepeaterNodeAdvert(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterNodeOps(w)
	if !ok {
		return
	}
	// The body is optional; a malformed or empty one falls back to a flood advert.
	var body struct {
		Flood *bool `json:"flood"`
	}
	_ = readJSON(r, &body)
	flood := true
	if body.Flood != nil {
		flood = *body.Flood
	}
	if err := ops.Advert(flood); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRepeaterNodeDiscover(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterNodeOps(w)
	if !ok {
		return
	}
	if err := ops.Discover(); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRepeaterNodeSetACL(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterNodeOps(w)
	if !ok {
		return
	}
	var in struct {
		Permission int `json:"permission"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := ops.SetACL(r.PathValue("pubkey"), in.Permission); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRepeaterNodeClearStats(w http.ResponseWriter, r *http.Request) {
	ops, ok := s.repeaterNodeOps(w)
	if !ok {
		return
	}
	ops.ClearStats()
	w.WriteHeader(http.StatusNoContent)
}
