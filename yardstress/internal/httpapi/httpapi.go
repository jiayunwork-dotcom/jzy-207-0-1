// Package httpapi exposes the admission engine over HTTP (Gin). It is a
// thin translation layer: all decisions happen in the admit package.
package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"yardstress/internal/admit"
	"yardstress/internal/events"
	"yardstress/internal/ferr"
)

// Server wires the engine to HTTP handlers.
type Server struct {
	eng *admit.Engine
}

// NewRouter builds the Gin router for the engine.
func NewRouter(eng *admit.Engine) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	s := &Server{eng: eng}

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	v1 := r.Group("/api/v1")
	v1.POST("/jobs", s.postJob)
	v1.POST("/corrections", s.postCorrection)
	v1.GET("/events", s.listEvents)
	v1.GET("/grid/current", s.gridCurrent)
	v1.GET("/grid/at/:seq", s.gridAt)
	v1.GET("/stacks", s.listStacks)
	return r
}

// ---------------------------------------------------------------------------
// jobs
// ---------------------------------------------------------------------------

func (s *Server) postJob(c *gin.Context) {
	var req admit.JobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"errors": ferr.List{{Field: "body", Message: err.Error()}}})
		return
	}
	out, err := s.eng.Submit(req)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	switch {
	case len(out.FieldErrors) > 0:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"errors": out.FieldErrors})
	case out.Rejected != nil:
		c.JSON(http.StatusConflict, out.Rejected)
	default:
		c.JSON(http.StatusCreated, gin.H{"seq": out.Seq})
	}
}

// ---------------------------------------------------------------------------
// corrections
// ---------------------------------------------------------------------------

type correctionRequest struct {
	EventID   int64   `json:"event_id"`
	NewWeight float64 `json:"new_weight"`
}

func (s *Server) postCorrection(c *gin.Context) {
	var req correctionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"errors": ferr.List{{Field: "body", Message: err.Error()}}})
		return
	}
	res, err := s.eng.Correct(req.EventID, req.NewWeight)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(res.FieldErrors) > 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"errors": res.FieldErrors})
		return
	}
	impacted := res.Impacted
	if impacted == nil {
		impacted = []admit.ImpactedEvent{}
	}
	current := res.CurrentExceedances
	if current == nil {
		current = []admit.Exceedance{}
	}
	c.JSON(http.StatusOK, gin.H{
		"seq":                 res.Seq,
		"impacted":            impacted,
		"current_exceedances": current,
	})
}

// ---------------------------------------------------------------------------
// queries
// ---------------------------------------------------------------------------

func (s *Server) listEvents(c *gin.Context) {
	from, err1 := strconv.ParseInt(c.DefaultQuery("from", "0"), 10, 64)
	to, err2 := strconv.ParseInt(c.DefaultQuery("to", "9223372036854775807"), 10, 64)
	if err1 != nil || err2 != nil || from < 0 || to < from {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid from/to"})
		return
	}
	evts, err := s.eng.Events(from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if evts == nil {
		evts = []events.Event{}
	}
	c.JSON(http.StatusOK, gin.H{"events": evts})
}

type gridResponse struct {
	Seq     int64      `json:"seq"`
	Depth   float64    `json:"depth"`
	Origin  [2]float64 `json:"origin"`
	Spacing [2]float64 `json:"spacing"`
	Count   [2]int     `json:"count"`
	Values  []float64  `json:"values"`
}

func (s *Server) gridJSON(st admit.State) gridResponse {
	g := s.eng.Grid()
	return gridResponse{
		Seq:     st.Seq,
		Depth:   g.Depth,
		Origin:  [2]float64{g.X0, g.Y0},
		Spacing: [2]float64{g.Dx, g.Dy},
		Count:   [2]int{g.Nx, g.Ny},
		Values:  st.Values,
	}
}

func (s *Server) gridCurrent(c *gin.Context) {
	c.JSON(http.StatusOK, s.gridJSON(s.eng.Current()))
}

func (s *Server) gridAt(c *gin.Context) {
	seq, err := strconv.ParseInt(c.Param("seq"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid seq"})
		return
	}
	st, err := s.eng.GridAt(seq)
	if err != nil {
		if errors.Is(err, admit.ErrSeqOutOfRange) {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, s.gridJSON(st))
}

func (s *Server) listStacks(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"stacks": s.eng.Stacks()})
}
