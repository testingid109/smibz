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
	"time"

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

	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		log.Fatalf("invalid DATABASE_URL: %v", err)
	}

	// Tuned for a small free-tier Postgres behind a service that can sleep
	// and cold-start (Render free tier). Recycling connections proactively
	// avoids handing out ones that went stale while the service was asleep.
	config.MaxConns = 5
	config.MinConns = 1
	config.MaxConnLifetime = 30 * time.Minute
	config.MaxConnIdleTime = 3 * time.Minute
	config.HealthCheckPeriod = 1 * time.Minute

	var pool *pgxpool.Pool
	for attempt := 1; attempt <= 5; attempt++ {
		pool, err = pgxpool.NewWithConfig(context.Background(), config)
		if err == nil {
			if pingErr := pool.Ping(context.Background()); pingErr == nil {
				break
			} else {
				err = pingErr
			}
		}
		log.Printf("database connection attempt %d failed: %v", attempt, err)
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	if err != nil {
		log.Fatalf("could not connect to database after retries: %v", err)
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

// queryExistsWithRetry runs an EXISTS query with a couple of quick retries
// before giving up. This is what fixes the "sometimes 404" bug: previously,
// a single failed query was silently treated as "doesn't exist" instead of
// "couldn't check." Now a real DB error is reported as a 500, and a
// transient blip (e.g. right after Render/Supabase wake from idle) gets a
// couple of quick extra tries before we give up.
func queryExistsWithRetry(ctx context.Context, query string, args ...interface{}) (bool, error) {
	var exists bool
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		err = db.QueryRow(ctx, query, args...).Scan(&exists)
		if err == nil {
			return exists, nil
		}
		time.Sleep(time.Duration(attempt*150) * time.Millisecond)
	}
	return false, err
}

func roomExists(ctx context.Context, code string) (bool, error) {
	return queryExistsWithRetry(ctx, "SELECT EXISTS(SELECT 1 FROM rooms WHERE code = $1)", code)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// ---------- DTOs ----------

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

func toOptionDTOs(options []roomOptionDTO) []roomOptionDTO {
	return options
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
	for {
		exists, err := roomExists(ctx, code)
		if err != nil {
			log.Printf("room existence check failed: %v", err)
			writeError(w, http.StatusInternalServerError, "database error, please try again")
			return
		}
		if !exists {
			break
		}
		code = generateCode()
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "database error, please try again")
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		"INSERT INTO rooms (code, question, category) VALUES ($1, $2, $3)",
		code, req.Question, req.Category,
	); err != nil {
		log.Printf("insert room failed: %v", err)
		writeError(w, http.StatusInternalServerError, "database error, please try again")
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
			writeError(w, http.StatusInternalServerError, "database error, please try again")
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
		writeError(w, http.StatusInternalServerError, "database error, please try again")
		return
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "database error, please try again")
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
		log.Printf("join lookup failed: %v", err)
		writeError(w, http.StatusInternalServerError, "database error, please try again")
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
		log.Printf("insert participant failed: %v", err)
		writeError(w, http.StatusInternalServerError, "database error, please try again")
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

	exists, err := roomExists(ctx, code)
	if err != nil {
		log.Printf("room existence check failed: %v", err)
		writeError(w, http.StatusInternalServerError, "database error, please try again")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "room not found")
		return
	}

	var req voteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	participantExists, err := queryExistsWithRetry(ctx,
		"SELECT EXISTS(SELECT 1 FROM participants WHERE id = $1 AND room_code = $2)",
		req.ParticipantID, code,
	)
	if err != nil {
		log.Printf("participant existence check failed: %v", err)
		writeError(w, http.StatusInternalServerError, "database error, please try again")
		return
	}
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

		optionValid, err := queryExistsWithRetry(ctx,
			"SELECT EXISTS(SELECT 1 FROM options WHERE id = $1 AND room_code = $2)",
			optionID, code,
		)
		if err != nil {
			log.Printf("option validity check failed after retries: %v", err)
			continue // skip this one option rather than failing the whole vote
		}
		if !optionValid {
			continue
		}

		if _, err := db.Exec(ctx, `
			INSERT INTO preferences (participant_id, option_id, level, updated_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT (participant_id, option_id)
			DO UPDATE SET level = $3, updated_at = now()
		`, req.ParticipantID, optionID, string(level)); err != nil {
			log.Printf("insert preference failed: %v", err)
		}
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
		log.Printf("status lookup failed: %v", err)
		writeError(w, http.StatusInternalServerError, "database error, please try again")
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
		resp.Result = computeResult(ctx, options, total)
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------- Consensus engine ----------

func computeResult(ctx context.Context, options []roomOptionDTO, total int) *resultDTO {
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
