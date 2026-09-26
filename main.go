package main

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------- Domain ----------

type PreferenceLevel string

const (
	Want  PreferenceLevel = "WANT"
	Okay  PreferenceLevel = "OKAY"
	No    PreferenceLevel = "NO"
	Never PreferenceLevel = "NEVER"
)

// ---------- DB ----------

var db *pgxpool.Pool

func connectDB() {
	connString := os.Getenv("DATABASE_URL")
	if connString == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	pool, err := pgxpool.New(context.Background(), connString)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("database ping failed: %v", err)
	}

	db = pool
	log.Println("Connected to Postgres")
}

const codeChars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func generateCode() string {
	b := make([]byte, 6)
	for i := range b {
		b[i] = codeChars[rand.Intn(len(codeChars))]
	}
	return string(b)
}

func newID(prefix string) string {
	return prefix + "_" + strings.ToLower(generateCode())
}

func roomExists(ctx context.Context, code string) bool {
	var exists bool
	db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM rooms WHERE code = $1)", code).Scan(&exists)
	return exists
}

// ---------- JSON helpers ----------

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// ---------- DTOs (unchanged from before — Android depends on these shapes) ----------

type createRoomRequest struct {
	Question    string   `json:"question"`
	Category    string   `json:"category"`
	Options     []string `json:"options"`
	CreatorName string   `json:"creatorName"`
}

type roomOptionDTO struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type createRoomResponse struct {
	Code                 string          `json:"code"`
	Question             string          `json:"question"`
	Category             string          `json:"category"`
	Options              []roomOptionDTO `json:"options"`
	CreatorParticipantID string          `json:"creatorParticipantId"`
}

type joinRequest struct {
	Name string `json:"name"`
}

type joinResponse struct {
	ParticipantID string          `json:"participantId"`
	Code          string          `json:"code"`
	Question      string          `json:"question"`
	Category      string          `json:"category"`
	Options       []roomOptionDTO `json:"options"`
}

type voteRequest struct {
	ParticipantID string                     `json:"participantId"`
	Preferences   map[string]PreferenceLevel `json:"preferences"`
}

type participantStatusDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	HasVoted bool   `json:"hasVoted"`
}

type optionResultDTO struct {
	OptionID string  `json:"optionId"`
	Text     string  `json:"text"`
	Want     int     `json:"want"`
	Okay     int     `json:"okay"`
	No       int     `json:"no"`
	Never    int     `json:"never"`
	Score    float64 `json:"score"`
	Vetoed   bool    `json:"vetoed"`
}

type resultDTO struct {
	MatchType   string            `json:"matchType"`
	Options     []optionResultDTO `json:"options"`
	TopOptionID string            `json:"topOptionId,omitempty"`
}

type statusResponse struct {
	Code              string                 `json:"code"`
	Question          string                 `json:"question"`
	Category          string                 `json:"category"`
	Options           []roomOptionDTO        `json:"options"`
	Participants      []participantStatusDTO `json:"participants"`
	TotalParticipants int                    `json:"totalParticipants"`
	VotedCount        int                    `json:"votedCount"`
	AllVoted          bool                   `json:"allVoted"`
	Result            *resultDTO             `json:"result,omitempty"`
}

// ---------- Handlers ----------

func createRoomHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req createRoomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" {
		writeError(w, http.StatusBadRequest, "question is required")
		return
	}

	cleanOptions := make([]string, 0, len(req.Options))
	for _, o := range req.Options {
		o = strings.TrimSpace(o)
		if o != "" {
			cleanOptions = append(cleanOptions, o)
		}
	}
	if len(cleanOptions) < 2 {
		writeError(w, http.StatusBadRequest, "at least 2 options are required")
		return
	}

	creatorName := strings.TrimSpace(req.CreatorName)
	if creatorName == "" {
		creatorName = "Guest"
	}

	code := generateCode()
	for roomExists(ctx, code) {
		code = generateCode()
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		"INSERT INTO rooms (code, question, category) VALUES ($1, $2, $3)",
		code, req.Question, req.Category,
	); err != nil {
		log.Printf("insert room failed: %v", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	optionDTOs := make([]roomOptionDTO, 0, len(cleanOptions))
	for i, text := range cleanOptions {
		optionID := newID("opt")
		if _, err := tx.Exec(ctx,
			"INSERT INTO options (id, room_code, text, position) VALUES ($1, $2, $3, $4)",
			optionID, code, text, i,
		); err != nil {
			log.Printf("insert option failed: %v", err)
			writeError(w, http.StatusInternalServerError, "database error")
			return
		}
		optionDTOs = append(optionDTOs, roomOptionDTO{ID: optionID, Text: text})
	}

	creatorID := newID("p")
	if _, err := tx.Exec(ctx,
		"INSERT INTO participants (id, room_code, name, seq) VALUES ($1, $2, $3, 0)",
		creatorID, code, creatorName,
	); err != nil {
		log.Printf("insert participant failed: %v", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	writeJSON(w, http.StatusCreated, createRoomResponse{
		Code: code, Question: req.Question, Category: req.Category,
		Options: optionDTOs, CreatorParticipantID: creatorID,
	})
}

func joinRoomHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := strings.ToUpper(strings.TrimSpace(r.PathValue("code")))

	var room struct {
		Question string
		Category string
	}
	err := db.QueryRow(ctx, "SELECT question, category FROM rooms WHERE code = $1", code).
		Scan(&room.Question, &room.Category)
	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "room not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	var req joinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Guest"
	}

	var nextSeq int
	db.QueryRow(ctx, "SELECT COALESCE(MAX(seq), -1) + 1 FROM participants WHERE room_code = $1", code).Scan(&nextSeq)

	participantID := newID("p")
	if _, err := db.Exec(ctx,
		"INSERT INTO participants (id, room_code, name, seq) VALUES ($1, $2, $3, $4)",
		participantID, code, name, nextSeq,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	options := fetchOptions(ctx, code)

	writeJSON(w, http.StatusOK, joinResponse{
		ParticipantID: participantID, Code: code,
		Question: room.Question, Category: room.Category, Options: options,
	})
}

func fetchOptions(ctx context.Context, code string) []roomOptionDTO {
	rows, _ := db.Query(ctx, "SELECT id, text FROM options WHERE room_code = $1 ORDER BY position", code)
	defer rows.Close()

	options := make([]roomOptionDTO, 0)
	for rows.Next() {
		var o roomOptionDTO
		rows.Scan(&o.ID, &o.Text)
		options = append(options, o)
	}
	return options
}

func voteHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := strings.ToUpper(strings.TrimSpace(r.PathValue("code")))

	if !roomExists(ctx, code) {
		writeError(w, http.StatusNotFound, "room not found")
		return
	}

	var req voteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var participantExists bool
	db.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM participants WHERE id = $1 AND room_code = $2)",
		req.ParticipantID, code,
	).Scan(&participantExists)
	if !participantExists {
		writeError(w, http.StatusNotFound, "participant not found in this room")
		return
	}

	for optionID, level := range req.Preferences {
		switch level {
		case Want, Okay, No, Never:
		default:
			continue
		}

		var optionValid bool
		db.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM options WHERE id = $1 AND room_code = $2)",
			optionID, code,
		).Scan(&optionValid)
		if !optionValid {
			continue
		}

		db.Exec(ctx, `
			INSERT INTO preferences (participant_id, option_id, level, updated_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT (participant_id, option_id)
			DO UPDATE SET level = $3, updated_at = now()
		`, req.ParticipantID, optionID, string(level))
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := strings.ToUpper(strings.TrimSpace(r.PathValue("code")))

	var room struct {
		Question string
		Category string
	}
	err := db.QueryRow(ctx, "SELECT question, category FROM rooms WHERE code = $1", code).
		Scan(&room.Question, &room.Category)
	if err == pgx.ErrNoRows {
		writeError(w, http.StatusNotFound, "room not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}

	options := fetchOptions(ctx, code)

	rows, _ := db.Query(ctx, `
		SELECT p.id, p.name,
		       EXISTS(SELECT 1 FROM preferences pr WHERE pr.participant_id = p.id) AS has_voted
		FROM participants p
		WHERE p.room_code = $1
		ORDER BY p.seq
	`, code)
	defer rows.Close()

	participants := make([]participantStatusDTO, 0)
	votedCount := 0
	for rows.Next() {
		var p participantStatusDTO
		rows.Scan(&p.ID, &p.Name, &p.HasVoted)
		participants = append(participants, p)
		if p.HasVoted {
			votedCount++
		}
	}

	total := len(participants)
	allVoted := total > 0 && votedCount == total

	resp := statusResponse{
		Code: code, Question: room.Question, Category: room.Category,
		Options: options, Participants: participants,
		TotalParticipants: total, VotedCount: votedCount, AllVoted: allVoted,
	}

	if allVoted {
		resp.Result = computeResult(ctx, code, options, total)
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------- Consensus engine ----------
//
// Same heuristic as before: score = want*3 + okay*1 - no*2 - never*100.
// A single NEVER vetoes an option. Now reads from Postgres instead of memory.

func computeResult(ctx context.Context, code string, options []roomOptionDTO, total int) *resultDTO {
	results := make([]optionResultDTO, 0, len(options))

	for _, opt := range options {
		var want, okay, no, never int
		rows, _ := db.Query(ctx, "SELECT level FROM preferences WHERE option_id = $1", opt.ID)
		for rows.Next() {
			var level string
			rows.Scan(&level)
			switch PreferenceLevel(level) {
			case Want:
				want++
			case Okay:
				okay++
			case No:
				no++
			case Never:
				never++
			}
		}
		rows.Close()

		score := float64(want)*3 + float64(okay)*1 - float64(no)*2 - float64(never)*100
		results = append(results, optionResultDTO{
			OptionID: opt.ID, Text: opt.Text,
			Want: want, Okay: okay, No: no, Never: never,
			Score: score, Vetoed: never > 0,
		})
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })

	matchType := "NO_CONSENSUS"
	topOptionID := ""

	for _, res := range results {
		if res.Vetoed {
			continue
		}
		accepted := res.Want + res.Okay
		if accepted == total && res.Want == total {
			matchType, topOptionID = "FULL_CONSENSUS", res.OptionID
			break
		}
		if accepted == total && res.Want*2 >= total {
			matchType, topOptionID = "STRONG_CONSENSUS", res.OptionID
			break
		}
		if accepted == total {
			matchType, topOptionID = "ACCEPTABLE_CONSENSUS", res.OptionID
			break
		}
	}

	if topOptionID == "" {
		supported := 0
		for _, res := range results {
			if !res.Vetoed && res.Score > 0 {
				supported++
			}
		}
		switch {
		case supported >= 2:
			matchType, topOptionID = "PARTIAL_CONSENSUS", results[0].OptionID
		case supported == 1:
			matchType, topOptionID = "CONFLICT", results[0].OptionID
		default:
			matchType = "NO_CONSENSUS"
		}
	}

	return &resultDTO{MatchType: matchType, Options: results, TopOptionID: topOptionID}
}

// ---------- Main ----------

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	connectDB()
	defer db.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /rooms", createRoomHandler)
	mux.HandleFunc("POST /rooms/{code}/join", joinRoomHandler)
	mux.HandleFunc("POST /rooms/{code}/vote", voteHandler)
	mux.HandleFunc("GET /rooms/{code}/status", statusHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Smibz backend listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, corsMiddleware(mux)); err != nil {
		log.Fatal(err)
	}
}
