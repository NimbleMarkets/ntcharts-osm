package mapview

import (
	_ "embed"
	"encoding/json"
)

//go:embed places.json
var placesJSON []byte

// Place is a curated point of interest with a recommended map zoom level.
type Place struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
	Zoom        int     `json:"zoom"`
	Recommended bool    `json:"recommended,omitempty"`
}

// EmbeddedPlaces returns the curated places shipped with the package.
func EmbeddedPlaces() ([]Place, error) {
	var places []Place
	if err := json.Unmarshal(placesJSON, &places); err != nil {
		return nil, err
	}
	return places, nil
}
