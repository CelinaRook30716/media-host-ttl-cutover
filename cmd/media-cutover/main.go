package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"example.com/media-host-cutover/cutover"
)

type server struct {
	pipeline *cutover.Pipeline
}

func main() {
	apiKey := os.Getenv("INFRAI_API_KEY")
	if apiKey == "" {
		log.Fatal("INFRAI_API_KEY is required")
	}
	dns := cutover.NewDNSClient(apiKey, nil)
	s := &server{pipeline: cutover.NewPipeline(dns)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /assets", s.ingest)
	mux.HandleFunc("POST /assets/{id}/ready", s.ready)
	mux.HandleFunc("POST /delivery/cutover", s.cutOver)
	mux.HandleFunc("POST /delivery/rollback", s.rollBack)
	log.Println("media cutover service listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func (s *server) ingest(w http.ResponseWriter, r *http.Request) {
	var asset cutover.Asset
	if !decode(w, r, &asset) {
		return
	}
	if err := s.pipeline.Ingest(asset); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, asset)
}

func (s *server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.pipeline.MarkReady(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"state": "processing_ready"})
}

func (s *server) cutOver(w http.ResponseWriter, r *http.Request) {
	var req cutover.CutoverRequest
	if !decode(w, r, &req) {
		return
	}
	result, err := s.pipeline.CutOver(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) rollBack(w http.ResponseWriter, r *http.Request) {
	var req cutover.CutoverRequest
	if !decode(w, r, &req) {
		return
	}
	result, err := s.pipeline.RollBack(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	status, message := cutover.MapError(err)
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil && !strings.Contains(err.Error(), "closed") {
		log.Printf("encode response: %v", err)
	}
}
