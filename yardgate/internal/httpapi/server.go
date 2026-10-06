// Package httpapi exposes the admission engine over HTTP (Gin).
package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"yardgate/internal/admission"
	"yardgate/internal/grid"
)

// Server wires the engine to HTTP handlers.
type Server struct {
	eng *admission.Engine
}

// NewRouter builds the Gin engine with all routes.
func NewRouter(eng *admission.Engine) *gin.Engine {
	s := &Server{eng: eng}
	r := gin.New()
	r.Use(gin.Recovery())
	r.GET("/api/health", s.health)
	r.POST("/api/jobs", s.submitJob)
	r.POST("/api/corrections", s.correct)
	r.GET("/api/grid/current", s.gridCurrent)
	r.GET("/api/grid/at/:seq", s.gridAt)
	r.GET("/api/events", s.events)
	return r
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok", "seq": s.eng.Seq()})
}

// jobRequest is the wire format of POST /api/jobs.
type jobRequest struct {
	Type     string  `json:"type"`
	Block    string  `json:"block"`
	Bay      int     `json:"bay"`
	Row      int     `json:"row"`
	ToBlock  string  `json:"to_block"`
	ToBay    int     `json:"to_bay"`
	ToRow    int     `json:"to_row"`
	WeightKN float64 `json:"weight_kn"`
	Tiers    int     `json:"tiers"`
}

func (r *jobRequest) toJob() admission.Job {
	j := admission.Job{
		Type:     admission.JobType(r.Type),
		From:     admission.SlotRef{Block: r.Block, Bay: r.Bay, Row: r.Row},
		WeightKN: r.WeightKN,
		Tiers:    r.Tiers,
	}
	if j.Type == admission.Reshuffle {
		j.To = &admission.SlotRef{Block: r.ToBlock, Bay: r.ToBay, Row: r.ToRow}
	}
	return j
}

type violationJSON struct {
	X            float64 `json:"x"`
	Y            float64 `json:"y"`
	StressKPa    float64 `json:"stress_kpa"`
	AllowanceKPa float64 `json:"allowance_kpa"`
	ExcessKPa    float64 `json:"excess_kpa"`
}

func violationsJSON(vs []grid.Violation) []violationJSON {
	out := make([]violationJSON, 0, len(vs))
	for _, v := range vs {
		out = append(out, violationJSON{
			X: v.X, Y: v.Y,
			StressKPa:    v.StressKPa,
			AllowanceKPa: v.AllowanceKPa,
			ExcessKPa:    v.ExcessKPa,
		})
	}
	return out
}

// validationError writes a 400 for field validation failures.
func validationError(c *gin.Context, err error) {
	var ve *admission.ValidationError
	if errors.As(err, &ve) {
		c.JSON(http.StatusBadRequest, gin.H{"error": ve.Message, "field": ve.Field})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

func (s *Server) submitJob(c *gin.Context) {
	var req jobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body: " + err.Error()})
		return
	}
	res, err := s.eng.Submit(req.toJob())
	if err != nil {
		validationError(c, err)
		return
	}
	if !res.Accepted {
		c.JSON(http.StatusConflict, gin.H{
			"accepted":   false,
			"violations": violationsJSON(res.Violations),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"accepted": true, "seq": res.Seq})
}

// correctionRequest is the wire format of POST /api/corrections.
type correctionRequest struct {
	TargetSeq   int64   `json:"target_seq"`
	NewWeightKN float64 `json:"new_weight_kn"`
}

func (s *Server) correct(c *gin.Context) {
	var req correctionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body: " + err.Error()})
		return
	}
	res, err := s.eng.Correct(req.TargetSeq, req.NewWeightKN)
	if err != nil {
		validationError(c, err)
		return
	}
	if !res.Accepted {
		c.JSON(http.StatusConflict, gin.H{
			"accepted":   false,
			"violations": violationsJSON(res.Violations),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"accepted":      true,
		"seq":           res.Seq,
		"affected_seqs": res.AffectedSeqs,
	})
}

type gridJSON struct {
	Seq     int64     `json:"seq"`
	ZM      float64   `json:"z_m"`
	OriginX float64   `json:"origin_x"`
	OriginY float64   `json:"origin_y"`
	Dx      float64   `json:"dx"`
	Dy      float64   `json:"dy"`
	Nx      int       `json:"nx"`
	Ny      int       `json:"ny"`
	Values  []float64 `json:"values"`
}

func (s *Server) gridJSON(values []float64, seq int64) gridJSON {
	g := s.eng.GridMeta()
	return gridJSON{
		Seq: seq, ZM: g.Z,
		OriginX: g.OriginX, OriginY: g.OriginY,
		Dx: g.Dx, Dy: g.Dy, Nx: g.Nx, Ny: g.Ny,
		Values: values,
	}
}

func (s *Server) gridCurrent(c *gin.Context) {
	values, seq := s.eng.Current()
	c.JSON(http.StatusOK, s.gridJSON(values, seq))
}

func (s *Server) gridAt(c *gin.Context) {
	seq, err := strconv.ParseInt(c.Param("seq"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "seq must be an integer"})
		return
	}
	g, seq, err := s.eng.GridAt(seq)
	if err != nil {
		validationError(c, err)
		return
	}
	c.JSON(http.StatusOK, s.gridJSON(g.Values, seq))
}

func (s *Server) events(c *gin.Context) {
	from, _ := strconv.ParseInt(c.DefaultQuery("from", "0"), 10, 64)
	to, _ := strconv.ParseInt(c.DefaultQuery("to", "0"), 10, 64)
	evs := s.eng.Events(from, to)
	if evs == nil {
		evs = []admission.Event{}
	}
	c.JSON(http.StatusOK, gin.H{"events": evs, "count": len(evs)})
}
