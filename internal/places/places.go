// Package places names where a photograph was taken without asking anybody
// (stratus-app#215): the nearest town of more than 5,000 people, from a copy
// of GeoNames built into the binary, so a position never leaves the machine
// to become a name.
//
// It is a guess and says so by being coarse: a town and a country, never a
// street. A position further than [Reach] from every town has no name at all,
// because the nearest one from the middle of a sea is not where anybody was.
package places

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"math"
	"strconv"
	"strings"
	"sync"
)

//go:embed cities.tsv.gz
var citiesGz []byte

//go:embed countries.tsv
var countriesTSV string

// Reach is how far from a town a position may be and still be named after it.
const Reach = 50.0 // km

// Place is a town and the country it is in.
type Place struct {
	Town, Country string
}

func (p Place) String() string {
	if p.Country == "" {
		return p.Town
	}
	return p.Town + ", " + p.Country
}

type town struct {
	name     string
	lat, lon float64
	country  string
}

// cell is a degree of latitude by a degree of longitude, which is what the
// towns are filed under so that a lookup reads a few hundred and not seventy
// thousand.
type cell struct{ lat, lon int }

var (
	loaded    sync.Once
	byCell    map[cell][]town
	countries map[string]string
)

// load reads the table the first time a name is asked for, and not at start:
// it is a megabyte and a half of memory that a server whose photographs
// carry no positions never needs.
func load() {
	countries = map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(countriesTSV), "\n") {
		code, name, ok := strings.Cut(line, "\t")
		if ok {
			countries[code] = name
		}
	}
	byCell = map[cell][]town{}
	r, err := gzip.NewReader(bytes.NewReader(citiesGz))
	if err != nil {
		panic("places: the built-in table is not gzip: " + err.Error())
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		f := strings.Split(scanner.Text(), "\t")
		if len(f) != 4 {
			continue
		}
		lat, errLat := strconv.ParseFloat(f[1], 64)
		lon, errLon := strconv.ParseFloat(f[2], 64)
		if errLat != nil || errLon != nil {
			continue
		}
		c := cellOf(lat, lon)
		byCell[c] = append(byCell[c], town{name: f[0], lat: lat, lon: lon, country: f[3]})
	}
}

func cellOf(lat, lon float64) cell {
	return cell{int(math.Floor(lat)), int(math.Floor(lon))}
}

// Nearest is the town nearest to a position, within [Reach], and false where
// there is none.
func Nearest(lat, lon float64) (Place, bool) {
	loaded.Do(load)
	// A degree of latitude is 111 km, so one cell either way covers the reach;
	// a degree of longitude shrinks towards the poles, so more of them do.
	across := 180
	if c := math.Cos(lat * math.Pi / 180); c > 0.01 {
		across = min(int(math.Ceil(Reach/(111*c))), 180)
	}

	home := cellOf(lat, lon)
	best, bestKm := town{}, math.Inf(1)
	for dLat := -1; dLat <= 1; dLat++ {
		for dLon := -across; dLon <= across; dLon++ {
			lonCell := (home.lon+dLon+180)%360 - 180
			if lonCell < -180 {
				lonCell += 360
			}
			for _, t := range byCell[cell{home.lat + dLat, lonCell}] {
				if km := distance(lat, lon, t.lat, t.lon); km < bestKm {
					best, bestKm = t, km
				}
			}
		}
	}
	if bestKm > Reach {
		return Place{}, false
	}
	return Place{Town: best.name, Country: countries[best.country]}, true
}

// distance is the great-circle distance in kilometres.
func distance(lat1, lon1, lat2, lon2 float64) float64 {
	const earth = 6371.0
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * earth * math.Asin(math.Sqrt(a))
}
