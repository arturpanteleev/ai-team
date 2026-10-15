package gaterun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// shape describes the keys of one JSON object and the shape of what they hold.
// A nil shape is a scalar or an unconstrained value.
type shape struct {
	fields map[string]*shape
	elem   *shape // element shape when the value is an array
}

var resultShape = &shape{fields: map[string]*shape{
	"protocol": nil,
	"verdict":  nil,
	"reason":   nil,
	"evidence": {elem: &shape{fields: map[string]*shape{"kind": nil, "path": nil, "sha256": nil}}},
}}

// rejectAmbiguousKeys makes the result document unambiguous. encoding/json
// keeps the last of duplicate keys and matches keys case-insensitively, so a
// document like {"verdict":"fail","verdict":"pass"} or {"Verdict":"pass"} would
// be read differently by another parser. Here duplicates (also after escape
// decoding) and keys spelled in a different case than the protocol's are
// errors.
func rejectAmbiguousKeys(data []byte, root *shape) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	return walkValue(dec, tok, root)
}

func walkValue(dec *json.Decoder, tok json.Token, sh *shape) error {
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := keyTok.(string)
			folded := strings.ToLower(key)
			if seen[folded] {
				return fmt.Errorf("key %q is duplicated (keys are compared case-insensitively)", key)
			}
			seen[folded] = true
			var child *shape
			if sh != nil && sh.fields != nil {
				if known, exact := sh.fields[key]; exact {
					child = known
				} else {
					for name := range sh.fields {
						if strings.EqualFold(name, key) {
							return fmt.Errorf("key %q must be spelled %q", key, name)
						}
					}
				}
			}
			valueTok, err := dec.Token()
			if err != nil {
				return err
			}
			if err := walkValue(dec, valueTok, child); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	case '[':
		var elem *shape
		if sh != nil {
			elem = sh.elem
		}
		for dec.More() {
			elemTok, err := dec.Token()
			if err != nil {
				return err
			}
			if err := walkValue(dec, elemTok, elem); err != nil {
				return err
			}
		}
		_, err := dec.Token()
		return err
	}
	return nil
}
