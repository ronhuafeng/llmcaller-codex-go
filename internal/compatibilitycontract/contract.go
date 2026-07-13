// Package compatibilitycontract owns the machine-readable shape and strict
// decoding rules for the repository compatibility contract.
package compatibilitycontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Contract is the active compatibility contract recorded in compatibility.json.
type Contract struct {
	FormatVersion int `json:"format_version"`
	Caller        struct {
		Module       string `json:"module"`
		APIInventory string `json:"api_inventory"`
	} `json:"caller"`
	Dependencies []struct {
		Module  string `json:"module"`
		Version string `json:"version"`
	} `json:"dependencies"`
	Gates map[string]struct {
		Path     string `json:"path"`
		Kind     string `json:"kind"`
		Selector string `json:"selector"`
	} `json:"gates"`
}

// Decode rejects unknown fields and trailing JSON so active gates cannot
// silently interpret different contract shapes.
func Decode(data []byte) (Contract, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var contract Contract
	if err := decoder.Decode(&contract); err != nil {
		return Contract{}, fmt.Errorf("decode compatibility contract: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Contract{}, fmt.Errorf("decode compatibility contract: %w", err)
	}
	return contract, nil
}
