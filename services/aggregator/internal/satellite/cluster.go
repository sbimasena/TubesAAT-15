// Package satellite contains the feed-independent P4 clustering rules.
package satellite

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Detection is an active detection snapshot, not an assumed SSE wire schema.
// These strings must come from raw feed tokens, without float round-tripping.
type Detection struct {
	Satellite, AcqDate, AcqTime, Latitude, Longitude, Confidence string
	FRP                                                          float64
}

func (d Detection) Key() string {
	return strings.Join([]string{d.Satellite, d.AcqDate, d.AcqTime, d.Latitude, d.Longitude}, "|")
}

type Fire struct {
	SourceRefID               string
	Latitude, Longitude       float64
	OccurredAt                time.Time
	Severity                  string
	DetectionKeys, Satellites []string
	MaxConfidence             string
	TotalFRPMW                float64
}

type point struct {
	d          Detection
	lat, lon   float64
	at         time.Time
	confidence string
}

func parse(d Detection) (point, error) {
	p := point{d: d}
	var err error
	p.lat, err = strconv.ParseFloat(d.Latitude, 64)
	if err != nil || math.IsNaN(p.lat) || math.IsInf(p.lat, 0) || math.Abs(p.lat) > 90 {
		return p, fmt.Errorf("invalid latitude")
	}
	p.lon, err = strconv.ParseFloat(d.Longitude, 64)
	if err != nil || math.IsNaN(p.lon) || math.IsInf(p.lon, 0) || math.Abs(p.lon) > 180 {
		return p, fmt.Errorf("invalid longitude")
	}
	if d.Satellite == "" || strings.Contains(d.Satellite, "|") {
		return p, fmt.Errorf("invalid satellite")
	}
	if math.IsNaN(d.FRP) || math.IsInf(d.FRP, 0) || d.FRP < 0 {
		return p, fmt.Errorf("invalid FRP")
	}
	if len(d.AcqTime) == 0 || len(d.AcqTime) > 4 {
		return p, fmt.Errorf("invalid acquisition time")
	}
	for _, c := range d.AcqTime {
		if c < '0' || c > '9' {
			return p, fmt.Errorf("invalid acquisition time")
		}
	}
	padded := strings.Repeat("0", 4-len(d.AcqTime)) + d.AcqTime
	p.at, err = time.Parse("2006-01-02 1504", d.AcqDate+" "+padded)
	if err != nil {
		return p, fmt.Errorf("invalid acquisition date/time: %w", err)
	}
	switch strings.ToLower(d.Confidence) {
	case "l", "low":
		p.confidence = "l"
	case "n", "nominal":
		p.confidence = "n"
	case "h", "high":
		p.confidence = "h"
	default:
		return p, fmt.Errorf("invalid confidence")
	}
	return p, nil
}

const earthKM = 6371.0088

func radians(deg float64) float64 { return deg * math.Pi / 180 }
func distance(a, b point) float64 {
	h := math.Pow(math.Sin(radians(b.lat-a.lat)/2), 2) + math.Cos(radians(a.lat))*math.Cos(radians(b.lat))*math.Pow(math.Sin(radians(b.lon-a.lon)/2), 2)
	return 2 * earthKM * math.Asin(math.Sqrt(math.Min(1, h)))
}

// Three-dimensional kilometre cells avoid longitude wrap and polar edge cases.
func cell(p point) [4]int64 {
	lat, lon := radians(p.lat), radians(p.lon)
	return [4]int64{int64(math.Floor(earthKM * math.Cos(lat) * math.Cos(lon))), int64(math.Floor(earthKM * math.Cos(lat) * math.Sin(lon))), int64(math.Floor(earthKM * math.Sin(lat))), p.at.Unix() / 21600}
}

// Cluster builds connected components of the current active snapshot. The caller
// applies ordered updates/retracts durably before supplying that snapshot. Reusing
// a key with conflicting data in one snapshot is rejected instead of guessed.
func Cluster(input []Detection) ([]Fire, int, error) {
	unique := map[string]point{}
	for _, d := range input {
		p, err := parse(d)
		if err != nil {
			return nil, 0, fmt.Errorf("detection %q: %w", d.Key(), err)
		}
		if previous, ok := unique[d.Key()]; ok && previous.d != d {
			return nil, 0, fmt.Errorf("conflicting detection %q", d.Key())
		}
		unique[d.Key()] = p
	}
	keys := make([]string, 0, len(unique))
	for key, p := range unique {
		if p.confidence != "l" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	points := make([]point, len(keys))
	parents := make([]int, len(keys))
	for i, key := range keys {
		points[i] = unique[key]
		parents[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		for parents[i] != i {
			parents[i] = parents[parents[i]]
			i = parents[i]
		}
		return i
	}
	grid := map[[4]int64][]int{}
	for i, p := range points {
		c := cell(p)
		for x := int64(-1); x <= 1; x++ {
			for y := int64(-1); y <= 1; y++ {
				for z := int64(-1); z <= 1; z++ {
					for t := int64(-1); t <= 1; t++ {
						for _, j := range grid[[4]int64{c[0] + x, c[1] + y, c[2] + z, c[3] + t}] {
							dt := p.at.Sub(points[j].at)
							if dt >= -6*time.Hour && dt <= 6*time.Hour && distance(p, points[j]) <= 1 {
								parents[root(i)] = root(j)
							}
						}
					}
				}
			}
		}
		grid[c] = append(grid[c], i)
	}
	groups := map[int][]point{}
	for i, p := range points {
		groups[root(i)] = append(groups[root(i)], p)
	}
	fires := []Fire{}
	singles := 0
	for _, group := range groups {
		if len(group) == 1 {
			singles++
			continue
		}
		fire := Fire{OccurredAt: group[0].at, MaxConfidence: "n", Severity: "WASPADA"}
		satellites := map[string]bool{}
		for _, p := range group {
			fire.DetectionKeys = append(fire.DetectionKeys, p.d.Key())
			satellites[p.d.Satellite] = true
			fire.Latitude += p.lat
			fire.Longitude += p.lon
			fire.TotalFRPMW += p.d.FRP
			if p.at.Before(fire.OccurredAt) {
				fire.OccurredAt = p.at
			}
			if p.confidence == "h" {
				fire.MaxConfidence = "h"
			}
		}
		fire.Latitude /= float64(len(group))
		fire.Longitude /= float64(len(group))
		for satellite := range satellites {
			fire.Satellites = append(fire.Satellites, satellite)
		}
		sort.Strings(fire.Satellites)
		// groups inherit byte-sorted keys, giving stable IDs and summation order.
		hash := sha256.Sum256([]byte(fire.DetectionKeys[0]))
		fire.SourceRefID = "fire-" + hex.EncodeToString(hash[:])
		if len(group) >= 20 || fire.TotalFRPMW >= 100 {
			fire.Severity = "AWAS"
		} else if len(group) >= 6 || fire.TotalFRPMW >= 30 {
			fire.Severity = "SIAGA"
		}
		fires = append(fires, fire)
	}
	sort.Slice(fires, func(i, j int) bool { return fires[i].SourceRefID < fires[j].SourceRefID })
	return fires, singles, nil
}
