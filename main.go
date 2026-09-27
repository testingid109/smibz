package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// ---------- DTOs (rooms) ----------

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

// ---------- Room handlers ----------

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
			continue
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

// ==================== EXPLORE / NEARBY PLACES — PIPELINE 1 + 2 ====================
//
// Pipeline 2 (ingestion) now genuinely exists: a scheduled/admin-triggered
// sweep pulls places from OpenStreetMap into a real PostGIS-backed table,
// deduplicated by (source, external_id) via upsert. Pipeline 1 (serving)
// now reads from that table first — fast, indexed, local — and only
// falls back to a live OSM query for areas nobody has ingested yet,
// lazily seeding the database with whatever it finds so that area is
// covered from then on.
//
// Still deliberately NOT built: a separate search service (Postgres
// full-text search covers this for free), a dedicated Redis (the
// in-process cache still plays that role), and cross-source entity
// resolution (only one source — OSM — so there's nothing to reconcile
// yet; this becomes real work only once a second data source is added).

type explorePlaceDTO struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Category       string   `json:"category"`
	Subcategory    string   `json:"subcategory"`
	Latitude       float64  `json:"latitude"`
	Longitude      float64  `json:"longitude"`
	Address        string   `json:"address"`
	DistanceMeters float64  `json:"distanceMeters"`
	Rating         *float64 `json:"rating,omitempty"`
	ReviewCount    int      `json:"reviewCount"`
	PriceLevel     *int     `json:"priceLevel,omitempty"`
	IsOpenNow      *bool    `json:"isOpenNow,omitempty"`
	ImageURL       *string  `json:"imageUrl,omitempty"`
	WebsiteURL     *string  `json:"websiteUrl,omitempty"`
	Score          float64  `json:"score"`
	Reason         *string  `json:"reason,omitempty"`
}

type nearbyPlacesResponse struct {
	Latitude     float64           `json:"latitude"`
	Longitude    float64           `json:"longitude"`
	RadiusMeters int               `json:"radiusMeters"`
	Category     string            `json:"category"`
	Cached       bool              `json:"cached"`
	Places       []explorePlaceDTO `json:"places"`
}

type osmTagFilter struct {
	Key   string
	Value string
}

type categoryTagSet struct {
	Category string
	Tags     []osmTagFilter
}

// An ordered list, not a map: category assignment during ingestion
// checks these in order, so overlapping tags resolve deterministically
// (e.g. "cafe" always lands in "eat", never randomly in "chill").
var exploreCategoryDefs = []categoryTagSet{
	{"eat", []osmTagFilter{
		{"amenity", "restaurant"}, {"amenity", "cafe"},
		{"amenity", "fast_food"}, {"amenity", "bar"}, {"amenity", "pub"},
	}},
	{"watch", []osmTagFilter{
		{"amenity", "cinema"}, {"amenity", "theatre"},
	}},
	{"play", []osmTagFilter{
		{"leisure", "sports_centre"}, {"leisure", "fitness_centre"},
		{"leisure", "bowling_alley"}, {"leisure", "pitch"},
	}},
	{"travel", []osmTagFilter{
		{"tourism", "hotel"}, {"tourism", "museum"},
	}},
	{"buy", []osmTagFilter{
		{"shop", "mall"}, {"shop", "supermarket"}, {"shop", "clothes"},
	}},
	{"events", []osmTagFilter{
		{"amenity", "events_venue"}, {"amenity", "arts_centre"},
	}},
	{"outdoors", []osmTagFilter{
		{"leisure", "garden"}, {"natural", "beach"},
	}},
	{"do", []osmTagFilter{
		{"tourism", "attraction"}, {"leisure", "amusement_arcade"},
	}},
	{"chill", []osmTagFilter{
		{"leisure", "park"},
	}},
}

func tagsForCategory(category string) []osmTagFilter {
	for _, def := range exploreCategoryDefs {
		if def.Category == category {
			return def.Tags
		}
	}
	return nil
}

func allCategoryTags() []osmTagFilter {
	seen := make(map[osmTagFilter]bool)
	all := []osmTagFilter{}
	for _, def := range exploreCategoryDefs {
		for _, t := range def.Tags {
			if !seen[t] {
				seen[t] = true
				all = append(all, t)
			}
		}
	}
	return all
}

func categorizeOSMTags(tags map[string]string) string {
	for _, def := range exploreCategoryDefs {
		for _, f := range def.Tags {
			if tags[f.Key] == f.Value {
				return def.Category
			}
		}
	}
	return "do"
}

type overpassElement struct {
	Type string            `json:"type"`
	ID   int64             `json:"id"`
	Lat  float64           `json:"lat"`
	Lon  float64           `json:"lon"`
	Tags map[string]string `json:"tags"`
}

type overpassResponse struct {
	Elements []overpassElement `json:"elements"`
}

type exploreCacheEntry struct {
	response  nearbyPlacesResponse
	expiresAt time.Time
}

var exploreCache = struct {
	sync.Mutex
	entries map[string]exploreCacheEntry
}{entries: make(map[string]exploreCacheEntry)}

const exploreCacheTTL = 10 * time.Minute

func exploreCacheKey(lat, lng float64, radius int, category, searchQuery string) string {
	roundedLat := math.Round(lat*100) / 100
	roundedLng := math.Round(lng*100) / 100
	return fmt.Sprintf("%.2f:%.2f:%d:%s:%s", roundedLat, roundedLng, radius, category, searchQuery)
}

func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadius = 6371000.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadius * c
}

func buildOverpassQuery(lat, lng float64, radius int, tags []osmTagFilter) string {
	var sb strings.Builder
	sb.WriteString("[out:json][timeout:25];\n(\n")
	for _, t := range tags {
		fmt.Fprintf(&sb, "  node[\"%s\"=\"%s\"](around:%d,%f,%f);\n", t.Key, t.Value, radius, lat, lng)
	}
	sb.WriteString(");\nout body;\n")
	return sb.String()
}

func buildAddress(tags map[string]string) string {
	parts := []string{}
	if v := tags["addr:housenumber"]; v != "" {
		parts = append(parts, v)
	}
	if v := tags["addr:street"]; v != "" {
		parts = append(parts, v)
	}
	if v := tags["addr:city"]; v != "" {
		parts = append(parts, v)
	}
	return strings.Join(parts, ", ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func formatDistance(meters float64) string {
	if meters < 1000 {
		return fmt.Sprintf("%.0f m away", meters)
	}
	return fmt.Sprintf("%.1f km away", meters/1000)
}

func fetchOverpassElements(query string) ([]overpassElement, error) {
	client := &http.Client{Timeout: 25 * time.Second}

	overpassURL := os.Getenv("OVERPASS_API_URL")
	if overpassURL == "" {
		overpassURL = "https://overpass.private.coffee/api/interpreter"
	}

	resp, err := client.PostForm(
		overpassURL,
		url.Values{"data": {query}},
	)

	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("overpass returned status %d", resp.StatusCode)
	}

	var parsed overpassResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	return parsed.Elements, nil
}

func elementsToPlaceDTOs(elements []overpassElement, originLat, originLng float64, category string) []explorePlaceDTO {
	places := make([]explorePlaceDTO, 0, len(elements))
	for _, el := range elements {
		name := el.Tags["name"]
		if name == "" {
			continue
		}

		distance := haversineMeters(originLat, originLng, el.Lat, el.Lon)
		address := buildAddress(el.Tags)

		var website *string
		if w := el.Tags["website"]; w != "" {
			website = &w
		} else if w := el.Tags["contact:website"]; w != "" {
			website = &w
		}

		score := 1000.0 - distance
		if score < 0 {
			score = 0
		}
		reason := formatDistance(distance)

		resolvedCategory := category
		if resolvedCategory == "" || resolvedCategory == "all" {
			resolvedCategory = categorizeOSMTags(el.Tags)
		}

		places = append(places, explorePlaceDTO{
			ID:             fmt.Sprintf("osm_%d", el.ID),
			Name:           name,
			Category:       resolvedCategory,
			Subcategory:    firstNonEmpty(el.Tags["amenity"], el.Tags["shop"], el.Tags["leisure"], el.Tags["tourism"]),
			Latitude:       el.Lat,
			Longitude:      el.Lon,
			Address:        address,
			DistanceMeters: distance,
			ReviewCount:    0,
			WebsiteURL:     website,
			Score:          score,
			Reason:         &reason,
		})
	}
	return places
}

// upsertOSMElement is the shared write path for both the scheduled
// ingestion sweep and the lazy fallback below. Dedup key is
// (source, external_id) - re-ingesting the same OSM node updates it
// in place rather than creating a duplicate row.
func upsertOSMElement(ctx context.Context, el overpassElement) (bool, error) {
	name := el.Tags["name"]
	if name == "" {
		return false, nil
	}

	category := categorizeOSMTags(el.Tags)
	address := buildAddress(el.Tags)
	subcategory := firstNonEmpty(el.Tags["amenity"], el.Tags["shop"], el.Tags["leisure"], el.Tags["tourism"])

	var website *string
	if w := el.Tags["website"]; w != "" {
		website = &w
	} else if w := el.Tags["contact:website"]; w != "" {
		website = &w
	}

	rawTagsJSON, _ := json.Marshal(el.Tags)
	id := fmt.Sprintf("osm_%d", el.ID)
	externalID := fmt.Sprintf("%d", el.ID)

	_, err := db.Exec(ctx, `
		INSERT INTO places (id, source, external_id, name, category, subcategory, address, website_url, location, latitude, longitude, raw_tags, updated_at)
		VALUES ($1, 'osm', $2, $3, $4, $5, $6, $7, ST_MakePoint($9,$8)::geography, $8, $9, $10, now())
		ON CONFLICT (source, external_id) DO UPDATE SET
			name = EXCLUDED.name,
			category = EXCLUDED.category,
			subcategory = EXCLUDED.subcategory,
			address = EXCLUDED.address,
			website_url = EXCLUDED.website_url,
			location = EXCLUDED.location,
			latitude = EXCLUDED.latitude,
			longitude = EXCLUDED.longitude,
			raw_tags = EXCLUDED.raw_tags,
			updated_at = now()
	`, id, externalID, name, category, subcategory, address, website, el.Lat, el.Lon, string(rawTagsJSON))

	if err != nil {
		return false, err
	}
	return true, nil
}

func queryNearbyPlacesFromDB(ctx context.Context, lat, lng float64, radius int, category, searchQuery string, limit int) ([]explorePlaceDTO, error) {
	rows, err := db.Query(ctx, `
		SELECT id, name, category, subcategory, address, website_url, latitude, longitude,
		       ST_Distance(location, ST_MakePoint($2,$1)::geography) AS distance_meters
		FROM places
		WHERE ST_DWithin(location, ST_MakePoint($2,$1)::geography, $3)
		  AND ($4 = 'all' OR category = $4)
		  AND ($5 = '' OR search_vector @@ plainto_tsquery('english', $5))
		ORDER BY distance_meters ASC
		LIMIT $6
	`, lat, lng, radius, category, searchQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	places := make([]explorePlaceDTO, 0)
	for rows.Next() {
		var (
			id, name, cat, subcat, address string
			website                        *string
			pLat, pLng, distance           float64
		)
		if err := rows.Scan(&id, &name, &cat, &subcat, &address, &website, &pLat, &pLng, &distance); err != nil {
			continue
		}

		score := 1000.0 - distance
		if score < 0 {
			score = 0
		}
		reason := formatDistance(distance)

		places = append(places, explorePlaceDTO{
			ID: id, Name: name, Category: cat, Subcategory: subcat,
			Latitude: pLat, Longitude: pLng, Address: address,
			DistanceMeters: distance, ReviewCount: 0, WebsiteURL: website,
			Score: score, Reason: &reason,
		})
	}
	return places, nil
}

func filterAndLimitPlaces(places []explorePlaceDTO, query string, limit int) []explorePlaceDTO {
	filtered := places
	if query != "" {
		filtered = make([]explorePlaceDTO, 0, len(places))
		for _, p := range places {
			if strings.Contains(strings.ToLower(p.Name), query) {
				filtered = append(filtered, p)
			}
		}
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
}

func nearbyPlacesHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()

	lat, errLat := strconv.ParseFloat(query.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(query.Get("lng"), 64)
	if errLat != nil || errLng != nil {
		writeError(w, http.StatusBadRequest, "lat and lng are required and must be numbers")
		return
	}

	radius := 5000
	if raw := query.Get("radius"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 20000 {
			radius = parsed
		}
	}

	category := strings.ToLower(strings.TrimSpace(query.Get("category")))
	if category == "" {
		category = "all"
	}

	limit := 40
	if raw := query.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	searchQuery := strings.ToLower(strings.TrimSpace(query.Get("q")))
	cacheKey := exploreCacheKey(lat, lng, radius, category, searchQuery)

	exploreCache.Lock()
	if entry, ok := exploreCache.entries[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		exploreCache.Unlock()
		resp := entry.response
		resp.Cached = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	exploreCache.Unlock()

	dbPlaces, dbErr := queryNearbyPlacesFromDB(ctx, lat, lng, radius, category, searchQuery, limit)
	if dbErr != nil {
		log.Printf("db nearby query failed: %v", dbErr)
	}

	if len(dbPlaces) > 0 {
		response := nearbyPlacesResponse{
			Latitude: lat, Longitude: lng, RadiusMeters: radius,
			Category: category, Cached: false, Places: dbPlaces,
		}
		exploreCache.Lock()
		exploreCache.entries[cacheKey] = exploreCacheEntry{response: response, expiresAt: time.Now().Add(exploreCacheTTL)}
		exploreCache.Unlock()
		writeJSON(w, http.StatusOK, response)
		return
	}

	// Nobody has ingested this area yet. Fall back to a live OSM query
	// so Explore still works everywhere, and lazily seed the database
	// in the background so this area is covered from now on without
	// blocking this user's response on the write.
	tags := tagsForCategory(category)
	if tags == nil {
		tags = allCategoryTags()
	}

	overpassQuery := buildOverpassQuery(lat, lng, radius, tags)
	elements, err := fetchOverpassElements(overpassQuery)
	if err != nil {
		log.Printf("overpass fallback failed: %v", err)
		writeError(w, http.StatusBadGateway, "couldn't load nearby places, please try again")
		return
	}

	go func(els []overpassElement) {
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, el := range els {
			if _, err := upsertOSMElement(bgCtx, el); err != nil {
				log.Printf("lazy upsert failed for element %d: %v", el.ID, err)
			}
		}
	}(elements)

	places := elementsToPlaceDTOs(elements, lat, lng, category)
	places = filterAndLimitPlaces(places, searchQuery, limit)
	sort.Slice(places, func(i, j int) bool { return places[i].Score > places[j].Score })

	response := nearbyPlacesResponse{
		Latitude: lat, Longitude: lng, RadiusMeters: radius,
		Category: category, Cached: false, Places: places,
	}

	exploreCache.Lock()
	exploreCache.entries[cacheKey] = exploreCacheEntry{response: response, expiresAt: time.Now().Add(exploreCacheTTL)}
	exploreCache.Unlock()

	writeJSON(w, http.StatusOK, response)
}

// ---------- Pipeline 2: scheduled ingestion ----------

type ingestAreaRequest struct {
	Latitude     float64 `json:"lat"`
	Longitude    float64 `json:"lng"`
	RadiusMeters int     `json:"radiusMeters"`
}

type ingestRequest struct {
	Areas []ingestAreaRequest `json:"areas"`
}

type ingestResponse struct {
	AreasProcessed int      `json:"areasProcessed"`
	PlacesUpserted int      `json:"placesUpserted"`
	Errors         []string `json:"errors,omitempty"`
}

// Default seed areas used when a request doesn't specify its own.
// Edit this list to the actual cities/localities your users are in —
// these three are placeholders, not a recommendation. Each area costs
// one Overpass request per sweep; the 2-second pause between areas
// below keeps this a respectful load on the shared free server.
var defaultSeedAreas = []ingestAreaRequest{
	{Latitude: 28.7041, Longitude: 77.1025, RadiusMeters: 8000}, // Delhi (placeholder)
	{Latitude: 19.0760, Longitude: 72.8777, RadiusMeters: 8000}, // Mumbai (placeholder)
	{Latitude: 12.9716, Longitude: 77.5946, RadiusMeters: 8000}, // Bengaluru (placeholder)
}

func ingestPlacesHandler(w http.ResponseWriter, r *http.Request) {
	adminToken := os.Getenv("INGEST_ADMIN_TOKEN")
	if adminToken == "" || r.Header.Get("X-Admin-Token") != adminToken {
		writeError(w, http.StatusUnauthorized, "invalid or missing admin token")
		return
	}

	var req ingestRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	areas := req.Areas
	if len(areas) == 0 {
		areas = defaultSeedAreas
	}

	tags := allCategoryTags()
	ctx := r.Context()
	totalUpserted := 0
	var errs []string

	for i, area := range areas {
		radius := area.RadiusMeters
		if radius <= 0 || radius > 20000 {
			radius = 8000
		}

		overpassQuery := buildOverpassQuery(area.Latitude, area.Longitude, radius, tags)
		elements, err := fetchOverpassElements(overpassQuery)
		if err != nil {
			errs = append(errs, fmt.Sprintf("area (%.4f,%.4f): %v", area.Latitude, area.Longitude, err))
			continue
		}

		for _, el := range elements {
			ok, err := upsertOSMElement(ctx, el)
			if err != nil {
				log.Printf("upsert failed for element %d: %v", el.ID, err)
				continue
			}
			if ok {
				totalUpserted++
			}
		}

		if i < len(areas)-1 {
			time.Sleep(2 * time.Second)
		}
	}

	writeJSON(w, http.StatusOK, ingestResponse{
		AreasProcessed: len(areas),
		PlacesUpserted: totalUpserted,
		Errors:         errs,
	})
}

// ---------- Main ----------

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Admin-Token")
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
	mux.HandleFunc("GET /places/nearby", nearbyPlacesHandler)
	mux.HandleFunc("POST /admin/ingest-places", ingestPlacesHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Smibz backend listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, corsMiddleware(mux)); err != nil {
		log.Fatal(err)
	}
}
